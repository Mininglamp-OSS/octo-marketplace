package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type frozenGraphInstaller struct {
	recoveryHTTPInstaller
	key     string
	request []byte
}

func (f *frozenGraphInstaller) InstallCapability(ctx context.Context, token, space, workspace, key string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	f.key = key
	f.request, _ = json.Marshal(in)
	return f.recoveryHTTPInstaller.InstallCapability(ctx, token, space, workspace, key, in)
}

// A later editable graph is not the authorization source for an immutable
// installation plan. Prove this with normal save/publication APIs and real SQL,
// while current delisting and tenant checks continue to stop the same retry.
func TestFrozenInstallHTTPPreservesReleasedGraphAcrossLaterEdits(t *testing.T) {
	for _, route := range []string{"new-keyed", "legacy-keyed"} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unknown=%t", route, unknown), func(t *testing.T) {
				database := reviewDB(t)
				ctx := context.Background()
				repo := pluginrepo.New(database)
				if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), true, "user-1", "Owner"); err != nil {
					t.Fatal(err)
				}
				remote := &frozenGraphInstaller{}
				svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
				skillReq := managedSkillWrite("1.0.0", "Frozen Skill", model.PluginVisibilitySpace)
				skillReq.Package = json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"---\nname: frozen\ndescription: fixture\n---\nOriginal body"}]}`)
				skill, err := svc.Create(ctx, managedOwner(), skillReq)
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, managedOwner(), skill.Plugin.ID)
				skillState, err := repo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
				if err != nil || skillState.PublishedVersionID == nil {
					t.Fatalf("missing Skill release: %v", err)
				}
				rootReq := managedWrite("1.0.0", "original expert instructions", model.PluginVisibilityPrivate)
				rootReq.Relations = []pluginsvc.RelationRequest{{Type: "expert_skill", TargetPluginID: skill.Plugin.ID, TargetVersionID: skillState.PublishedVersionID}}
				root, err := svc.Create(ctx, managedOwner(), rootReq)
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, managedOwner(), root.Plugin.ID)
				gin.SetMode(gin.TestMode)
				engine := func(space string) *gin.Engine {
					e := gin.New()
					auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Owner"}, space)
					pluginhandler.New(svc).Register(e.Group("/api/v1", auth.Handler()))
					return e
				}
				own, foreign := engine("space-a"), engine("space-b")
				const key = "frozen-graph-original"
				send := func(e *gin.Engine) *httptest.ResponseRecorder {
					r := httptest.NewRecorder()
					e.ServeHTTP(r, installationHTTPRequest(root.Plugin.ID, route, key))
					return r
				}
				if unknown {
					remote.err = errors.New("lost response")
				}
				first := send(own)
				want := 200
				if unknown {
					want = 503
				}
				if first.Code != want || remote.calls != 1 {
					t.Fatalf("initial status=%d calls=%d body=%s", first.Code, remote.calls, first.Body.String())
				}
				original, originalKey := string(remote.request), remote.key
				encoded, _ := json.Marshal(key)
				sum := sha256.Sum256(encoded)
				digest := hex.EncodeToString(sum[:])
				evidence := func() string {
					t.Helper()
					a, err := repo.GetInstallAttempt(ctx, tenantScope(), "workspace-1", digest)
					if err != nil || a == nil {
						t.Fatalf("attempt missing: %v", err)
					}
					raw, err := json.Marshal(a)
					if err != nil {
						t.Fatal(err)
					}
					return string(raw)
				}
				checkBlocked := func(e *gin.Engine, status int) {
					t.Helper()
					before, calls := evidence(), remote.calls
					response := send(e)
					if response.Code != status || remote.calls != calls || evidence() != before {
						t.Fatalf("blocked retry status=%d calls=%d unchanged=%t body=%s", response.Code, remote.calls-calls, evidence() == before, response.Body.String())
					}
				}
				// Keep an UNKNOWN receipt unknown while access is revoked. No reset,
				// replacement key or new frozen plan may be created by the refusal.
				if _, err = svc.Delist(ctx, managedReviewer(), pluginsvc.DelistParams{PluginID: skill.Plugin.ID}); err != nil {
					t.Fatal(err)
				}
				checkBlocked(own, 403)
				checkBlocked(foreign, 404)
				skillReq.Version = "1.0.1"
				if _, err = svc.Update(ctx, managedOwner(), skill.Plugin.ID, skillReq); err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, managedOwner(), skill.Plugin.ID)
				remote.err = nil
				checkRetry := func() {
					t.Helper()
					calls := remote.calls
					response := send(own)
					if response.Code != 200 || remote.calls != calls+1 || string(remote.request) != original || remote.key != originalKey {
						t.Fatalf("frozen retry drifted: status=%d calls=%d same-bytes=%t same-key=%t body=%s", response.Code, remote.calls-calls, string(remote.request) == original, remote.key == originalKey, response.Body.String())
					}
				}
				// Deleting the editable edge must not change the selected release.
				removed := managedWrite("2.0.0", "later draft without skill", model.PluginVisibilityPrivate)
				if _, err = svc.Update(ctx, managedOwner(), root.Plugin.ID, removed); err != nil {
					t.Fatal(err)
				}
				checkRetry()
				publishManaged(t, svc, managedOwner(), root.Plugin.ID)
				checkRetry()
				// Publishing another pin changes a new operation, never the old key.
				skillReq.Version = "2.0.0"
				skillReq.Package = json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"---\nname: frozen\ndescription: fixture\n---\nLater body"}]}`)
				_, err = svc.Update(ctx, managedOwner(), skill.Plugin.ID, skillReq)
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, managedOwner(), skill.Plugin.ID)
				skillState, err = repo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
				if err != nil || skillState.PublishedVersionID == nil {
					t.Fatalf("missing later Skill release: %v", err)
				}
				rootReq.Version = "3.0.0"
				rootReq.Relations[0].TargetVersionID = skillState.PublishedVersionID
				if _, err = svc.Update(ctx, managedOwner(), root.Plugin.ID, rootReq); err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, managedOwner(), root.Plugin.ID)
				checkRetry()
			})
		}
	}
}
