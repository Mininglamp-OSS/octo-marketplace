package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func TestLatestGraphFollowsBothLevelsAndFailsClosed(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	for _, p := range []seedPlugin{{id: "team", typ: "expert_team", visibility: "space", listingState: "published"}, {id: "expert", typ: "expert", visibility: "space", listingState: "published"}, {id: "skill", typ: "skill", visibility: "space", listingState: "published"}} {
		seed(t, database, p)
	}
	edge := func(source, target, kind, pin string) model.PluginRelation {
		r := model.PluginRelation{ID: source + target, SourcePluginID: source, TargetPluginID: target, Type: kind, Status: 1, Data: json.RawMessage(`{}`)}
		if pin != "" {
			r.TargetVersionID = &pin
		}
		if kind == "expert_team_expert" {
			r.Data = json.RawMessage(`{"member_key":"lead","is_leader":true}`)
		}
		return r
	}
	release := func(id, vid, label string, rels []model.PluginRelation) {
		t.Helper()
		raw, err := model.EncodeSnapshotRelations(rels)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(`INSERT INTO plugin_versions(version_id,plugin_id,version,declared_version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,created_by,created_at,snapshot_schema_version,artifacts_json) VALUES (?,?,?, ?,JSON_OBJECT(),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),?,'user-1',NOW(3),1,JSON_ARRAY())`, vid, id, label, label, string(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(`INSERT INTO plugin_version_releases VALUES (?,?,?,'user-1',NOW(3))`, id, vid, label); err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, vid, id); err != nil {
			t.Fatal(err)
		}
	}
	release("skill", "s1", "1.0.0", nil)
	release("expert", "e1", "1.0.0", []model.PluginRelation{edge("expert", "skill", "expert_skill", "s1")})
	release("team", "t1", "1.0.0", []model.PluginRelation{edge("team", "expert", "expert_team_expert", "e1")})
	release("skill", "s2", "2.0.0", nil)
	release("expert", "e2", "2.0.0", []model.PluginRelation{edge("expert", "skill", "expert_skill", "")})
	g, err := repo.ResolveLatestGraph(ctx, tenantScope(), "team")
	if err != nil {
		t.Fatal(err)
	}
	if g.Selected["expert"] != "e2" || g.Selected["skill"] != "s2" || *g.Relations["team"][0].TargetVersionID != "e2" {
		t.Fatalf("latest selection=%v", g.Selected)
	}
	original, err := model.DecodeSnapshotRelations(g.Versions[pluginrepo.InstallVersionKey{PluginID: "team", VersionID: "t1"}].Relations)
	if err != nil || *original[0].TargetVersionID != "e1" {
		t.Fatal("immutable audit snapshot was rewritten")
	}
	if _, err = repo.ResolveLatestGraph(ctx, pluginrepo.Scope{CallerUID: "user-1", SpaceID: "other", Admin: true}, "team"); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-space admin bypass: %v", err)
	}
	if _, err = database.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ResolveLatestGraph(ctx, tenantScope(), "team"); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("delisted latest fell back: %v", err)
	}
	// The already resolved snapshot stays frozen even after governance changes.
	if g.Selected["skill"] != "s2" || g.Plugins["skill"].ListingState != model.PluginListingStatePublished {
		t.Fatal("snapshot mutated")
	}
	if _, err = database.Exec(`UPDATE plugins SET listing_state='published',published_version_id=NULL WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ResolveLatestGraph(ctx, tenantScope(), "team"); !errors.Is(err, pluginrepo.ErrVersionBaselineUnavailable) {
		t.Fatalf("missing head fell back: %v", err)
	}
}
