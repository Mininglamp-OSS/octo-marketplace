package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

func dependencyFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, p := range []seedPlugin{
		{id: "skill", typ: "skill", visibility: "space", listingState: "published"},
		{id: "connector", typ: "connector", visibility: "space", listingState: "published"},
		{id: "expert", typ: "expert", visibility: "space", listingState: "published"},
		{id: "team", typ: "expert_team", visibility: "space", listingState: "published"},
	} {
		seed(t, db, p)
		if p.typ == "connector" {
			continue
		}
		seedVersionRow(t, db, p.id+"-v1", p.id, "1")
		if _, err := db.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,'1.0.0','user-1',NOW(3))`, p.id, p.id+"-v1"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, p.id+"-v1", p.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE plugin_versions SET manifest_json=JSON_OBJECT('plugin_name','Released Skill') WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugins SET plugin_name='Unpublished Draft Name' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	for _, edge := range []struct{ source, id, kind, target, pin string }{
		{"expert", "es", "expert_skill", "skill", "skill-v1"},
		{"expert", "ec", "expert_connector", "connector", ""},
		{"team", "te", "expert_team_expert", "expert", "expert-v1"},
	} {
		var pin any
		if edge.pin != "" {
			pin = edge.pin
		}
		if _, err := db.Exec(`INSERT INTO plugin_version_relation_refs(source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id) VALUES (?,?,?,?,?,?)`, edge.source, edge.source+"-v1", edge.id, edge.kind, edge.target, pin); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []string{"expert", "team"} {
		var edges []model.PluginRelation
		if source == "expert" {
			skillPin := "skill-v1"
			edges = []model.PluginRelation{{ID: "es", SourcePluginID: source, TargetPluginID: "skill", Type: "expert_skill", TargetVersionID: &skillPin, Status: 1}, {ID: "ec", SourcePluginID: source, TargetPluginID: "connector", Type: "expert_connector", Status: 1}}
		} else {
			memberPin := "expert-v1"
			edges = []model.PluginRelation{{ID: "te", SourcePluginID: source, TargetPluginID: "expert", Type: "expert_team_expert", TargetVersionID: &memberPin, Status: 1}}
		}
		raw, err := model.EncodeSnapshotRelations(edges)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`UPDATE plugin_versions SET relations_json=? WHERE plugin_id=?`, raw, source); err != nil {
			t.Fatal(err)
		}
	}
}

// The split-brain case from review (formal edge with a NULL pin) is rejected by
// the schema check constraint; visibility still uses the diagnostics COALESCE as
// defense in depth. Pin the schema invariant here.
func TestDependencyFormalRefNullPinRejected(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	seed(t, db, seedPlugin{id: "unreleased-skill", typ: "skill", visibility: "space", listingState: "published"})
	if _, err := db.Exec(`INSERT INTO plugin_version_relation_refs
		(source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id)
		VALUES ('expert','expert-v1','missing-release','expert_skill','unreleased-skill',NULL)`); err == nil {
		t.Fatal("schema accepted a NULL-pinned expert_skill reference")
	}
}

func setDependencySnapshotRelations(t *testing.T, db *sql.DB, pluginID, versionID string, rels []model.PluginRelation) {
	t.Helper()
	raw, err := model.EncodeSnapshotRelations(rels)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE plugin_versions SET relations_json=? WHERE plugin_id=? AND version_id=?`, raw, pluginID, versionID); err != nil {
		t.Fatal(err)
	}
}

func TestDependencySnapshotDoesNotNeedReferenceIndex(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	if _, err := db.Exec(`DELETE FROM plugin_version_relation_refs`); err != nil {
		t.Fatal(err)
	}
	repo := pluginrepo.New(db).WithVersionManagement(true).WithReferences(true)
	ctx := context.Background()
	sc := pluginrepo.Scope{CallerUID: "member", SpaceID: "space-a"}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	blocked, err := repo.HasUnavailableDependencies(ctx, sc, "expert")
	if err != nil || !blocked {
		t.Fatalf("snapshot fallback blocked=%v err=%v", blocked, err)
	}
	issues, err := repo.DependencyIssues(ctx, pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a"}, []model.Plugin{{ID: "expert", Type: model.PluginTypeExpert, ListingState: model.PluginListingStatePublished}})
	if err != nil || len(issues["expert"]) != 1 || issues["expert"][0].TargetName != "Released Skill" || issues["expert"][0].Reason != "delisted" {
		t.Fatalf("snapshot diagnostics issues=%+v err=%v", issues["expert"], err)
	}
}

func TestDependencyTeamUsesPinnedMemberVersion(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	seed(t, db, seedPlugin{id: "skill2", typ: "skill", visibility: "space", listingState: "published"})
	seedVersionRow(t, db, "skill2-v1", "skill2", "1")
	if _, err := db.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES ('skill2','skill2-v1','1.0.0','user-1',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugins SET published_version_id='skill2-v1' WHERE plugin_id='skill2'`); err != nil {
		t.Fatal(err)
	}
	seedVersionRow(t, db, "expert-v2", "expert", "2")
	if _, err := db.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES ('expert','expert-v2','2.0.0','user-1',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	pin := "skill2-v1"
	setDependencySnapshotRelations(t, db, "expert", "expert-v2", []model.PluginRelation{{ID: "es2", SourcePluginID: "expert", TargetPluginID: "skill2", Type: "expert_skill", TargetVersionID: &pin, Status: 1}})
	if _, err := db.Exec(`UPDATE plugins SET published_version_id='expert-v2' WHERE plugin_id='expert'`); err != nil {
		t.Fatal(err)
	}
	repo := pluginrepo.New(db).WithVersionManagement(true).WithReferences(true)
	ctx := context.Background()
	sc := pluginrepo.Scope{CallerUID: "member", SpaceID: "space-a"}
	// expert-v2's skill is delisted; the team still pins expert-v1, whose skill is healthy.
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill2'`); err != nil {
		t.Fatal(err)
	}
	blocked, err := repo.HasUnavailableDependencies(ctx, sc, "team")
	if err != nil || blocked {
		t.Fatalf("team false-hide blocked=%v err=%v", blocked, err)
	}
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	graph, err := svc.DetailGraph(ctx, pluginsvc.Caller{UID: "member", SpaceID: "space-a"}, "team")
	if err != nil || graph.Plugin == nil || graph.Plugin.ID != "team" {
		t.Fatalf("pinned team graph=%+v err=%v", graph, err)
	}
	ownerScope := pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a"}
	issues, err := repo.DependencyIssues(ctx, ownerScope, []model.Plugin{{ID: "team", Type: model.PluginTypeExpertTeam, ListingState: model.PluginListingStatePublished}})
	if err != nil || len(issues["team"]) != 0 {
		t.Fatalf("team false-hide diagnostics=%+v err=%v", issues["team"], err)
	}
	// v1's actual skill is delisted; the team must hide despite expert-v2 being healthy.
	if _, err := db.Exec(`UPDATE plugins SET listing_state='published' WHERE plugin_id='skill2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	blocked, err = repo.HasUnavailableDependencies(ctx, sc, "team")
	if err != nil || !blocked {
		t.Fatalf("team false-show blocked=%v err=%v", blocked, err)
	}
	issues, err = repo.DependencyIssues(ctx, ownerScope, []model.Plugin{{ID: "team", Type: model.PluginTypeExpertTeam, ListingState: model.PluginListingStatePublished}})
	if err != nil || len(issues["team"]) != 1 || issues["team"][0].TargetPluginID != "skill" || len(issues["team"][0].Via) != 1 || issues["team"][0].Via[0] != "expert" {
		t.Fatalf("team false-show diagnostics=%+v err=%v", issues["team"], err)
	}
}

func TestDependencySnapshotHandlesUnpinnedEdges(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	if _, err := db.Exec(`DELETE FROM plugin_version_relation_refs`); err != nil {
		t.Fatal(err)
	}
	setDependencySnapshotRelations(t, db, "expert", "expert-v1", []model.PluginRelation{{ID: "es", SourcePluginID: "expert", TargetPluginID: "skill", Type: "expert_skill", Status: 1}})
	repo := pluginrepo.New(db).WithVersionManagement(true).WithReferences(false)
	ctx := context.Background()
	sc := pluginrepo.Scope{CallerUID: "member", SpaceID: "space-a"}
	blocked, err := repo.HasUnavailableDependencies(ctx, sc, "expert")
	if err != nil || blocked {
		t.Fatalf("healthy unpinned edge blocked=%v err=%v", blocked, err)
	}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	blocked, err = repo.HasUnavailableDependencies(ctx, sc, "expert")
	if err != nil || !blocked {
		t.Fatalf("delisted unpinned target blocked=%v err=%v", blocked, err)
	}
}

func TestDependencySnapshotHandlesHistoricalFieldNames(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	if _, err := db.Exec(`DELETE FROM plugin_version_relation_refs`); err != nil {
		t.Fatal(err)
	}
	// Historical Go field names are still accepted by model.DecodeSnapshotRelations.
	historical := []byte(`[{"ID":"es","SourcePluginID":"expert","TargetPluginID":"skill","TargetVersionID":"skill-v1","Type":"expert_skill","Status":1}]`)
	if _, err := db.Exec(`UPDATE plugin_versions SET relations_json=? WHERE plugin_id='expert' AND version_id='expert-v1'`, historical); err != nil {
		t.Fatal(err)
	}
	teamHistorical := []byte(`[{"ID":"te","SourcePluginID":"team","TargetPluginID":"expert","TargetVersionID":"expert-v1","Type":"expert_team_expert","Status":1}]`)
	if _, err := db.Exec(`UPDATE plugin_versions SET relations_json=? WHERE plugin_id='team' AND version_id='team-v1'`, teamHistorical); err != nil {
		t.Fatal(err)
	}
	repo := pluginrepo.New(db).WithVersionManagement(true).WithReferences(false)
	ctx := context.Background()
	sc := pluginrepo.Scope{CallerUID: "member", SpaceID: "space-a"}
	if blocked, err := repo.HasUnavailableDependencies(ctx, sc, "team"); err != nil || blocked {
		t.Fatalf("healthy historical team blocked=%v err=%v", blocked, err)
	}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"expert", "team"} {
		blocked, err := repo.HasUnavailableDependencies(ctx, sc, id)
		if err != nil || !blocked {
			t.Fatalf("historical %s blocked=%v err=%v", id, blocked, err)
		}
	}
}

func TestDependencyLegacyTeamHonorsLiveMemberPin(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	seed(t, db, seedPlugin{id: "skill2", typ: "skill", visibility: "space", listingState: "published"})
	seedVersionRow(t, db, "skill2-v1", "skill2", "1")
	if _, err := db.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES ('skill2','skill2-v1','1.0.0','user-1',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugins SET published_version_id='skill2-v1' WHERE plugin_id='skill2'`); err != nil {
		t.Fatal(err)
	}
	seedVersionRow(t, db, "expert-v2", "expert", "2")
	if _, err := db.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES ('expert','expert-v2','2.0.0','user-1',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	pin := "skill2-v1"
	setDependencySnapshotRelations(t, db, "expert", "expert-v2", []model.PluginRelation{{ID: "es2", SourcePluginID: "expert", TargetPluginID: "skill2", Type: "expert_skill", TargetVersionID: &pin, Status: 1}})
	if _, err := db.Exec(`UPDATE plugins SET published_version_id='expert-v2' WHERE plugin_id='expert'`); err != nil {
		t.Fatal(err)
	}
	// The team root is legacy (no published pointer), but its live member edge is
	// pinned to expert-v1.
	if _, err := db.Exec(`UPDATE plugins SET published_version_id=NULL WHERE plugin_id='team'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_relations(relation_id,source_plugin_id,target_plugin_id,relation_type,target_version_id,status,created_by,created_at,updated_at) VALUES ('team-live','team','expert','expert_team_expert','expert-v1',1,'user-1',NOW(3),NOW(3))`); err != nil {
		t.Fatal(err)
	}
	repo := pluginrepo.New(db).WithVersionManagement(true).WithReferences(true)
	ctx := context.Background()
	sc := pluginrepo.Scope{CallerUID: "member", SpaceID: "space-a"}
	ownerScope := pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a"}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill2'`); err != nil {
		t.Fatal(err)
	}
	blocked, err := repo.HasUnavailableDependencies(ctx, sc, "team")
	if err != nil || blocked {
		t.Fatalf("legacy team false-hide blocked=%v err=%v", blocked, err)
	}
	issues, err := repo.DependencyIssues(ctx, ownerScope, []model.Plugin{{ID: "team", Type: model.PluginTypeExpertTeam, ListingState: model.PluginListingStatePublished}})
	if err != nil || len(issues["team"]) != 0 {
		t.Fatalf("legacy team false-hide diagnostics=%+v err=%v", issues["team"], err)
	}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='published' WHERE plugin_id='skill2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	blocked, err = repo.HasUnavailableDependencies(ctx, sc, "team")
	if err != nil || !blocked {
		t.Fatalf("legacy team false-show blocked=%v err=%v", blocked, err)
	}
	issues, err = repo.DependencyIssues(ctx, ownerScope, []model.Plugin{{ID: "team", Type: model.PluginTypeExpertTeam, ListingState: model.PluginListingStatePublished}})
	if err != nil || len(issues["team"]) != 1 || issues["team"][0].TargetPluginID != "skill" || len(issues["team"][0].Via) != 1 || issues["team"][0].Via[0] != "expert" {
		t.Fatalf("legacy team false-show diagnostics=%+v err=%v", issues["team"], err)
	}
}

// The web UI's 发布 action on a delisted row calls POST /plugins/publish with the
// row's current version. For a version-managed Space resource that is the same
// label as the existing release, so the current API refuses it. This pins the
// behaviour the UI currently sees; a same-release relist path would change it.
func TestFrontendRelistPublishSameVersionIsRejected(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	repo := pluginrepo.New(db).WithVersionManagement(true).WithReferences(true)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	ctx := context.Background()
	owner := managedOwner()
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: "skill", Version: "1.0.0"})
	if !errors.Is(err, pluginsvc.ErrLabelTaken) {
		t.Fatalf("same-version relist err=%v, want ErrLabelTaken", err)
	}
}

// The documented recovery path is edit -> save a new version -> 发布. That must
// restore the dependent expert even though the expert still pins skill-v1.
func TestFrontendRelistPublishNewVersionRestoresDependency(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	repo := pluginrepo.New(db).WithVersionManagement(true).WithReferences(true)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	ctx := context.Background()
	owner := managedOwner()
	if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), true, "reviewer", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	if blocked, err := repo.HasUnavailableDependencies(ctx, pluginrepo.Scope{CallerUID: "member", SpaceID: "space-a"}, "expert"); err != nil || !blocked {
		t.Fatalf("precondition expert blocked=%v err=%v", blocked, err)
	}
	if _, err := svc.Update(ctx, owner, "skill", managedSkillWrite("2.0.0", "relisted skill v2", model.PluginVisibilitySpace)); err != nil {
		t.Fatalf("update skill: %v", err)
	}
	publishManaged(t, svc, owner, "skill")
	blocked, err := repo.HasUnavailableDependencies(ctx, pluginrepo.Scope{CallerUID: "member", SpaceID: "space-a"}, "expert")
	if err != nil || blocked {
		t.Fatalf("expert after relist blocked=%v err=%v", blocked, err)
	}
}

// A later private release of a dependency must not leak its name through the
// owner's root diagnostics. The expert's frozen edge pins the old public v1
// release; v2 is private and owned by another user.
func TestDependencyDiagnosticsRedactsLaterPrivateTargetName(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	seedVersionRow(t, db, "skill-v2", "skill", "2")
	if _, err := db.Exec(`UPDATE plugin_versions SET manifest_json=JSON_OBJECT('plugin_name','Later Private Name') WHERE plugin_id='skill' AND version_id='skill-v2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES ('skill','skill-v2','2.0.0','author-a',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugins SET published_version_id='skill-v2',visibility='private',owner_uid='author-a',space_id='space-b' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugins SET deleted_at=NOW(3) WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	repo := pluginrepo.New(db).WithVersionManagement(true).WithReferences(true)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	ctx := context.Background()
	detail, err := svc.Detail(ctx, managedOwner(), "expert", true)
	if err != nil {
		t.Fatalf("owner detail: %v", err)
	}
	if len(detail.DependencyIssues) != 1 || detail.DependencyIssues[0].Reason != "deleted" {
		t.Fatalf("diagnostics=%+v", detail.DependencyIssues)
	}
	if detail.DependencyIssues[0].TargetName != "" {
		t.Fatalf("private later name leaked: %q", detail.DependencyIssues[0].TargetName)
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Owner"}, "space-a")
	pluginhandler.New(svc).Register(engine.Group("/api/v1", auth.Handler()))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/detail?plugin_id=expert", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			DependencyIssues []struct {
				TargetName string `json:"target_name"`
			} `json:"dependency_issues"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.DependencyIssues) != 1 || body.Data.DependencyIssues[0].TargetName != "" {
		t.Fatalf("HTTP diagnostics=%+v", body.Data.DependencyIssues)
	}
}

// Legacy live relations have no target foreign key; a dangling target must be
// reported as deleted, not turn owner detail/admin list into a 500.
func TestDependencyDiagnosticsHandlesMissingLiveTarget(t *testing.T) {
	db := reviewDB(t)
	seed(t, db, seedPlugin{id: "legacy-expert", typ: "expert", visibility: "space", listingState: "published"})
	if _, err := db.Exec(`INSERT INTO plugin_relations(relation_id,source_plugin_id,target_plugin_id,relation_type,status,created_by,created_at,updated_at) VALUES ('dangling','legacy-expert','missing-connector','expert_connector',1,'user-1',NOW(3),NOW(3))`); err != nil {
		t.Fatal(err)
	}
	repo := pluginrepo.New(db).WithVersionManagement(true).WithReferences(true)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	ctx := context.Background()
	detail, err := svc.Detail(ctx, managedOwner(), "legacy-expert", true)
	if err != nil {
		t.Fatalf("owner detail: %v", err)
	}
	if len(detail.DependencyIssues) != 1 || detail.DependencyIssues[0].Reason != "deleted" || detail.DependencyIssues[0].TargetName != "" {
		t.Fatalf("owner diagnostics=%+v", detail.DependencyIssues)
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Owner"}, "space-a")
	pluginhandler.New(svc).Register(engine.Group("/api/v1", auth.Handler()))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/detail?plugin_id=legacy-expert", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, _, err := svc.AdminList(ctx, pluginsvc.Caller{UID: "admin", IsSystemAdmin: true}, model.PluginTypeExpert, "", pluginsvc.ListParams{Limit: 20}); err != nil {
		t.Fatalf("admin list: %v", err)
	}
}

func TestDependencyDelistingHidesCatalogButNotOwnerOrAdmin(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	repo := pluginrepo.New(db).WithVersionManagement(true).WithReferences(true)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	ctx := context.Background()
	member := pluginsvc.Caller{UID: "member", SpaceID: "space-a"}
	owner := managedOwner()
	list := func() ([]model.Plugin, int64) {
		t.Helper()
		items, total, err := repo.List(ctx, pluginrepo.Scope{CallerUID: member.UID, SpaceID: member.SpaceID}, pluginrepo.ListFilter{PlacementCode: "default", Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		return items, total
	}
	if _, total := list(); total != 4 {
		t.Fatalf("before delist total=%d, want 4", total)
	}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	items, total := list()
	if total != 1 || len(items) != 1 || items[0].ID != "connector" {
		t.Fatalf("after delist total=%d items=%+v", total, items)
	}
	page, pageTotal, err := repo.List(ctx, pluginrepo.Scope{CallerUID: member.UID, SpaceID: member.SpaceID}, pluginrepo.ListFilter{PlacementCode: "default", Limit: 1, Offset: 1})
	if err != nil || pageTotal != 1 || len(page) != 0 {
		t.Fatalf("filtered pagination page=%+v total=%d err=%v", page, pageTotal, err)
	}
	if _, err := repo.DependencyIssues(ctx, pluginrepo.Scope{CallerUID: "other", SpaceID: "other-space"}, []model.Plugin{{ID: "expert", Type: model.PluginTypeExpert, ListingState: model.PluginListingStatePublished}}); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-space diagnostic err=%v, want not found", err)
	}
	for _, id := range []string{"expert", "team"} {
		if _, err := svc.Detail(ctx, member, id, true); !errors.Is(err, pluginsvc.ErrNotFound) {
			t.Fatalf("member detail %s err=%v, want not found", id, err)
		}
		if _, err := svc.DetailGraph(ctx, member, id); !errors.Is(err, pluginsvc.ErrNotFound) {
			t.Fatalf("member graph %s err=%v, want not found", id, err)
		}
	}
	owned, _, err := svc.List(ctx, owner, pluginsvc.ListParams{Mine: true, PlacementCode: "default", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range owned {
		if p.ID == "expert" || p.ID == "team" {
			if p.ListingState != model.PluginListingStatePublished || len(p.DependencyIssues) != 1 || p.DependencyIssues[0].TargetName != "Released Skill" || p.DependencyIssues[0].Reason != "delisted" {
				t.Fatalf("owner item %s: %+v", p.ID, p)
			}
			if p.ID == "team" && (len(p.DependencyIssues[0].Via) != 1 || p.DependencyIssues[0].Via[0] != "expert") {
				t.Fatalf("team via=%+v", p.DependencyIssues[0].Via)
			}
		}
	}
	detail, err := svc.Detail(ctx, owner, "expert", true)
	if err != nil || len(detail.DependencyIssues) != 1 {
		t.Fatalf("owner detail=%+v err=%v", detail, err)
	}
	graph, err := svc.DetailGraph(ctx, owner, "expert")
	if err != nil || len(graph.DependencyIssues) != 1 {
		t.Fatalf("owner graph=%+v err=%v", graph, err)
	}
	admin, _, err := svc.AdminList(ctx, pluginsvc.Caller{UID: "admin", IsSystemAdmin: true}, model.PluginTypeExpert, "", pluginsvc.ListParams{Limit: 20})
	if err != nil || len(admin) != 1 || len(admin[0].DependencyIssues) != 1 {
		t.Fatalf("admin list=%+v err=%v", admin, err)
	}
	adminDetail, err := svc.AdminDetail(ctx, pluginsvc.Caller{UID: "admin", IsSystemAdmin: true}, "expert", true)
	if err != nil || len(adminDetail.DependencyIssues) != 1 {
		t.Fatalf("admin detail=%+v err=%v", adminDetail, err)
	}
	if _, err := repo.ListTags(ctx, pluginrepo.Scope{CallerUID: member.UID, SpaceID: member.SpaceID}, pluginrepo.TagListFilter{PlacementCode: "default", Type: model.PluginTypeExpert}); err != nil {
		t.Fatalf("tag query: %v", err)
	}
	if _, err := repo.ListPlacementCategories(ctx, pluginrepo.Scope{CallerUID: member.UID, SpaceID: member.SpaceID}, "default", model.PluginTypeExpert); err != nil {
		t.Fatalf("category query: %v", err)
	}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='published' WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	if _, total := list(); total != 4 {
		t.Fatalf("restored total=%d, want 4 without republishing expert", total)
	}
	if _, err := svc.Detail(ctx, member, "expert", true); err != nil {
		t.Fatalf("restored detail: %v", err)
	}
	if _, err := db.Exec(`UPDATE plugins SET deleted_at=NOW(3) WHERE plugin_id='skill'`); err != nil {
		t.Fatal(err)
	}
	if _, total := list(); total != 1 { // only the connector remains visible
		t.Fatalf("deleted skill total=%d, want 1", total)
	}
	ownedDetail, err := svc.Detail(ctx, owner, "expert", true)
	if err != nil || len(ownedDetail.DependencyIssues) != 1 || ownedDetail.DependencyIssues[0].Reason != "deleted" {
		t.Fatalf("deleted dependency owner detail=%+v err=%v", ownedDetail, err)
	}
	if len(ownedDetail.Relations) != 0 {
		t.Fatalf("owner diagnostics leaked hidden relation content: %+v", ownedDetail.Relations)
	}
}

func TestDependencyConnectorAndMissingRelease(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	repo := pluginrepo.New(db).WithVersionManagement(true).WithReferences(true)
	ctx := context.Background()
	sc := pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a"}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='connector'`); err != nil {
		t.Fatal(err)
	}
	roots, _, err := repo.List(ctx, sc, pluginrepo.ListFilter{Mine: true, Type: model.PluginTypeExpert})
	if err != nil {
		t.Fatal(err)
	}
	issues, err := repo.DependencyIssues(ctx, sc, roots)
	if err != nil || len(issues["expert"]) != 1 || issues["expert"][0].Reason != "delisted" || issues["expert"][0].RelationType != "expert_connector" {
		t.Fatalf("connector issues=%+v err=%v", issues, err)
	}
	if visible, total, err := repo.List(ctx, sc, pluginrepo.ListFilter{Type: model.PluginTypeExpert}); err != nil || total != 0 || len(visible) != 0 {
		t.Fatalf("delisted connector catalog visible=%+v total=%d err=%v", visible, total, err)
	}
	// A legacy live pin has no FK; a missing release must block it even while
	// the target resource itself remains on the shelf.
	seed(t, db, seedPlugin{id: "legacy", typ: "expert", visibility: "space", listingState: "published"})
	seedRelation(t, db, "legacy-skill", "legacy", "skill", "expert_skill")
	if _, err := db.Exec(`UPDATE plugin_relations SET target_version_id='absent-v1' WHERE relation_id='legacy-skill'`); err != nil {
		t.Fatal(err)
	}
	issues, err = repo.DependencyIssues(ctx, sc, []model.Plugin{{ID: "legacy", Type: model.PluginTypeExpert, ListingState: model.PluginListingStatePublished}})
	if err != nil || len(issues["legacy"]) != 1 || issues["legacy"][0].Reason != "release_missing" {
		t.Fatalf("missing release issues=%+v err=%v", issues, err)
	}
	if visible, total, err := repo.List(ctx, sc, pluginrepo.ListFilter{Type: model.PluginTypeExpert}); err != nil || total != 0 || len(visible) != 0 {
		t.Fatalf("missing release catalog visible=%+v total=%d err=%v", visible, total, err)
	}
	// A later draft relation must not change the released graph.
	seedRelation(t, db, "draft-skill", "expert", "skill", "expert_skill")
	if _, err := db.Exec(`UPDATE plugin_relations SET target_version_id='absent-v1' WHERE relation_id='draft-skill'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugins SET listing_state='published' WHERE plugin_id='connector'`); err != nil {
		t.Fatal(err)
	}
	issues, err = repo.DependencyIssues(ctx, sc, roots)
	if err != nil || len(issues["expert"]) != 0 {
		t.Fatalf("draft relation leaked into release: %+v err=%v", issues, err)
	}
}
