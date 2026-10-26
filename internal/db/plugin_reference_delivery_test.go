package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func TestReferenceDeliveryOwnerPrivateSnapshotAndLifecycle(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	seed(t, database, seedPlugin{id: "skill", typ: "skill", visibility: "private", listingState: "published"})
	seedVersionRow(t, database, "skill-v1", "skill", "1")
	if _, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES ('skill','skill-v1','1.0.0','user-1',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	setPublishedFixture(t, database, "skill", "skill-v1")
	space := "space-a"
	version := "skill-v1"
	label := "1.0.0"
	p := model.Plugin{ID: "expert", Name: "Expert", Type: model.PluginTypeExpert, OwnerUID: "user-1", SpaceID: &space, Visibility: "private", ListingState: "draft", CreatedByType: "human", Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{"plugin_name":"Expert"}`), Package: json.RawMessage(`{"attachments":[]}`), ManifestHash: "m", PluginHash: "p", CurrentVersion: &label, Status: 1}
	edge := model.PluginRelation{ID: "ref", SourcePluginID: p.ID, TargetPluginID: "skill", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", TargetVersionID: &version, Status: 1}
	sync, err := repo.Create(context.Background(), tenantScope(), pluginrepo.Mutation{Plugin: p, Relations: []model.PluginRelation{edge}, SnapshotVersion: true, OperatorID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	var pin string
	if err = database.QueryRow(`SELECT target_version_id FROM plugin_version_relation_refs WHERE source_version_id=?`, sync.NewVersionID).Scan(&pin); err != nil || pin != version {
		t.Fatalf("index pin=%q err=%v", pin, err)
	}
	_, rels, err := repo.GetWithRelations(context.Background(), tenantScope(), p.ID)
	if err != nil || len(rels) != 1 || rels[0].TargetVersionID == nil || *rels[0].TargetVersionID != version {
		t.Fatalf("read pin=%+v err=%v", rels, err)
	}
	// Delisting after draft save is rechecked during publication under locks.
	if _, err = database.Exec(`UPDATE plugins SET listing_state='draft' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	_, err = repo.PublishManaged(context.Background(), tenantScope(), pluginrepo.PublishParams{PluginID: p.ID, ExpectedVersionID: sync.NewVersionID, OperatorID: "user-1"})
	if !errors.Is(err, pluginrepo.ErrInvalidRelation) {
		t.Fatalf("publish stale dependency=%v", err)
	}
	if err = repo.Delete(context.Background(), tenantScope(), "skill", "user-1", "", "", nil); !errors.Is(err, pluginrepo.ErrRelationInUse) {
		t.Fatalf("delete live referenced=%v", err)
	}
}
func TestReferenceDeliveryRejectsUnauthorizedAndWrongRelease(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	seed(t, database, seedPlugin{id: "private", typ: "skill", visibility: "private", listingState: "published"})
	seedVersionRow(t, database, "private-v1", "private", "1")
	_, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES ('private','private-v1','1.0.0','user-1',NOW(3))`)
	if err != nil {
		t.Fatal(err)
	}
	space := "space-a"
	version := "private-v1"
	for i, tt := range []struct {
		vis                   model.PluginVisibility
		owner, space, version string
		want                  error
	}{
		{"space", "user-1", space, version, pluginrepo.ErrNotFound},
		{"private", "other", space, version, pluginrepo.ErrNotFound},
		{"private", "user-1", "other-space", version, pluginrepo.ErrNotFound},
		{"private", "user-1", space, "unreleased", pluginrepo.ErrInvalidRelation},
	} {
		p := model.Plugin{ID: time.Now().Format("150405.000000000"), Type: model.PluginTypeExpert, OwnerUID: tt.owner, SpaceID: &tt.space, Visibility: tt.vis, Status: 1}
		edge := model.PluginRelation{TargetPluginID: "private", Type: "expert_skill", TargetVersionID: &tt.version, Status: 1}
		_, err := repo.Create(context.Background(), pluginrepo.Scope{CallerUID: tt.owner, SpaceID: tt.space}, pluginrepo.Mutation{Plugin: p, Relations: []model.PluginRelation{edge}})
		if !errors.Is(err, tt.want) {
			t.Fatalf("case%d got=%v want=%v", i, err, tt.want)
		}
	}
}

func TestReferenceDeliveryHistoricalDependentsAndCandidates(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	for _, p := range []seedPlugin{{id: "skill", typ: "skill", visibility: "private", listingState: "published"}, {id: "expert", typ: "expert", visibility: "private", listingState: "published"}, {id: "team", typ: "expert_team", visibility: "private", listingState: "published"}, {id: "foreign", typ: "expert", visibility: "private", owner: "other", listingState: "published"}} {
		seed(t, database, p)
		seedVersionRow(t, database, p.id+"-v1", p.id, "1")
		if _, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,'1.0.0','user-1',NOW(3))`, p.id, p.id+"-v1"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, p.id+"-v1", p.id); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range [][6]string{{"expert", "expert-v1", "es", "expert_skill", "skill", "skill-v1"}, {"team", "team-v1", "te", "expert_team_expert", "expert", "expert-v1"}, {"foreign", "foreign-v1", "fs", "expert_skill", "skill", "skill-v1"}} {
		if _, err := database.Exec(`INSERT INTO plugin_version_relation_refs(source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id) VALUES (?,?,?,?,?,?)`, row[0], row[1], row[2], row[3], row[4], row[5]); err != nil {
			t.Fatal(err)
		}
	}
	markReferenceIndexFixtures(t, database)
	items, total, err := repo.ReferenceDependents(context.Background(), tenantScope(), "skill", "skill-v1", 20, 0)
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("dependents total=%d items=%+v err=%v", total, items, err)
	}
	if items[0].PluginID != "expert" || items[0].Depth != 1 || items[1].PluginID != "team" || items[1].Depth != 2 {
		t.Fatalf("wrong historical dependency path %+v", items)
	}
	candidates, count, err := repo.ReferenceCandidates(context.Background(), tenantScope(), "expert", "expert_skill", 20, 0)
	if err != nil || count != 1 || len(candidates) != 1 || !candidates[0].IsSelectable || candidates[0].VersionID == nil {
		t.Fatalf("candidates %+v count=%d err=%v", candidates, count, err)
	}
	seedRelation(t, database, "live", "expert", "skill", "expert_skill")
	candidates, _, err = repo.ReferenceCandidates(context.Background(), tenantScope(), "expert", "expert_skill", 20, 0)
	if err != nil || len(candidates) != 1 || !candidates[0].AlreadyBound || candidates[0].IsSelectable {
		t.Fatalf("bound candidates %+v err=%v", candidates, err)
	}
}

func TestReferenceDeliveryRedirectClearsVersionAndKeepsIdentity(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	seed(t, database, seedPlugin{id: "skill", typ: "skill", visibility: "private", listingState: "published"})
	seed(t, database, seedPlugin{id: "connector", typ: "connector", visibility: "private", listingState: "published"})
	seedVersionRow(t, database, "sv", "skill", "1")
	if _, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES ('skill','sv','1.0.0','user-1',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	setPublishedFixture(t, database, "skill", "sv")
	effective, err := repo.Get(context.Background(), tenantScope(), "connector")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Update(context.Background(), pluginrepo.Scope{Admin: true}, pluginrepo.Mutation{Plugin: *effective, OperatorID: "platform"}); err != nil {
		t.Fatal(err)
	}
	space, label, version := "space-a", "1.0.0", "sv"
	p := model.Plugin{ID: "expert", Name: "Expert", Type: model.PluginTypeExpert, OwnerUID: "user-1", SpaceID: &space, Visibility: "private", ListingState: "draft", CreatedByType: "human", Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{"plugin_name":"Expert"}`), Package: json.RawMessage(`{"attachments":[]}`), ManifestHash: "m", PluginHash: "p", CurrentVersion: &label, Status: 1}
	edge := model.PluginRelation{ID: "stable", SourcePluginID: p.ID, TargetPluginID: "skill", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", TargetVersionID: &version, Status: 1}
	_, err = repo.Create(context.Background(), tenantScope(), pluginrepo.Mutation{Plugin: p, Relations: []model.PluginRelation{edge}, SnapshotVersion: true, OperatorID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	edge.TargetPluginID = "connector"
	edge.TargetPluginType = model.PluginTypeConnector
	edge.Type = "expert_connector"
	edge.TargetVersionID = nil
	_, err = repo.Update(context.Background(), tenantScope(), pluginrepo.Mutation{Plugin: p, Relations: []model.PluginRelation{edge}, SnapshotVersion: true, OperatorID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, rels, err := repo.GetWithRelations(context.Background(), tenantScope(), p.ID)
	if err != nil || len(rels) != 1 || rels[0].ID != "stable" || rels[0].TargetVersionID != nil || rels[0].Type != "expert_connector" {
		t.Fatalf("live redirect %+v err=%v", rels, err)
	}
	edge.ID = "" // A fetch-free caller resubmits the same target; reuse identity.
	sync, err := repo.Update(context.Background(), tenantScope(), pluginrepo.Mutation{Plugin: p, Relations: []model.PluginRelation{edge}, SnapshotVersion: true, OperatorID: "user-1"})
	if err != nil || len(sync.Created) != 0 || sync.Relations[0].ID != "stable" {
		t.Fatalf("identity sync %+v err=%v", sync, err)
	}
}

func TestReferenceDeliveryApprovalRevalidatesFrozenPins(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	seed(t, database, seedPlugin{id: "skill", typ: "skill", visibility: "system", listingState: "published"})
	seedVersionRow(t, database, "skill-v1", "skill", "1")
	seedVersionRow(t, database, "skill-v2", "skill", "2")
	if _, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES ('skill','skill-v1','1.0.0','user-1',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	setPublishedFixture(t, database, "skill", "skill-v1")
	seed(t, database, seedPlugin{id: "expert", typ: "expert", visibility: "space", listingState: "draft", currentVersion: "1.0.0"})
	version := "skill-v1"
	edge := model.PluginRelation{ID: "frozen-ref", SourcePluginID: "expert", TargetPluginID: "skill", Type: "expert_skill", TargetVersionID: &version, Status: 1}
	req := newRequest("expert", "1.0.0")
	if err := repo.InsertReviewRequest(context.Background(), tenantScope(), req, snapshotOf(`{"plugin_name":"Frozen expert","labels":[]}`, `{"attachments":[]}`, []model.PluginRelation{edge})); err != nil {
		t.Fatal(err)
	}
	// A new editable target head never changes the frozen released pin.
	if _, err := database.Exec(`UPDATE plugins SET current_version_id='skill-v2',listing_state='delisted' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	params := pluginrepo.ApproveReviewParams{ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Reviewer"}
	if _, err := repo.ApproveReview(context.Background(), reviewerScope(), params); !errors.Is(err, pluginrepo.ErrInvalidRelation) {
		t.Fatalf("approval accepted delisted dependency: %v", err)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM plugin_review_requests WHERE review_id=?`, req.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("failed approval changed request %s %v", status, err)
	}
	if _, err := database.Exec(`UPDATE plugins SET listing_state='published' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApproveReview(context.Background(), reviewerScope(), params); err != nil {
		t.Fatal(err)
	}
	var pin string
	if err := database.QueryRow(`SELECT target_version_id FROM plugin_version_relation_refs WHERE source_plugin_id='expert'`).Scan(&pin); err != nil || pin != "skill-v1" {
		t.Fatalf("frozen pin=%q err=%v", pin, err)
	}
}

func TestReferenceDeliveryDiagnosticNamesUseFrozenVersions(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	for _, p := range []seedPlugin{{id: "skill", typ: "skill", visibility: "space", listingState: "published"}, {id: "expert", typ: "expert", visibility: "space", listingState: "published"}} {
		seed(t, database, p)
		seedVersionRow(t, database, p.id+"-v1", p.id, "1")
		if _, err := database.Exec(`UPDATE plugin_versions SET manifest_json=JSON_OBJECT('plugin_name','Published name') WHERE version_id=?`, p.id+"-v1"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,'1.0.0','user-1',NOW(3))`, p.id, p.id+"-v1"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE plugins SET published_version_id=?,plugin_name='Unpublished secret name' WHERE plugin_id=?`, p.id+"-v1", p.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO plugin_version_relation_refs(source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id) VALUES ('expert','expert-v1','ref','expert_skill','skill','skill-v1')`); err != nil {
		t.Fatal(err)
	}
	other := pluginrepo.Scope{CallerUID: "other", SpaceID: "space-a"}
	markReferenceIndexFixtures(t, database)
	deps, _, err := repo.ReferenceDependents(context.Background(), other, "skill", "", 20, 0)
	if err != nil || len(deps) != 1 || deps[0].Name != "Published name" {
		t.Fatalf("leaked draft dependent metadata %+v %v", deps, err)
	}
	candidates, _, err := repo.ReferenceCandidates(context.Background(), other, "expert", "expert_skill", 20, 0)
	if err != nil || len(candidates) != 1 || candidates[0].Name != "Published name" {
		t.Fatalf("leaked draft candidate metadata %+v %v", candidates, err)
	}
}

func markReferenceIndexFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO plugin_version_reference_index_state(source_plugin_id,source_version_id,relation_count,snapshot_digest,verified_at) SELECT v.plugin_id,v.version_id,(SELECT COUNT(*) FROM plugin_version_relation_refs r WHERE r.source_version_id=v.version_id),SHA2(CAST(v.relations_json AS CHAR CHARACTER SET utf8mb4),256),NOW(3) FROM plugin_versions v`); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceDeliveryUnknownIndexFailsClosed(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	seed(t, database, seedPlugin{id: "skill", visibility: "private", listingState: "published"})
	seedVersionRow(t, database, "sv", "skill", "1")
	if _, _, err := repo.ReferenceDependents(context.Background(), tenantScope(), "skill", "", 20, 0); !errors.Is(err, pluginrepo.ErrReferenceIndexIncomplete) {
		t.Fatalf("missing historical coverage returned partial result: %v", err)
	}
	markReferenceIndexFixtures(t, database)
	if _, _, err := repo.ReferenceDependents(context.Background(), tenantScope(), "skill", "", 20, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE plugin_version_reference_index_state SET snapshot_digest=REPEAT('0',64)`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ReferenceDependents(context.Background(), tenantScope(), "skill", "", 20, 0); !errors.Is(err, pluginrepo.ErrReferenceIndexIncomplete) {
		t.Fatalf("corrupt coverage marker accepted: %v", err)
	}
}

func TestReferenceCandidatesKeepDraftBindingsPrivate(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	for _, p := range []seedPlugin{{id: "skill", typ: "skill", visibility: "space", listingState: "published"}, {id: "expert", typ: "expert", visibility: "space", listingState: "published"}} {
		seed(t, database, p)
		seedVersionRow(t, database, p.id+"-v1", p.id, "1")
		if _, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,'1.0.0','user-1',NOW(3))`, p.id, p.id+"-v1"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, p.id+"-v1", p.id); err != nil {
			t.Fatal(err)
		}
	}
	assertBinding := func(scope pluginrepo.Scope, want bool) {
		t.Helper()
		rows, n, err := repo.ReferenceCandidates(context.Background(), scope, "expert", "expert_skill", 20, 0)
		if err != nil || n != 1 || len(rows) != 1 {
			t.Fatalf("candidates=%+v n=%d err=%v", rows, n, err)
		}
		if rows[0].AlreadyBound != want || rows[0].IsSelectable == want {
			t.Fatalf("binding leaked or lost: %+v want bound=%v", rows[0], want)
		}
	}
	reader := pluginrepo.Scope{CallerUID: "other", SpaceID: "space-a"}
	assertBinding(reader, false)
	if _, err := database.Exec(`INSERT INTO plugin_relations(relation_id,source_plugin_id,target_plugin_id,relation_type,target_version_id,status,created_by,created_at,updated_at) VALUES ('draft-edge','expert','skill','expert_skill','skill-v1',1,'user-1',NOW(3),NOW(3))`); err != nil {
		t.Fatal(err)
	}
	assertBinding(tenantScope(), true)
	assertBinding(reader, false)
	// A released binding remains visible even when a later draft removes it.
	version := "skill-v1"
	raw, err := model.EncodeSnapshotRelations([]model.PluginRelation{{ID: "released-edge", SourcePluginID: "expert", TargetPluginID: "skill", Type: "expert_skill", TargetVersionID: &version, Status: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE plugin_versions SET relations_json=? WHERE version_id='expert-v1'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`DELETE FROM plugin_relations WHERE relation_id='draft-edge'`); err != nil {
		t.Fatal(err)
	}
	assertBinding(tenantScope(), false)
	assertBinding(reader, true)
	if _, _, err = repo.ReferenceCandidates(context.Background(), pluginrepo.Scope{CallerUID: "other", SpaceID: "space-b"}, "expert", "expert_skill", 20, 0); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-space: %v", err)
	}
	if _, err = database.Exec(`UPDATE plugins SET published_version_id=NULL WHERE plugin_id='expert'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = repo.ReferenceCandidates(context.Background(), reader, "expert", "expert_skill", 20, 0); !errors.Is(err, pluginrepo.ErrVersionBaselineUnavailable) {
		t.Fatalf("unknown baseline: %v", err)
	}
}

func setPublishedFixture(t *testing.T, db *sql.DB, id, version string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, version, id); err != nil {
		t.Fatal(err)
	}
}
