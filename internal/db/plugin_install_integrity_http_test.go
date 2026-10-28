package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

// Publish through the real service, round-trip the signed plan through MySQL,
// then corrupt only eligibility metadata. Both adapters must stop before Fleet.
func TestInstallHTTPAuthenticatesFrozenEligibilityMetadata(t *testing.T) {
	for _, route := range []string{"new-keyed", "legacy-keyed"} {
		t.Run(route, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			remote := &recoveryHTTPInstaller{}
			svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
			req := managedSkillWrite("1.0.0", "Integrity Skill", model.PluginVisibilityPrivate)
			req.Package = json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"---\nname: integrity\ndescription: fixture\n---\nBody"}]}`)
			skill, err := svc.Create(ctx, managedOwner(), req)
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, svc, managedOwner(), skill.Plugin.ID)
			rootReq := managedWrite("1.0.0", "released instructions", model.PluginVisibilityPrivate)
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
			for _, mode := range []string{"omit_skill", "omit_edges", "unsigned", "bad_mac"} {
				t.Run(mode, func(t *testing.T) {
					key := "integrity-" + mode
					remote.err = errors.New("lost response")
					initialCalls := remote.calls
					first := httptest.NewRecorder()
					engine.ServeHTTP(first, installationHTTPRequest(root.Plugin.ID, route, key))
					if first.Code != 503 || remote.calls != initialCalls+1 {
						t.Fatalf("first=%d body=%s calls=%d", first.Code, first.Body.String(), remote.calls)
					}
					encoded, _ := json.Marshal(key)
					sum := sha256.Sum256(encoded)
					digest := hex.EncodeToString(sum[:])
					var original []byte
					if err = database.QueryRow(`SELECT frozen_plan_json FROM plugin_install_attempts WHERE plugin_id=? AND key_digest=?`, root.Plugin.ID, digest).Scan(&original); err != nil {
						t.Fatal(err)
					}
					var plan map[string]json.RawMessage
					if err = json.Unmarshal(original, &plan); err != nil {
						t.Fatal(err)
					}
					if len(plan["EligibilityHMAC"]) != 66 {
						t.Fatal("MAC did not round-trip through MySQL")
					}
					switch mode {
					case "omit_skill":
						plan["NodeIDs"], _ = json.Marshal([]string{root.Plugin.ID})
						plan["Edges"] = json.RawMessage(`[]`)
					case "omit_edges":
						plan["Edges"] = json.RawMessage(`[]`)
					case "unsigned":
						delete(plan, "EligibilityHMAC")
					case "bad_mac":
						plan["EligibilityHMAC"] = json.RawMessage(`"invalid"`)
					}
					corrupted, err := json.Marshal(plan)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = database.Exec(`UPDATE plugin_install_attempts SET frozen_plan_json=? WHERE plugin_id=? AND key_digest=?`, string(corrupted), root.Plugin.ID, digest); err != nil {
						t.Fatal(err)
					}
					if mode == "omit_skill" {
						if _, err = database.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id=?`, skill.Plugin.ID); err != nil {
							t.Fatal(err)
						}
					}
					const evidence = `SELECT JSON_OBJECT('state',attempt_state,'unknown',had_unknown,'completed',completed,'plan',frozen_plan_json,'request',request_fingerprint,'input',input_fingerprint,'deadline',retry_until,'agent',agent_id,'team',team_id,'lease',lease_token,'lease_until',lease_until) FROM plugin_install_attempts WHERE plugin_id=? AND key_digest=?`
					var before, after string
					if err = database.QueryRow(evidence, root.Plugin.ID, digest).Scan(&before); err != nil {
						t.Fatal(err)
					}
					remote.err = nil
					retry := httptest.NewRecorder()
					engine.ServeHTTP(retry, installationHTTPRequest(root.Plugin.ID, route, key))
					if err = database.QueryRow(evidence, root.Plugin.ID, digest).Scan(&after); err != nil {
						t.Fatal(err)
					}
					if retry.Code != 500 || !strings.Contains(retry.Body.String(), "artifact_unavailable") || remote.calls != initialCalls+1 || before != after {
						t.Fatalf("retry=%d body=%s calls=%d unchanged=%t", retry.Code, retry.Body.String(), remote.calls, before == after)
					}
					// Restoring original authenticated bytes restores normal recovery; no signing
					// or rebuilding of corrupted/unsigned rows is performed by the application.
					if _, err = database.Exec(`UPDATE plugin_install_attempts SET frozen_plan_json=? WHERE plugin_id=? AND key_digest=?`, string(original), root.Plugin.ID, digest); err != nil {
						t.Fatal(err)
					}
					if _, err = database.Exec(`UPDATE plugins SET listing_state='published' WHERE plugin_id=?`, skill.Plugin.ID); err != nil {
						t.Fatal(err)
					}
					restored := httptest.NewRecorder()
					engine.ServeHTTP(restored, installationHTTPRequest(root.Plugin.ID, route, key))
					if restored.Code != 200 || remote.calls != initialCalls+2 {
						t.Fatalf("restored=%d body=%s calls=%d", restored.Code, restored.Body.String(), remote.calls)
					}
				})
			}
		})
	}
}
