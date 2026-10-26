package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// pinnedTestEdge builds one snapshot edge. Connector edges carry no pin by
// contract; every Skill/Expert edge in a released snapshot must carry one.
func pinnedTestEdge(source, target, kind, pin string) model.PluginRelation {
	r := model.PluginRelation{ID: source + "-" + target, SourcePluginID: source, TargetPluginID: target, Type: kind, Status: 1, Data: json.RawMessage(`{}`)}
	if pin != "" {
		v := pin
		r.TargetVersionID = &v
	}
	if kind == "expert_team_expert" {
		r.Data = json.RawMessage(`{"member_key":"lead","is_leader":true}`)
	}
	return r
}

// releasePinnedVersion stores one immutable snapshot plus its formal release and
// points the source's published head at it.
func releasePinnedVersion(t *testing.T, database *sql.DB, id, versionID, label string, rels []model.PluginRelation) {
	t.Helper()
	seedVersionRow(t, database, versionID, id, label)
	raw, err := model.EncodeSnapshotRelations(rels)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE plugin_versions SET relations_json=?,snapshot_schema_version=1,artifacts_json=JSON_ARRAY() WHERE plugin_id=? AND version_id=?`, string(raw), id, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,?,'user-1',NOW(3))`, id, versionID, label); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, versionID, id); err != nil {
		t.Fatal(err)
	}
}

func pinnedGraphFixture(t *testing.T, database *sql.DB, plugins ...seedPlugin) {
	t.Helper()
	for _, p := range plugins {
		seed(t, database, p)
	}
}

// The P model freezes Expert→Skill at publish time: releasing a newer Skill does
// not move an already-published Expert's pin, and the graph must load v1 bytes.
func TestPinnedGraphExpertKeepsSkillPinAfterNewerReleaseMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	pinnedGraphFixture(t, database,
		seedPlugin{id: "expert", typ: "expert", visibility: "space", listingState: "published"},
		seedPlugin{id: "skill", typ: "skill", visibility: "space", listingState: "published"},
	)
	releasePinnedVersion(t, database, "skill", "s1", "1.0.0", nil)
	releasePinnedVersion(t, database, "expert", "e1", "1.0.0", []model.PluginRelation{pinnedTestEdge("expert", "skill", "expert_skill", "s1")})
	// A newer formal release becomes the Skill's current head. The Expert pin
	// must neither follow it nor fall back to an older release.
	releasePinnedVersion(t, database, "skill", "s2", "2.0.0", nil)

	g, err := repo.ResolvePinnedGraph(ctx, tenantScope(), "expert")
	if err != nil {
		t.Fatal(err)
	}
	if g.Selected["expert"] != "e1" || g.Selected["skill"] != "s1" {
		t.Fatalf("pinned selection drifted: %v", g.Selected)
	}
	if g.Versions[pluginrepo.InstallVersionKey{PluginID: "skill", VersionID: "s1"}] == nil {
		t.Fatal("pinned skill v1 bytes were not loaded")
	}
	if g.Versions[pluginrepo.InstallVersionKey{PluginID: "skill", VersionID: "s2"}] != nil {
		t.Fatal("newer target version leaked into the pinned graph")
	}
	if len(g.Relations["expert"]) != 1 || g.Relations["expert"][0].TargetVersionID == nil || *g.Relations["expert"][0].TargetVersionID != "s1" {
		t.Fatalf("expert edge lost its pin: %+v", g.Relations["expert"])
	}
	// Cross-Space admin must not widen the graph's audience.
	if _, err = repo.ResolvePinnedGraph(ctx, pluginrepo.Scope{CallerUID: "user-1", SpaceID: "other", Admin: true}, "expert"); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-space admin bypass: %v", err)
	}
}

// Team pins are authoritative for member Expert versions; a member's later
// release does not move the Team, and the pinned member's own Skill pin is used
// instead of re-resolving the member's current head.
func TestPinnedGraphTeamKeepsMemberExpertPinAfterNewerReleaseMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	pinnedGraphFixture(t, database,
		seedPlugin{id: "team", typ: "expert_team", visibility: "space", listingState: "published"},
		seedPlugin{id: "expert", typ: "expert", visibility: "space", listingState: "published"},
		seedPlugin{id: "skill", typ: "skill", visibility: "space", listingState: "published"},
	)
	releasePinnedVersion(t, database, "skill", "s1", "1.0.0", nil)
	releasePinnedVersion(t, database, "expert", "e1", "1.0.0", []model.PluginRelation{pinnedTestEdge("expert", "skill", "expert_skill", "s1")})
	releasePinnedVersion(t, database, "team", "t1", "1.0.0", []model.PluginRelation{pinnedTestEdge("team", "expert", "expert_team_expert", "e1")})
	releasePinnedVersion(t, database, "expert", "e2", "2.0.0", []model.PluginRelation{pinnedTestEdge("expert", "skill", "expert_skill", "s2")})
	releasePinnedVersion(t, database, "skill", "s2", "2.0.0", nil)

	g, err := repo.ResolvePinnedGraph(ctx, tenantScope(), "team")
	if err != nil {
		t.Fatal(err)
	}
	if g.Selected["team"] != "t1" || g.Selected["expert"] != "e1" || g.Selected["skill"] != "s1" {
		t.Fatalf("team pin drifted: %v", g.Selected)
	}
	if g.Versions[pluginrepo.InstallVersionKey{PluginID: "expert", VersionID: "e2"}] != nil || g.Versions[pluginrepo.InstallVersionKey{PluginID: "skill", VersionID: "s2"}] != nil {
		t.Fatal("newer member/dependency releases leaked into the pinned graph")
	}
	for _, edge := range g.Relations["team"] {
		if edge.TargetVersionID == nil || *edge.TargetVersionID != "e1" {
			t.Fatalf("team member pin lost: %+v", edge)
		}
	}
	for _, edge := range g.Relations["expert"] {
		if edge.TargetVersionID == nil || *edge.TargetVersionID != "s1" {
			t.Fatalf("pinned member's skill pin lost: %+v", edge)
		}
	}
}

// A released snapshot without a Skill/Expert pin is invalid under the P model.
// The resolver must fail closed instead of reading the target's current head.
func TestPinnedGraphMissingPinFailsClosedMySQL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edges []model.PluginRelation
		root  string
	}{
		{name: "expert skill", edges: []model.PluginRelation{pinnedTestEdge("expert", "skill", "expert_skill", "")}, root: "expert"},
		{name: "team member behind expert", edges: []model.PluginRelation{pinnedTestEdge("expert", "skill", "expert_skill", "")}, root: "team"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			repo := pluginrepo.New(database)
			pinnedGraphFixture(t, database,
				seedPlugin{id: "team", typ: "expert_team", visibility: "space", listingState: "published"},
				seedPlugin{id: "expert", typ: "expert", visibility: "space", listingState: "published"},
				seedPlugin{id: "skill", typ: "skill", visibility: "space", listingState: "published"},
			)
			releasePinnedVersion(t, database, "skill", "s1", "1.0.0", nil)
			releasePinnedVersion(t, database, "expert", "e1", "1.0.0", tc.edges)
			releasePinnedVersion(t, database, "team", "t1", "1.0.0", []model.PluginRelation{pinnedTestEdge("team", "expert", "expert_team_expert", "e1")})
			if _, err := repo.ResolvePinnedGraph(ctx, tenantScope(), tc.root); !errors.Is(err, pluginrepo.ErrInvalidRelation) {
				t.Fatalf("missing pin was not rejected: %v", err)
			}
		})
	}
	t.Run("connector pin", func(t *testing.T) {
		database := reviewDB(t)
		ctx := context.Background()
		repo := pluginrepo.New(database)
		pinnedGraphFixture(t, database,
			seedPlugin{id: "expert", typ: "expert", visibility: "space", listingState: "published"},
			seedPlugin{id: "connector", typ: "connector", visibility: "space", listingState: "published"},
		)
		seedConnectorEffectiveForTest(t, database, "connector")
		// Connectors must stay NULL: a stored pin is a corrupt snapshot.
		releasePinnedVersion(t, database, "expert", "e1", "1.0.0", []model.PluginRelation{pinnedTestEdge("expert", "connector", "expert_connector", "c1")})
		if _, err := repo.ResolvePinnedGraph(ctx, tenantScope(), "expert"); !errors.Is(err, pluginrepo.ErrInvalidRelation) {
			t.Fatalf("connector pin was not rejected: %v", err)
		}
	})
}

// Connector dependencies keep the existing ID + effective semantics: no version
// pin, and the effective payload (not the editable row) is distributed.
func TestPinnedGraphConnectorUsesEffectivePayloadMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	pinnedGraphFixture(t, database,
		seedPlugin{id: "expert", typ: "expert", visibility: "space", listingState: "published"},
		seedPlugin{id: "connector", typ: "connector", visibility: "space", listingState: "published"},
	)
	if _, err := database.Exec(`UPDATE plugins SET plugin_name='Editable Draft Name' WHERE plugin_id='connector'`); err != nil {
		t.Fatal(err)
	}
	seedConnectorEffectiveForTest(t, database, "connector")
	releasePinnedVersion(t, database, "expert", "e1", "1.0.0", []model.PluginRelation{pinnedTestEdge("expert", "connector", "expert_connector", "")})

	g, err := repo.ResolvePinnedGraph(ctx, tenantScope(), "expert")
	if err != nil {
		t.Fatal(err)
	}
	connector := g.Plugins["connector"]
	if connector == nil || connector.EffectiveRevision == nil || *connector.EffectiveRevision != 1 || connector.EffectiveDigest == nil {
		t.Fatalf("connector effective not overlayed: %+v", connector)
	}
	if connector.Name != "Effective Connector" {
		t.Fatalf("editable connector row leaked instead of effective payload: %q", connector.Name)
	}
	if g.Selected["connector"] != "" {
		t.Fatalf("connector must not be version-selected: %v", g.Selected)
	}
	if len(g.Relations["expert"]) != 1 || g.Relations["expert"][0].TargetVersionID != nil {
		t.Fatalf("connector edge must stay unpinned: %+v", g.Relations["expert"])
	}
}

// A pin that is deleted, delisted, unreleased, or belongs to another plugin is
// unavailable; the resolver must not fall back to the target's current head.
func TestPinnedGraphUnavailablePinFailsClosedMySQL(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pin    string
		mutate func(*testing.T, *sql.DB)
	}{
		{name: "deleted target", pin: "s1", mutate: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE plugins SET deleted_at=NOW(3) WHERE plugin_id='skill'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "delisted target", pin: "s1", mutate: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "disabled target", pin: "s1", mutate: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE plugins SET status=0 WHERE plugin_id='skill'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unreleased pin", pin: "s1", mutate: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`DELETE FROM plugin_version_releases WHERE plugin_id='skill' AND version_id='s1'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "foreign version", pin: "expert-v1", mutate: func(t *testing.T, db *sql.DB) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			repo := pluginrepo.New(database)
			pinnedGraphFixture(t, database,
				seedPlugin{id: "expert", typ: "expert", visibility: "space", listingState: "published"},
				seedPlugin{id: "skill", typ: "skill", visibility: "space", listingState: "published"},
			)
			releasePinnedVersion(t, database, "skill", "s1", "1.0.0", nil)
			releasePinnedVersion(t, database, "skill", "s2", "2.0.0", nil)
			releasePinnedVersion(t, database, "expert", "expert-v1", "1.0.0", nil)
			releasePinnedVersion(t, database, "expert", "e1", "2.0.0", []model.PluginRelation{pinnedTestEdge("expert", "skill", "expert_skill", tc.pin)})
			tc.mutate(t, database)
			if _, err := repo.ResolvePinnedGraph(ctx, tenantScope(), "expert"); !errors.Is(err, pluginrepo.ErrDependencyUnavailable) {
				t.Fatalf("unavailable pin %q was not rejected: %v", tc.pin, err)
			}
		})
	}
}

func seedConnectorEffectiveForTest(t *testing.T, database *sql.DB, id string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO plugin_connector_effective
		(plugin_id,effective_revision,effective_digest,plugin_name,tags_json,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version,visibility,updated_by,updated_at)
		VALUES (?,1,CONCAT('sha256:',SHA2(CONCAT('fixture',?),256)),'Effective Connector',JSON_ARRAY(),0,JSON_OBJECT('plugin_name','Effective Connector'),JSON_OBJECT(),JSON_ARRAY(),REPEAT('a',71),REPEAT('b',71),'1.0.0',(SELECT visibility FROM plugins WHERE plugin_id=?),'fixture',NOW(3))`, id, id, id); err != nil {
		t.Fatal(err)
	}
}
