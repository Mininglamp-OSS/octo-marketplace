package db

import (
	"context"
	"encoding/json"
	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

type frozenEdgeInstaller struct {
	calls     int
	key, body string
	err       error
}

func (f *frozenEdgeInstaller) InstallCapability(_ context.Context, _, _, _, key string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	f.calls++
	f.key = key
	raw, _ := json.Marshal(in)
	f.body = string(raw)
	if f.err != nil {
		return nil, f.err
	}
	return &fleet.CapabilityInstallResult{ExpertID: "created"}, nil
}

func TestFrozenInstallHTTPIgnoresLiveRelationEdits(t *testing.T) {
	for _, route := range []string{"new-keyed", "legacy-keyed"} {
		for _, mode := range []string{"delete_draft", "delete_and_publish", "disable_live", "update_and_publish"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				database := reviewDB(t)
				ctx := context.Background()
				remote := &frozenEdgeInstaller{err: context.DeadlineExceeded}
				svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
				req := managedSkillWrite("1.0.0", "Frozen Skill", model.PluginVisibilityPrivate)
				req.Package = json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"---\nname: frozen\ndescription: fixture\n---\nORIGINAL_SKILL_BYTES"}]}`)
				skill, err := svc.Create(ctx, managedOwner(), req)
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, managedOwner(), skill.Plugin.ID)
				rootReq := managedWrite("1.0.0", "original expert instructions", model.PluginVisibilityPrivate)
				rootReq.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.Plugin.ID, TargetVersionID: skill.Plugin.CurrentVersionID, Type: "expert_skill"}}
				root, err := svc.Create(ctx, managedOwner(), rootReq)
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, managedOwner(), root.Plugin.ID)
				gin.SetMode(gin.TestMode)
				engine := gin.New()
				auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Owner"}, "space-a")
				pluginhandler.New(svc).Register(engine.Group("/api/v1", auth.Handler()))
				invoke := func(key string) *httptest.ResponseRecorder {
					w := httptest.NewRecorder()
					engine.ServeHTTP(w, installationHTTPRequest(root.Plugin.ID, route, key))
					return w
				}
				first := invoke("frozen-edge")
				if first.Code != 503 || remote.calls != 1 {
					t.Fatalf("initial %d %s calls=%d", first.Code, first.Body.String(), remote.calls)
				}
				originalBody, originalKey := remote.body, remote.key
				if !strings.Contains(originalBody, "ORIGINAL_SKILL_BYTES") {
					t.Fatal("initial skill missing")
				}
				const evidence = `SELECT JSON_OBJECT('state',attempt_state,'unknown',had_unknown,'plan',frozen_plan_json,'request',request_fingerprint,'deadline',retry_until) FROM plugin_install_attempts WHERE plugin_id=?`
				var before, after string
				if err = database.QueryRow(evidence, root.Plugin.ID).Scan(&before); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "delete_draft", "delete_and_publish":
					if _, err = svc.Update(ctx, managedOwner(), root.Plugin.ID, managedWrite("2.0.0", "new expert without skill", model.PluginVisibilityPrivate)); err != nil {
						t.Fatal(err)
					}
					var active int
					if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_relations WHERE source_plugin_id=? AND deleted_at IS NULL AND status=1`, root.Plugin.ID).Scan(&active); err != nil || active != 0 {
						t.Fatalf("delete not applied count=%d err=%v", active, err)
					}
					if mode == "delete_and_publish" {
						publishManaged(t, svc, managedOwner(), root.Plugin.ID)
					}
				case "disable_live":
					// The write API has no relation-status toggle. Inject a disabled live row
					// to verify it cannot silently revoke an immutable released snapshot.
					res, e := database.Exec(`UPDATE plugin_relations SET status=0 WHERE source_plugin_id=? AND deleted_at IS NULL`, root.Plugin.ID)
					if e != nil {
						t.Fatal(e)
					}
					n, _ := res.RowsAffected()
					if n != 1 {
						t.Fatalf("disabled rows=%d", n)
					}
				case "update_and_publish":
					req.Version = "2.0.0"
					req.Package = json.RawMessage(strings.ReplaceAll(string(req.Package), "ORIGINAL_SKILL_BYTES", "UPDATED_SKILL_BYTES"))
					newer, e := svc.Update(ctx, managedOwner(), skill.Plugin.ID, req)
					if e != nil {
						t.Fatal(e)
					}
					publishManaged(t, svc, managedOwner(), skill.Plugin.ID)
					next := managedWrite("2.0.0", "new expert v2", model.PluginVisibilityPrivate)
					next.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.Plugin.ID, TargetVersionID: newer.Plugin.CurrentVersionID, Type: "expert_skill"}}
					if _, err = svc.Update(ctx, managedOwner(), root.Plugin.ID, next); err != nil {
						t.Fatal(err)
					}
					publishManaged(t, svc, managedOwner(), root.Plugin.ID)
				}
				retried := invoke("frozen-edge")
				if retried.Code != 503 || remote.calls != 2 || remote.body != originalBody || remote.key != originalKey {
					t.Fatalf("retry changed: status=%d calls=%d sameBody=%t sameKey=%t %s", retried.Code, remote.calls, remote.body == originalBody, remote.key == originalKey, retried.Body.String())
				}
				if err = database.QueryRow(evidence, root.Plugin.ID).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if before != after {
					t.Fatal("frozen bytes/deadline/UNKNOWN changed")
				}
				if _, err = database.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id=?`, skill.Plugin.ID); err != nil {
					t.Fatal(err)
				}
				denied := invoke("frozen-edge")
				if denied.Code == 200 || denied.Code == 503 || remote.calls != 2 {
					t.Fatalf("delisted dispatched: %d calls=%d", denied.Code, remote.calls)
				}
				if err = database.QueryRow(evidence, root.Plugin.ID).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if before != after {
					t.Fatal("refusal erased evidence")
				}
				if _, err = database.Exec(`UPDATE plugins SET listing_state='published' WHERE plugin_id=?`, skill.Plugin.ID); err != nil {
					t.Fatal(err)
				}
				remote.err = nil
				done := invoke("frozen-edge")
				if done.Code != 200 || remote.calls != 3 || remote.body != originalBody || remote.key != originalKey {
					t.Fatalf("restore: %d %s", done.Code, done.Body.String())
				}
				fresh := invoke("fresh-frozen-edge")
				if fresh.Code != 200 || remote.calls != 4 {
					t.Fatalf("fresh %d %s", fresh.Code, fresh.Body.String())
				}
				switch mode {
				case "delete_and_publish":
					if strings.Contains(remote.body, "ORIGINAL_SKILL_BYTES") {
						t.Fatal("fresh plan retained removed skill")
					}
				case "update_and_publish":
					if !strings.Contains(remote.body, "UPDATED_SKILL_BYTES") || strings.Contains(remote.body, "ORIGINAL_SKILL_BYTES") {
						t.Fatal("fresh plan missed new pin")
					}
				default:
					if !strings.Contains(remote.body, "ORIGINAL_SKILL_BYTES") {
						t.Fatal("draft/live edit replaced released snapshot")
					}
				}
			})
		}
	}
}
