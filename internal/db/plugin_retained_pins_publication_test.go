package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// Disabling independent-reference writes must not turn retained version pins
// into live-head lookups. A Team can still select Expert v1 after that Expert
// publishes v2 without its Skill; publication must validate v1's dependency tree.
func TestRetainedPinnedTeamPublicationChecksHistoricalDependencies(t *testing.T) {
	for _, references := range []bool{false, true} {
		for _, deletedSkill := range []bool{false, true} {
			t.Run(fmt.Sprintf("references=%t/deleted_skill=%t", references, deletedSkill), func(t *testing.T) {
				database := reviewDB(t)
				ctx := context.Background()
				owner := managedOwner()
				svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true)
				skill, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "Historical Skill", model.PluginVisibilityPrivate))
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, owner, skill.Plugin.ID)
				expertReq := managedWrite("1.0.0", "expert with historical Skill", model.PluginVisibilityPrivate)
				expertReq.Relations = []pluginsvc.RelationRequest{{Type: "expert_skill", TargetPluginID: skill.Plugin.ID, TargetVersionID: skill.Plugin.CurrentVersionID}}
				expert, err := svc.Create(ctx, owner, expertReq)
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, owner, expert.Plugin.ID)
				teamReq := compatibilitySourceWrite("expert", "1.0.0")
				teamReq.Relations = []pluginsvc.RelationRequest{{
					Type: "expert_team_expert", TargetPluginID: expert.Plugin.ID, TargetVersionID: expert.Plugin.CurrentVersionID,
					Data: json.RawMessage(`{"member_key":"leader","is_leader":true,"role":"leader"}`),
				}}
				team, err := svc.Create(ctx, owner, teamReq)
				if err != nil {
					t.Fatal(err)
				}
				var originalSnapshot string
				if err := database.QueryRow(`SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`, team.Plugin.ID, *team.Plugin.CurrentVersionID).Scan(&originalSnapshot); err != nil {
					t.Fatal(err)
				}
				if _, err := svc.Update(ctx, owner, expert.Plugin.ID, managedWrite("2.0.0", "expert without Skill", model.PluginVisibilityPrivate)); err != nil {
					t.Fatal(err)
				}
				publishManaged(t, svc, owner, expert.Plugin.ID)
				compatibility := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true)
				if deletedSkill {
					// Only the unpublished Team pins the old Expert. The Skill is
					// historical-only and may be soft-deleted without rewriting v1.
					if err := compatibility.Delete(ctx, owner, skill.Plugin.ID); err != nil {
						t.Fatalf("delete historical-only Skill: %v", err)
					}
					var deleted sql.NullTime
					if err := database.QueryRow(`SELECT deleted_at FROM plugins WHERE plugin_id=?`, skill.Plugin.ID).Scan(&deleted); err != nil || !deleted.Valid {
						t.Fatalf("Skill was not soft-deleted: %v", err)
					}
				}
				publisher := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(references)
				_, err = publisher.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: team.Plugin.ID})
				if deletedSkill {
					if !errors.Is(err, pluginsvc.ErrNotFound) {
						t.Fatalf("retained pin published through deleted transitive Skill: %v", err)
					}
				} else if err != nil {
					t.Fatalf("healthy retained pin could not publish: %v", err)
				}
				var state string
				var published sql.NullString
				if err := database.QueryRow(`SELECT listing_state,published_version_id FROM plugins WHERE plugin_id=?`, team.Plugin.ID).Scan(&state, &published); err != nil {
					t.Fatal(err)
				}
				wantReleases := 1
				if deletedSkill {
					wantReleases = 0
					if state != "draft" || published.Valid {
						t.Fatalf("rejected publication changed Team: state=%s published=%v", state, published)
					}
				} else if state != "published" || !published.Valid || published.String != *team.Plugin.CurrentVersionID {
					t.Fatalf("healthy Team release missing: state=%s published=%v", state, published)
				}
				var releases int
				if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=?`, team.Plugin.ID).Scan(&releases); err != nil || releases != wantReleases {
					t.Fatalf("Team release evidence=%d want=%d err=%v", releases, wantReleases, err)
				}
				assertAudit(t, database, team.Plugin.ID, "publish", wantReleases)
				var retainedSnapshot string
				if err := database.QueryRow(`SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`, team.Plugin.ID, *team.Plugin.CurrentVersionID).Scan(&retainedSnapshot); err != nil || retainedSnapshot != originalSnapshot {
					t.Fatalf("retained Team snapshot changed: %v", err)
				}
				if !deletedSkill {
					if err := compatibility.Delete(ctx, owner, skill.Plugin.ID); !errors.Is(err, pluginsvc.ErrRelationInUse) {
						t.Fatalf("published Team did not protect its historical Skill pin: %v", err)
					}
				}
			})
		}
	}
}
