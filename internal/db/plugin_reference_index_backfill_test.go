package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/referenceindex"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
	migrate "github.com/rubenv/sql-migrate"
)

func TestHistoricalReferenceIndexBackfillMySQL(t *testing.T) {
	db := isolatedTestDB(t)
	if _, err := migrate.Exec(db, "mysql", &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}, migrate.Up); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, r := range []struct{ id, kind string }{{"expert", "expert"}, {"skill", "skill"}, {"team", "expert_team"}, {"connector", "connector"}} {
		if _, err := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,space_id,visibility,manifest_json,plugin_json,manifest_hash,plugin_hash,created_at,updated_at) VALUES (?,?,?,JSON_ARRAY(),'owner','space','private',JSON_OBJECT(),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),NOW(3),NOW(3))`, r.id, r.id, r.kind); err != nil {
			t.Fatal(err)
		}
	}
	pin := "skill-v1"
	expertPin := "expert-v1"
	for _, s := range []struct {
		id, plugin string
		relations  []model.PluginRelation
	}{
		{"skill-v1", "skill", nil},
		{"expert-v1", "expert", []model.PluginRelation{{ID: "skill-edge", SourcePluginID: "expert", SourcePluginType: model.PluginTypeExpert, TargetPluginID: "skill", TargetPluginType: model.PluginTypeSkill, TargetVersionID: &pin, Type: "expert_skill", Status: 1}, {ID: "connector-edge", SourcePluginID: "expert", SourcePluginType: model.PluginTypeExpert, TargetPluginID: "connector", TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1}}},
		{"team-v1", "team", []model.PluginRelation{{ID: "member-edge", SourcePluginID: "team", SourcePluginType: model.PluginTypeExpertTeam, TargetPluginID: "expert", TargetPluginType: model.PluginTypeExpert, TargetVersionID: &expertPin, Type: "expert_team_expert", Status: 1}}},
	} {
		raw, err := model.EncodeSnapshotRelations(s.relations)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO plugin_versions (version_id,plugin_id,version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,created_by,created_at) VALUES (?,?,'1.0.0',JSON_OBJECT(),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),?,'owner',NOW(3))`, s.id, s.plugin, raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []struct{ plugin, id string }{{"skill", "skill-v1"}, {"expert", "expert-v1"}} {
		if _, err := db.Exec(`INSERT INTO plugin_version_releases VALUES (?,?,'1.0.0','owner',NOW(3))`, r.plugin, r.id); err != nil {
			t.Fatal(err)
		}
	}
	before, err := referenceindex.Read(ctx, db, 100)
	if err != nil {
		t.Fatal(err)
	}
	baseline, _ := json.Marshal(before)
	for _, p := range referenceindex.Build(before) {
		if !p.Complete {
			t.Fatalf("incomplete fixture: %+v", p)
		}
		for run := 0; run < 2; run++ {
			n, err := referenceindex.ApplyVersion(ctx, db, 100, p.VersionID, p.SnapshotDigest)
			want := len(p.Edges)
			if run > 0 {
				want = 0
			}
			if err != nil || n != want {
				t.Fatalf("apply %s run%d: n=%d want=%d err=%v", p.VersionID, run, n, want, err)
			}
		}
	}
	after, err := referenceindex.Read(ctx, db, 100)
	if err != nil {
		t.Fatal(err)
	}
	retained, _ := json.Marshal(after)
	if string(retained) != string(baseline) {
		t.Fatal("index backfill changed source/release evidence")
	}
	if _, err := db.Exec(`UPDATE plugin_version_relation_refs SET relation_type='expert_connector',target_plugin_id='connector',target_version_id=NULL WHERE relation_id='skill-edge'`); err != nil {
		t.Fatal(err)
	}
	for _, p := range referenceindex.Build(after) {
		if p.VersionID == "expert-v1" {
			if _, err := referenceindex.ApplyVersion(ctx, db, 100, p.VersionID, p.SnapshotDigest); !errors.Is(err, referenceindex.ErrConflict) {
				t.Fatalf("conflicting index accepted: %v", err)
			}
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plugin_version_relation_refs`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("unexpected facts: %d %v", n, err)
	}
}
