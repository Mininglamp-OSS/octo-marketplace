package db

import (
	"context"
	"encoding/json"
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

func installSecurityPackage(path, content string) string {
	raw, _ := json.Marshal(map[string]any{"attachments": []map[string]string{{"path": path, "content_type": "raw", "raw_content": content}}})
	return string(raw)
}

func TestFrozenMCPHTTPProtectedMySQLRoundTrip(t *testing.T) {
	const credential = "synthetic-http-connector-credential"
	for _, route := range []string{"new-keyed", "legacy-keyed"} {
		t.Run(route, func(t *testing.T) {
			database := reviewDB(t)
			config := `{"mcpServers":{"remote":{"url":"https://example.invalid/mcp","headers":{"Authorization":"Bearer ` + credential + `"}}}}`
			seed(t, database, seedPlugin{id: "connector", typ: "connector", visibility: "private", listingState: "published", pkg: installSecurityPackage("mcp.json", config)})
			remote := &frozenGraphInstaller{}
			remote.err = context.DeadlineExceeded
			svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
			req := managedWrite("1.0.0", "released instructions", model.PluginVisibilityPrivate)
			req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: "connector", Type: "expert_connector"}}
			root, err := svc.Create(context.Background(), managedOwner(), req)
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, svc, managedOwner(), root.Plugin.ID)
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Owner"}, "space-a")
			pluginhandler.New(svc).Register(engine.Group("/api/v1", auth.Handler()))
			invoke := func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, installationHTTPRequest(root.Plugin.ID, route, "protected-mcp"))
				return w
			}
			first := invoke()
			if first.Code != 503 || remote.calls != 1 {
				t.Fatalf("first=%d %s calls=%d", first.Code, first.Body.String(), remote.calls)
			}
			if !strings.Contains(string(remote.request), credential) {
				t.Fatal("authorized Fleet request lost credential")
			}
			originalKey, originalBody := remote.key, string(remote.request)
			const evidence = `SELECT JSON_OBJECT('state',attempt_state,'unknown',had_unknown,'plan',frozen_plan_json,'deadline',retry_until,'request',request_fingerprint,'completed',completed) FROM plugin_install_attempts WHERE plugin_id=?`
			var original, deadline string
			if err := database.QueryRow(`SELECT frozen_plan_json,CAST(retry_until AS CHAR) FROM plugin_install_attempts WHERE plugin_id=?`, root.Plugin.ID).Scan(&original, &deadline); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(original, credential) {
				t.Fatal("MySQL frozen plan exposed credential")
			}
			var plan map[string]json.RawMessage
			if err := json.Unmarshal([]byte(original), &plan); err != nil || len(plan["ProtectedMCPConfigs"]) < 32 {
				t.Fatal("protected MCP payload missing", err)
			}
			if _, err := database.Exec(`UPDATE plugin_install_attempts SET frozen_plan_json=JSON_SET(frozen_plan_json,'$.ProtectedMCPConfigs','dGFtcGVyZWQ=') WHERE plugin_id=?`, root.Plugin.ID); err != nil {
				t.Fatal(err)
			}
			var before, after string
			if err := database.QueryRow(evidence, root.Plugin.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			remote.err = nil
			bad := invoke()
			if err := database.QueryRow(evidence, root.Plugin.ID).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if bad.Code != 500 || remote.calls != 1 || before != after || strings.Contains(bad.Body.String(), credential) {
				t.Fatalf("tampering changed dispatch/evidence: status=%d calls=%d", bad.Code, remote.calls)
			}
			if _, err := database.Exec(`UPDATE plugin_install_attempts SET frozen_plan_json=? WHERE plugin_id=?`, original, root.Plugin.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE plugins SET plugin_json=? WHERE plugin_id='connector'`, installSecurityPackage("mcp.json", `{"mcpServers":{"latest":{"command":"changed"}}}`)); err != nil {
				t.Fatal(err)
			}
			svc.WithInstallationHMACKeys("new", map[string]string{"test": "synthetic-installation-test-key-32-bytes", "new": strings.Repeat("n", 32)})
			for i := 0; i < 2; i++ {
				ok := invoke()
				if ok.Code != 200 || remote.calls != 2+i || remote.key != originalKey || string(remote.request) != originalBody {
					t.Fatalf("frozen retry/replay drifted: status=%d calls=%d", ok.Code, remote.calls)
				}
			}
			var stored, retainedDeadline string
			if err := database.QueryRow(`SELECT frozen_plan_json,CAST(retry_until AS CHAR) FROM plugin_install_attempts WHERE plugin_id=?`, root.Plugin.ID).Scan(&stored, &retainedDeadline); err != nil || stored != original || retainedDeadline != deadline {
				t.Fatal("retry rewrote protected plan/deadline", err)
			}
		})
	}
}
