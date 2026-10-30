package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// A legacy edit preserves existing live pins when the caller omits them. Space
// publication freezes those live edges, so its release can contain both retained
// pins and newly added unpinned dependencies. Build that state via normal writes.
func TestMixedRetainedReferencesPreserveLegacyPublication(t *testing.T) {
	for _, tc := range []struct {
		name, targetKind                  string
		unpinnedLeader, publishedUnpinned bool
		deletedHistoricalSkill            bool
	}{
		{name: "expert_draft_skill", targetKind: "skill"},
		{name: "expert_published_skill", targetKind: "skill", publishedUnpinned: true},
		{name: "team_pinned_leader", targetKind: "expert"},
		{name: "team_unpinned_leader", targetKind: "expert", unpinnedLeader: true},
		{name: "team_deleted_historical_skill", targetKind: "expert", unpinnedLeader: true, deletedHistoricalSkill: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			owner := managedOwner()
			repo := pluginrepo.New(database)
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), true, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			modern := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
			releasedID := func(id string) *string {
				t.Helper()
				state, err := repo.GetVersionState(ctx, tenantScope(), id)
				if err != nil || state.PublishedVersionID == nil {
					t.Fatalf("missing released version for fixture: %v", err)
				}
				return state.PublishedVersionID
			}
			skill, err := modern.Create(ctx, owner, managedSkillWrite("1.0.0", "Historical Skill", model.PluginVisibilitySpace))
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, modern, owner, skill.Plugin.ID)
			targetID, pin := skill.Plugin.ID, releasedID(skill.Plugin.ID)
			if tc.targetKind == "expert" {
				req := managedWrite("1.0.0", "historical member", model.PluginVisibilitySpace)
				req.Relations = []pluginsvc.RelationRequest{{Type: "expert_skill", TargetPluginID: targetID, TargetVersionID: pin}}
				expert, err := modern.Create(ctx, owner, req)
				if err != nil {
					t.Fatal(err)
				}
				publishManaged(t, modern, owner, expert.Plugin.ID)
				targetID, pin = expert.Plugin.ID, releasedID(expert.Plugin.ID)
			}
			rootReq := compatibilitySourceWrite(tc.targetKind, "1.0.0")
			rootReq.Visibility = model.PluginVisibilitySpace
			relType := map[string]string{"skill": "expert_skill", "expert": "expert_team_expert"}[tc.targetKind]
			rootReq.Relations = []pluginsvc.RelationRequest{{Type: relType, TargetPluginID: targetID, TargetVersionID: pin}}
			if tc.targetKind == "expert" {
				rootReq.Relations[0].Data = json.RawMessage(`{"member_key":"pinned","is_leader":true,"role":"leader"}`)
			}
			root, err := modern.Create(ctx, owner, rootReq)
			if err != nil {
				t.Fatal(err)
			}
			if tc.targetKind == "expert" {
				if _, err := modern.Update(ctx, owner, targetID, managedWrite("2.0.0", "member without historical Skill", model.PluginVisibilitySpace)); err != nil {
					t.Fatal(err)
				}
				publishManaged(t, modern, owner, targetID)
			}
			legacyRepo := pluginrepo.New(database)
			legacy := pluginsvc.New(legacyRepo).WithVersionManagement(true).WithReferences(false)
			newReq := managedSkillWrite("1.0.0", "Legacy Dependency", model.PluginVisibilitySpace)
			if tc.targetKind == "expert" {
				newReq = managedWrite("1.0.0", "legacy member", model.PluginVisibilitySpace)
				// This is a distinct member, not a rename/update of the pinned Expert.
				newReq.Name = "Legacy Unpinned Member"
				var manifest map[string]any
				if err := json.Unmarshal(newReq.Manifest, &manifest); err != nil {
					t.Fatal(err)
				}
				manifest["plugin_name"] = newReq.Name
				newReq.Manifest, err = json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
			}
			unpinned, err := legacy.Create(ctx, owner, newReq)
			if err != nil {
				t.Fatal(err)
			}
			if tc.publishedUnpinned {
				publishManaged(t, legacy, owner, unpinned.Plugin.ID)
			}
			rootReq.Version = "1.0.1"
			rootReq.Relations[0].ID = root.Relations[0].ID
			rootReq.Relations[0].TargetVersionID = nil
			rootReq.Relations = append(rootReq.Relations, pluginsvc.RelationRequest{Type: relType, TargetPluginID: unpinned.Plugin.ID})
			if tc.targetKind == "expert" {
				rootReq.Relations[0].Data, _ = json.Marshal(map[string]any{"member_key": "pinned", "is_leader": !tc.unpinnedLeader})
				rootReq.Relations[1].Data, _ = json.Marshal(map[string]any{"member_key": "legacy", "is_leader": tc.unpinnedLeader})
			}
			updated, err := legacy.Update(ctx, owner, root.Plugin.ID, rootReq)
			if err != nil {
				t.Fatalf("ordinary legacy edit: %v", err)
			}
			_, live, err := legacyRepo.GetWithRelations(ctx, tenantScope(), root.Plugin.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertMixed := func(rels []model.PluginRelation) {
				t.Helper()
				if len(rels) != 2 {
					t.Fatalf("expected two mixed relations, got %d", len(rels))
				}
				for _, rel := range rels {
					switch rel.TargetPluginID {
					case targetID:
						if rel.TargetVersionID == nil || *rel.TargetVersionID != *pin {
							t.Fatal("retained historical pin changed")
						}
					case unpinned.Plugin.ID:
						if rel.TargetVersionID != nil {
							t.Fatal("legacy edge acquired a pin")
						}
					default:
						t.Fatal("unexpected relation target")
					}
				}
			}
			assertMixed(live)
			before, err := legacyRepo.GetManagedVersion(ctx, tenantScope(), root.Plugin.ID, *updated.Plugin.CurrentVersionID)
			if err != nil {
				t.Fatal(err)
			}
			foreign := owner
			foreign.SpaceID = "foreign-space"
			if _, err := legacy.Publish(ctx, foreign, explicitTestPublication(t, database, root.Plugin.ID)); !errors.Is(err, pluginsvc.ErrNotFound) {
				t.Fatalf("foreign Space published mixed graph: %v", err)
			}
			if tc.deletedHistoricalSkill {
				if err := legacy.Delete(ctx, owner, skill.Plugin.ID); err != nil {
					t.Fatalf("delete unused historical Skill: %v", err)
				}
			}
			_, err = legacy.Publish(ctx, owner, explicitTestPublication(t, database, root.Plugin.ID))
			if tc.deletedHistoricalSkill {
				if !errors.Is(err, pluginsvc.ErrNotFound) {
					t.Fatalf("mixed graph bypassed historical dependency check: %v", err)
				}
				var state string
				var published sql.NullString
				var releases int
				if err := database.QueryRow(`SELECT listing_state,published_version_id FROM plugins WHERE plugin_id=?`, root.Plugin.ID).Scan(&state, &published); err != nil || state != "draft" || published.Valid {
					t.Fatalf("failed publication changed root: state=%s published=%v err=%v", state, published, err)
				}
				if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=?`, root.Plugin.ID).Scan(&releases); err != nil || releases != 0 {
					t.Fatalf("failed publication created release evidence: %d %v", releases, err)
				}
			} else {
				if err != nil {
					t.Fatalf("compatible mixed graph could not publish: %v", err)
				}
				version, err := legacyRepo.GetManagedVersion(ctx, tenantScope(), root.Plugin.ID, *releasedID(root.Plugin.ID))
				if err != nil {
					t.Fatal(err)
				}
				rels, err := model.DecodeSnapshotRelations(version.Relations)
				if err != nil {
					t.Fatal(err)
				}
				assertMixed(rels)
				var state string
				if err := database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, unpinned.Plugin.ID).Scan(&state); err != nil || (!tc.publishedUnpinned && state != "draft") {
					t.Fatalf("publication changed legacy dependency listing: %s %v", state, err)
				}
			}
			after, err := legacyRepo.GetManagedVersion(ctx, tenantScope(), root.Plugin.ID, before.ID)
			if err != nil || string(after.Relations) != string(before.Relations) {
				t.Fatalf("publication mutated saved snapshot: %v", err)
			}
		})
	}
}
