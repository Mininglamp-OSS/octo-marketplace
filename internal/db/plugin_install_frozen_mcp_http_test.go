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

func TestFrozenInstallHTTPRejectsCredentialPersistence(t *testing.T) {
	for _, route := range []string{"new-keyed", "legacy-keyed"} {
		t.Run(route, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			seed(t, database, seedPlugin{id: "connector", typ: "connector", visibility: "private", listingState: "published"})
			setConfig := func(raw string) {
				t.Helper()
				pkg, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-package-2.0.json", "attachments": []any{map[string]string{"path": "mcp.json", "content_type": "raw", "mime_type": "application/json", "raw_content": raw}}})
				if _, err := database.Exec(`UPDATE plugins SET plugin_json=? WHERE plugin_id='connector'`, pkg); err != nil {
					t.Fatal(err)
				}
			}
			safe := `{"mcpServers":{"search":{"url":"https://example.test/mcp","headers":{"Authorization":"${API_TOKEN}"}}}}`
			unsafe := `{"mcpServers":{"search":{"url":"https://example.test/mcp","headers":{"Authorization":"Bearer synthetic-db-secret"}}}}`
			setConfig(safe)
			remote := &frozenEdgeInstaller{}
			svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
			req := managedWrite("1.0.0", "credential persistence fixture", model.PluginVisibilityPrivate)
			req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: "connector", Type: "expert_connector"}}
			root, err := svc.Create(ctx, managedOwner(), req)
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
			for _, config := range []string{unsafe, `{"mcpServers":{"search":{"command":"search","env":{"API_KEY":"synthetic-db-secret"}}}}`, `{"mcpServers":{"search":{"url":"https://example.test/mcp?api_key=synthetic-db-secret"}}}`} {
				setConfig(config)
				w := invoke("credential-check")
				var count int
				if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_install_attempts WHERE plugin_id=?`, root.Plugin.ID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if w.Code != 400 || !strings.Contains(w.Body.String(), "credential_not_persistable") || strings.Contains(w.Body.String(), "synthetic-db-secret") || remote.calls != 0 || count != 0 {
					t.Fatalf("unsafe persistence/dispatch: status=%d calls=%d rows=%d body=%s", w.Code, remote.calls, count, w.Body.String())
				}
			}
			setConfig(safe)
			remote.err = context.DeadlineExceeded
			w := invoke("credential-check")
			if w.Code != 503 || remote.calls != 1 {
				t.Fatalf("safe first %d %s", w.Code, w.Body.String())
			}
			var frozen string
			if err = database.QueryRow(`SELECT frozen_plan_json FROM plugin_install_attempts WHERE plugin_id=?`, root.Plugin.ID).Scan(&frozen); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(frozen, "synthetic-db-secret") || !strings.Contains(frozen, "${API_TOKEN}") {
				t.Fatal("secret-free frozen configuration changed")
			}
			original, key := remote.body, remote.key
			// New current credentials cannot be copied into the already safe frozen plan.
			setConfig(unsafe)
			remote.err = nil
			w = invoke("credential-check")
			if w.Code != 200 || remote.calls != 2 || remote.body != original || remote.key != key {
				t.Fatalf("safe frozen retry changed: %d %s", w.Code, w.Body.String())
			}
			var after string
			if err = database.QueryRow(`SELECT frozen_plan_json FROM plugin_install_attempts WHERE plugin_id=?`, root.Plugin.ID).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != frozen {
				t.Fatal("safe retry rewrote frozen plan")
			}
		})
	}
}
