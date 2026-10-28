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
	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
	migrate "github.com/rubenv/sql-migrate"
)

// Legacy approval creates immutable snapshots but no formal release pointer.
// Delisting and saving another draft does not prove the older publication's
// references are gone. An actual new approval must release that protection.
func TestLegacyNullablePublicationProtectsDeletionAcrossDrafts(t *testing.T) {
	for _, references := range []bool{false, true} {
		t.Run(fmt.Sprintf("references=%t", references), func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			owner := managedOwner()
			repo := pluginrepo.New(database)
			legacy := pluginsvc.New(repo)
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), true, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			skill, err := legacy.Create(ctx, owner, managedSkillWrite("1.0.0", "Legacy Referenced Skill", model.PluginVisibilitySpace))
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, legacy, owner, skill.Plugin.ID)
			req := managedWrite("1.0.0", "legacy published Expert", model.PluginVisibilitySpace)
			req.Relations = []pluginsvc.RelationRequest{{Type: "expert_skill", TargetPluginID: skill.Plugin.ID}}
			expert, err := legacy.Create(ctx, owner, req)
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, legacy, owner, expert.Plugin.ID)
			teamReq := compatibilitySourceWrite("expert", "1.0.0")
			teamReq.Visibility = model.PluginVisibilitySpace
			teamReq.Relations = []pluginsvc.RelationRequest{{Type: "expert_team_expert", TargetPluginID: expert.Plugin.ID, Data: json.RawMessage(`{"member_key":"lead","is_leader":true}`)}}
			team, err := legacy.Create(ctx, owner, teamReq)
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, legacy, owner, team.Plugin.ID)
			var pointers int
			if err := database.QueryRow(`SELECT COUNT(*) FROM plugins WHERE published_version_id IS NOT NULL`).Scan(&pointers); err != nil || pointers != 0 {
				t.Fatalf("fixture accidentally has formal publication pointers: %d %v", pointers, err)
			}
			if _, err := legacy.Delist(ctx, managedReviewer(), pluginsvc.DelistParams{PluginID: expert.Plugin.ID}); err != nil {
				t.Fatal(err)
			}
			var approvedHash string
			if err := database.QueryRow(`SELECT plugin_hash FROM plugins WHERE plugin_id=?`, expert.Plugin.ID).Scan(&approvedHash); err != nil {
				t.Fatal(err)
			}
			draft := req
			draft.Relations = []pluginsvc.RelationRequest{}
			if _, err := legacy.Update(ctx, owner, expert.Plugin.ID, draft); err != nil {
				t.Fatal(err)
			}
			var draftHash string
			if err := database.QueryRow(`SELECT plugin_hash FROM plugins WHERE plugin_id=?`, expert.Plugin.ID).Scan(&draftHash); err != nil || draftHash != approvedHash {
				t.Fatalf("fixture must exercise a relation-only draft with unchanged content hash: %v", err)
			}
			deletion := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(references)
			assertNullableLegacyDeleteRefused(t, database, deletion, skill.Plugin.ID)
			// The next true approval makes the unpinned member use the new graph;
			// old immutable snapshots must not become a permanent deletion ban.
			// The legacy label set includes the delisted row's saved 1.0.0;
			// submit an explicitly newer release label through the normal review API.
			noRelations := []pluginsvc.RelationRequest{}
			if _, err := legacy.SubmitReview(ctx, owner, pluginsvc.ReviewSubmitParams{PluginID: expert.Plugin.ID, Version: "2.0.0", Relations: &noRelations}); err != nil {
				t.Fatal(err)
			}
			if _, err := legacy.Delist(ctx, managedReviewer(), pluginsvc.DelistParams{PluginID: expert.Plugin.ID}); err != nil {
				t.Fatal(err)
			}
			if err := deletion.Delete(ctx, owner, skill.Plugin.ID); err != nil {
				t.Fatalf("superseded legacy history permanently blocked deletion: %v", err)
			}
			assertNullableLegacySoftDeleted(t, database, skill.Plugin.ID)
		})
	}
}

// Reproduce the nullable representation through the real foundation migration:
// an old published Team selects an unpinned member whose saved snapshot can
// still contain a Skill after the mutable live edge has disappeared. The member
// is an unpublished draft, so only the Team can make it an incoming candidate.
func TestLegacyUnpinnedTeamResolvesNullableMemberSnapshots(t *testing.T) {
	for _, references := range []bool{false, true} {
		t.Run(fmt.Sprintf("references=%t", references), func(t *testing.T) {
			database := isolatedTestDB(t)
			if _, err := migrate.Exec(database, "mysql", migrationSourceThrough(t, "20261008-00-plugin-scenes.sql"), migrate.Up); err != nil {
				t.Fatal(err)
			}
			for _, p := range []seedPlugin{
				{id: "legacy-skill", visibility: "space", listingState: "published", currentVersionID: "legacy-s1"},
				{id: "legacy-expert", typ: "expert", visibility: "space", listingState: "draft", currentVersionID: "legacy-e1"},
				{id: "legacy-team", typ: "expert_team", visibility: "space", listingState: "published", currentVersionID: "legacy-t1"},
				{id: "unrelated-draft", typ: "expert", visibility: "private", listingState: "draft", currentVersionID: "unrelated-v1"},
			} {
				seed(t, database, p)
				seedVersionRow(t, database, p.currentVersionID, p.id, "1")
			}
			for _, row := range []struct{ version, source, target, kind string }{
				{"legacy-e1", "legacy-expert", "legacy-skill", "expert_skill"},
				{"legacy-t1", "legacy-team", "legacy-expert", "expert_team_expert"},
				{"unrelated-v1", "unrelated-draft", "legacy-skill", "expert_skill"},
			} {
				raw, err := model.EncodeSnapshotRelations([]model.PluginRelation{{ID: row.source + "-edge", SourcePluginID: row.source, TargetPluginID: row.target, Type: row.kind, Status: 1}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`UPDATE plugin_versions SET relations_json=? WHERE version_id=?`, string(raw), row.version); err != nil {
					t.Fatal(err)
				}
			}
			seedRelation(t, database, "team-live", "legacy-team", "legacy-expert", "expert_team_expert")
			if _, err := migrate.Exec(database, "mysql", &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}, migrate.Up); err != nil {
				t.Fatal(err)
			}
			deletion := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(references)
			assertNullableLegacyDeleteRefused(t, database, deletion, "legacy-skill")
			// Deleted publication sources no longer protect their old graph. The
			// unrelated draft snapshot alone is not publication evidence.
			if _, err := database.Exec(`UPDATE plugins SET deleted_at=NOW(3) WHERE plugin_id='legacy-team'`); err != nil {
				t.Fatal(err)
			}
			if err := deletion.Delete(context.Background(), managedOwner(), "legacy-skill"); err != nil {
				t.Fatalf("unpublished history blocked deletion without surviving Team: %v", err)
			}
			assertNullableLegacySoftDeleted(t, database, "legacy-skill")
		})
	}
}

func assertNullableLegacyDeleteRefused(t *testing.T, database *sql.DB, svc *pluginsvc.Service, target string) {
	t.Helper()
	foreign := managedOwner()
	foreign.SpaceID = "foreign-space"
	if err := svc.Delete(context.Background(), foreign, target); !errors.Is(err, pluginsvc.ErrNotFound) {
		t.Fatalf("foreign Space deletion: %v", err)
	}
	var before, after string
	const evidence = `SELECT JSON_ARRAYAGG(JSON_OBJECT('plugin',plugin_id,'version',version_id,'relations',relations_json)) FROM (SELECT plugin_id,version_id,relations_json FROM plugin_versions ORDER BY plugin_id,version_id) versions`
	if err := database.QueryRow(evidence).Scan(&before); err != nil {
		t.Fatal(err)
	}
	err := svc.Delete(context.Background(), managedOwner(), target)
	if !errors.Is(err, pluginsvc.ErrRelationInUse) && !errors.Is(err, pluginrepo.ErrReferenceIndexIncomplete) {
		t.Fatalf("nullable legacy publication failed open: %v", err)
	}
	if err := database.QueryRow(evidence).Scan(&after); err != nil || before != after {
		t.Fatalf("refused deletion rewrote immutable evidence: %v", err)
	}
	var deleted sql.NullTime
	if err := database.QueryRow(`SELECT deleted_at FROM plugins WHERE plugin_id=?`, target).Scan(&deleted); err != nil || deleted.Valid {
		t.Fatalf("refused deletion changed target: %v", err)
	}
	assertAudit(t, database, target, "delete", 0)
}

func assertNullableLegacySoftDeleted(t *testing.T, database *sql.DB, target string) {
	t.Helper()
	var deleted sql.NullTime
	if err := database.QueryRow(`SELECT deleted_at FROM plugins WHERE plugin_id=?`, target).Scan(&deleted); err != nil || !deleted.Valid {
		t.Fatalf("unreferenced target was not soft-deleted: %v", err)
	}
	var snapshots int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, target).Scan(&snapshots); err != nil || snapshots == 0 {
		t.Fatalf("soft deletion lost target history: count=%d err=%v", snapshots, err)
	}
}
