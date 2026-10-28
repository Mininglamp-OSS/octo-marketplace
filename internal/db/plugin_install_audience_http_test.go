package db

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type audienceHTTPInstaller struct{ calls int }

func (f *audienceHTTPInstaller) InstallCapability(_ context.Context, _, _, _, _ string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	f.calls++
	if in.Definition.ExpertTeam != nil {
		return &fleet.CapabilityInstallResult{ExpertTeamID: "created-team"}, nil
	}
	return &fleet.CapabilityInstallResult{ExpertID: "created-expert"}, nil
}

// Existing legacy records can predate the source-audience write checks. Test
// installation through real scoped SQL and both HTTP adapters, including managed
// releases, rather than assuming caller visibility authorizes distribution.
func TestInstallHTTPEnforcesIndependentReferenceAudience(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, targetKind := range []string{"skill", "expert"} {
			t.Run(fmt.Sprintf("managed=%t/%s", managed, targetKind), func(t *testing.T) {
				database := reviewDB(t)
				ctx := context.Background()
				repo := pluginrepo.New(database)
				remote := &audienceHTTPInstaller{}
				svc := pluginsvc.New(repo).WithVersionManagement(managed).WithCapabilityInstaller(remote)
				owner := managedOwner()
				if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), true, owner.UID, owner.Name); err != nil {
					t.Fatal(err)
				}
				targetReq := managedSkillWrite("1.0.0", "Scoped Dependency", model.PluginVisibilitySpace)
				if targetKind == "expert" {
					targetReq = managedWrite("1.0.0", "scoped-dependency-content", model.PluginVisibilitySpace)
				}
				target, err := svc.Create(ctx, owner, targetReq)
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, owner, target.Plugin.ID)
				sourceReq := compatibilitySourceWrite(targetKind, "1.0.0")
				sourceReq.Relations = []pluginsvc.RelationRequest{{
					Type:           map[string]string{"skill": "expert_skill", "expert": "expert_team_expert"}[targetKind],
					TargetPluginID: target.Plugin.ID,
				}}
				if targetKind == "expert" {
					sourceReq.Relations[0].Data = json.RawMessage(`{"member_key":"leader","is_leader":true,"role":"leader"}`)
				}
				source, err := svc.Create(ctx, owner, sourceReq)
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, owner, source.Plugin.ID)
				gin.SetMode(gin.TestMode)
				engine := gin.New()
				auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: owner.UID, Name: owner.Name}, owner.SpaceID)
				pluginhandler.New(svc).Register(engine.Group("/api/v1", auth.Handler()))
				for _, tc := range []struct {
					name, sourceVisibility, targetVisibility, targetSpace string
					status                                                int
				}{
					{"system_to_space", "system", "space", "space-a", http.StatusForbidden},
					{"public_to_space", "public", "space", "space-a", http.StatusForbidden},
					{"private_to_space", "private", "space", "space-a", http.StatusOK},
					{"space_to_space", "space", "space", "space-a", http.StatusOK},
					{"system_to_system", "system", "system", "space-a", http.StatusOK},
					{"public_to_system", "public", "system", "space-a", http.StatusOK},
					{"foreign_space", "private", "space", "space-b", http.StatusForbidden},
				} {
					t.Run(tc.name, func(t *testing.T) {
						// Simulate retained pre-fix metadata; normal writes may not create
						// the invalid graph. Release contents/relations remain unchanged.
						if _, err := database.Exec(`UPDATE plugins SET visibility=? WHERE plugin_id=?`, tc.sourceVisibility, source.Plugin.ID); err != nil {
							t.Fatal(err)
						}
						if _, err := database.Exec(`UPDATE plugins SET visibility=?,space_id=? WHERE plugin_id=?`, tc.targetVisibility, tc.targetSpace, target.Plugin.ID); err != nil {
							t.Fatal(err)
						}
						for _, route := range []string{"new-keyed", "legacy-keyed"} {
							t.Run(route, func(t *testing.T) {
								before := remote.calls
								rec := httptest.NewRecorder()
								engine.ServeHTTP(rec, installationHTTPRequest(source.Plugin.ID, route, tc.name+"-"+route))
								wantCalls := before
								if tc.status == http.StatusOK {
									wantCalls++
								}
								if rec.Code != tc.status || remote.calls != wantCalls {
									t.Fatalf("status=%d calls=%d body=%s", rec.Code, remote.calls-before, rec.Body.String())
								}
								if tc.status != http.StatusOK {
									var body struct {
										Error struct{ Code string } `json:"error"`
									}
									if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "FORBIDDEN" {
										t.Fatalf("unsafe error envelope: %s err=%v", rec.Body.String(), err)
									}
									for _, hidden := range []string{source.Plugin.ID, target.Plugin.ID, "Scoped Dependency", "scoped-dependency-content", "space-b"} {
										if strings.Contains(rec.Body.String(), hidden) {
											t.Fatalf("dependency data disclosed: %s", rec.Body.String())
										}
									}
								}
							})
						}
					})
				}
			})
		}
	}
}
