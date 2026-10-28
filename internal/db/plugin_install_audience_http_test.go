package db

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestInstallHTTPChecksIndependentSpaceAudience(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, kind := range []string{"skill", "expert"} {
			t.Run(fmt.Sprintf("pinned=%t/%s", pinned, kind), func(t *testing.T) {
				database := reviewDB(t)
				remote := &frozenEdgeInstaller{}
				svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(pinned).WithPinnedInstallation(pinned).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
				ctx := context.Background()
				req := managedSkillWrite("1.0.0", "Audience Skill", model.PluginVisibilitySpace)
				if kind == "expert" {
					req = managedWrite("1.0.0", "member instructions", model.PluginVisibilitySpace)
				}
				target, err := svc.Create(ctx, managedOwner(), req)
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, managedOwner(), target.Plugin.ID)
				var version string
				if err := database.QueryRow(`SELECT published_version_id FROM plugins WHERE plugin_id=?`, target.Plugin.ID).Scan(&version); err != nil {
					t.Fatal(err)
				}
				rootReq := managedWrite("1.0.0", "root instructions", model.PluginVisibilitySpace)
				rel := pluginsvc.RelationRequest{TargetPluginID: target.Plugin.ID, Type: "expert_skill"}
				if kind == "expert" {
					rootReq.Type = model.PluginTypeExpertTeam
					rootReq.Manifest = json.RawMessage(strings.ReplaceAll(string(rootReq.Manifest), `"plugin_type":"expert"`, `"plugin_type":"expert_team"`))
					rel.Type = "expert_team_expert"
					rel.Data = json.RawMessage(`{"member_key":"leader","role":"leader","is_leader":true}`)
				}
				if pinned {
					rel.TargetVersionID = &version
				}
				rootReq.Relations = []pluginsvc.RelationRequest{rel}
				root, err := svc.Create(ctx, managedOwner(), rootReq)
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, managedOwner(), root.Plugin.ID)
				gin.SetMode(gin.TestMode)
				engine := gin.New()
				auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Owner"}, "space-a")
				pluginhandler.New(svc).Register(engine.Group("/api/v1", auth.Handler()))
				// Inject stale metadata that normal reference writes correctly reject.
				// The caller still reads the target, but a global source cannot distribute it.
				for _, visibility := range []string{"system", "public", "space"} {
					if _, err := database.Exec(`UPDATE plugins SET visibility=? WHERE plugin_id=?`, visibility, root.Plugin.ID); err != nil {
						t.Fatal(err)
					}
					for _, route := range []string{"new-keyed", "legacy-keyed", "legacy-keyless"} {
						before := remote.calls
						w := httptest.NewRecorder()
						engine.ServeHTTP(w, installationHTTPRequest(root.Plugin.ID, route, visibility+"-"+route))
						if visibility == "space" {
							if w.Code != 200 || remote.calls != before+1 {
								t.Fatalf("legal %s: %d %s", route, w.Code, w.Body.String())
							}
						} else if w.Code != 403 || remote.calls != before {
							t.Fatalf("%s/%s leaked Space target: %d calls=%d %s", visibility, route, w.Code, remote.calls-before, w.Body.String())
						}
					}
				}
				// A same-Space source cannot use a now-foreign Space target either.
				if _, err := database.Exec(`UPDATE plugins SET space_id='other-space' WHERE plugin_id=?`, target.Plugin.ID); err != nil {
					t.Fatal(err)
				}
				for _, route := range []string{"new-keyed", "legacy-keyed", "legacy-keyless"} {
					before := remote.calls
					w := httptest.NewRecorder()
					engine.ServeHTTP(w, installationHTTPRequest(root.Plugin.ID, route, "foreign-"+route))
					if (w.Code != 403 && w.Code != 404) || remote.calls != before {
						t.Fatalf("foreign %s dispatched: %d calls=%d", route, w.Code, remote.calls-before)
					}
				}
			})
		}
	}
}
