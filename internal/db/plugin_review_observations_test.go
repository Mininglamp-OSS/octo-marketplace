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

// --- A15 observation fixtures ------------------------------------------------

// seedReleasedVersion appends an immutable release snapshot for pluginID. The
// relations slice becomes that release's frozen graph, so a member Expert's
// published release can carry its own Skill/Connector edges.
func seedReleasedVersion(t *testing.T, database *sql.DB, pluginID, versionID, label string, rels []model.PluginRelation) {
	t.Helper()
	raw, err := model.EncodeSnapshotRelations(rels)
	if err != nil {
		t.Fatalf("encode relations for %s: %v", versionID, err)
	}
	if _, err := database.Exec(`INSERT INTO plugin_versions
		(version_id,plugin_id,version,declared_version,manifest_json,plugin_json,
		 manifest_hash,plugin_hash,relations_json,created_by,created_at,
		 snapshot_schema_version,artifacts_json)
		VALUES (?,?,?,?,JSON_OBJECT(),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),?,'user-1',NOW(3),2,JSON_ARRAY())`,
		versionID, pluginID, label, label, string(raw)); err != nil {
		t.Fatalf("seed release version %s: %v", versionID, err)
	}
	if _, err := database.Exec(`INSERT INTO plugin_version_releases
		(plugin_id,version_id,version_label,released_by,released_at)
		VALUES (?,?,?,'user-1',NOW(3))`, pluginID, versionID, label); err != nil {
		t.Fatalf("seed release %s: %v", versionID, err)
	}
}

// seedReleasedDependency creates a published versioned dependency whose release
// graph is rels, and points the plugin's published pointer at it.
func seedReleasedDependency(t *testing.T, database *sql.DB, pluginID, typ, versionID, label string, rels []model.PluginRelation) {
	t.Helper()
	seed(t, database, seedPlugin{id: pluginID, typ: typ, visibility: "space", listingState: "published", currentVersion: label})
	seedReleasedVersion(t, database, pluginID, versionID, label, rels)
	if _, err := database.Exec(`UPDATE plugins SET published_version_id=?,current_version_id=? WHERE plugin_id=?`, versionID, versionID, pluginID); err != nil {
		t.Fatalf("point %s at %s: %v", pluginID, versionID, err)
	}
}

// seedConnector dependency/root with an explicit effective baseline.
func seedConnectorWithEffective(t *testing.T, database *sql.DB, pluginID string, listingState string, revision uint64, digest string) {
	t.Helper()
	seed(t, database, seedPlugin{id: pluginID, typ: "connector", visibility: "space", listingState: listingState, currentVersion: "1.0.0"})
	if _, err := database.Exec(`INSERT INTO plugin_connector_effective
		(plugin_id,effective_revision,effective_digest,plugin_name,tags_json,tool_count,
		 manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,
		 current_version,visibility,updated_by,updated_at)
		SELECT plugin_id,?,?,plugin_name,tags_json,tool_count,manifest_json,plugin_json,
		       attachment_keys_json,manifest_hash,plugin_hash,current_version,visibility,'user-1',NOW(3)
		  FROM plugins WHERE plugin_id=?`, revision, digest, pluginID); err != nil {
		t.Fatalf("seed connector effective %s: %v", pluginID, err)
	}
}

func setPublishedVersion(t *testing.T, database *sql.DB, pluginID, versionID string) {
	t.Helper()
	if _, err := database.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, versionID, pluginID); err != nil {
		t.Fatalf("switch published version of %s: %v", pluginID, err)
	}
}

func bumpConnectorEffective(t *testing.T, database *sql.DB, pluginID string, revision uint64, digest string) {
	t.Helper()
	if _, err := database.Exec(`UPDATE plugin_connector_effective SET effective_revision=?,effective_digest=? WHERE plugin_id=?`, revision, digest, pluginID); err != nil {
		t.Fatalf("bump connector effective %s: %v", pluginID, err)
	}
}

// storedReviewObservations reads the two additive JSON columns without going
// through the repository, so the test pins what was actually persisted.
func storedReviewObservations(t *testing.T, database *sql.DB, reviewID string) (deps []model.ReviewDependencyObservation, conns []model.ReviewConnectorObservation, present bool) {
	t.Helper()
	var depRaw, connRaw sql.NullString
	if err := database.QueryRow(`SELECT resolved_dependency_observations_json,connector_observations_json
		FROM plugin_review_requests WHERE review_id=?`, reviewID).Scan(&depRaw, &connRaw); err != nil {
		t.Fatalf("read observations: %v", err)
	}
	present = depRaw.Valid || connRaw.Valid
	if depRaw.Valid {
		if err := json.Unmarshal([]byte(depRaw.String), &deps); err != nil {
			t.Fatalf("decode dependency observations: %v", err)
		}
	}
	if connRaw.Valid {
		if err := json.Unmarshal([]byte(connRaw.String), &conns); err != nil {
			t.Fatalf("decode connector observations: %v", err)
		}
	}
	return deps, conns, present
}

func reviewObservationRepo(t *testing.T) (*sql.DB, *pluginrepo.Repo) {
	t.Helper()
	database := reviewDB(t)
	return database, pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
}

// --- submit stores observations ---------------------------------------------

// A15: submit freezes the latest formal releases of the submitted graph —
// Skill/Expert by published version, Connector by effective revision/digest —
// and persists them with the request so list/detail never re-resolve.
func TestReviewSubmitStoresLatestDependencyObservations(t *testing.T) {
	database, repo := reviewObservationRepo(t)
	ctx := context.Background()

	seedReleasedDependency(t, database, "skill-a", "skill", "skill-a-v1", "1.0.0", nil)
	seedReleasedDependency(t, database, "skill-b", "skill", "skill-b-v1", "1.0.0", nil)
	seedConnectorWithEffective(t, database, "connector-c", "published", 4, "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	seed(t, database, seedPlugin{id: "expert-src", typ: "expert", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})

	rels := []model.PluginRelation{
		{ID: "r1", SourcePluginID: "expert-src", TargetPluginID: "skill-a", Type: "expert_skill", TargetPluginType: "skill", Status: 1},
		{ID: "r2", SourcePluginID: "expert-src", TargetPluginID: "skill-b", Type: "expert_skill", TargetPluginType: "skill", Status: 1},
		{ID: "r3", SourcePluginID: "expert-src", TargetPluginID: "connector-c", Type: "expert_connector", TargetPluginType: "connector", Status: 1},
	}
	req := newRequest("expert-src", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Fixture"}`, `{"attachments":[]}`, rels)); err != nil {
		t.Fatalf("InsertReviewRequest: %v", err)
	}

	deps, conns, present := storedReviewObservations(t, database, req.ID)
	if !present {
		t.Fatal("submit did not persist A15 observations")
	}
	if len(deps) != 2 || deps[0].PluginID != "skill-a" || deps[1].PluginID != "skill-b" {
		t.Fatalf("dependency observations = %+v", deps)
	}
	if deps[0].VersionID != "skill-a-v1" || deps[0].PluginType != model.PluginTypeSkill ||
		deps[1].VersionID != "skill-b-v1" || deps[1].PluginType != model.PluginTypeSkill {
		t.Fatalf("dependency observations = %+v", deps)
	}
	if len(conns) != 1 || conns[0].PluginID != "connector-c" ||
		conns[0].EffectiveRevision == nil || *conns[0].EffectiveRevision != 4 ||
		conns[0].EffectiveDigest == nil || *conns[0].EffectiveDigest != "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" {
		t.Fatalf("connector observations = %+v", conns)
	}

	// Detail read returns the persisted values without loading the full graph.
	detail, err := repo.LoadReviewSnapshot(ctx, tenantScope(), req.ID, false)
	if err != nil {
		t.Fatalf("LoadReviewSnapshot: %v", err)
	}
	if !detail.ObservationsPresent || len(detail.ResolvedDependencyObservations) != 2 || len(detail.ConnectorObservations) != 1 {
		t.Fatalf("detail observations = %+v/%+v present=%v", detail.ResolvedDependencyObservations, detail.ConnectorObservations, detail.ObservationsPresent)
	}
	// The list projection carries the same small audit payload.
	items, total, err := repo.ListReviewRequests(ctx, tenantScope(), pluginrepo.ReviewListFilter{SpaceID: "space-a", ApplicantUID: "user-1", Limit: 20})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("list = %d/%d err=%v", len(items), total, err)
	}
	if !items[0].ObservationsPresent || len(items[0].ResolvedDependencyObservations) != 2 {
		t.Fatalf("list observations = %+v", items[0].ResolvedDependencyObservations)
	}

	// Cross-Space reads stay hidden, observations included.
	if _, err := repo.LoadReviewSnapshot(ctx, pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-b"}, req.ID, true); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-Space reviewer read = %v, want ErrNotFound", err)
	}
	if _, total, err := repo.ListReviewRequests(ctx, pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-b"}, pluginrepo.ReviewListFilter{SpaceID: "space-b", Limit: 20}); err != nil || total != 0 {
		t.Fatalf("cross-Space list total=%d err=%v", total, err)
	}
}

// A Team's observation set follows the frozen members into their CURRENT
// published releases, dedupes a skill shared by two members, and records the
// members' Connector targets too.
func TestReviewTeamObservationsExpandMemberExpertGraph(t *testing.T) {
	database, repo := reviewObservationRepo(t)
	ctx := context.Background()

	seedReleasedDependency(t, database, "skill-shared", "skill", "skill-shared-v1", "1.0.0", nil)
	seedConnectorWithEffective(t, database, "connector-shared", "published", 2, "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	memberRels := []model.PluginRelation{
		{ID: "ma-skill", SourcePluginID: "member-a", TargetPluginID: "skill-shared", Type: "expert_skill", TargetPluginType: "skill", Status: 1},
		{ID: "ma-conn", SourcePluginID: "member-a", TargetPluginID: "connector-shared", Type: "expert_connector", TargetPluginType: "connector", Status: 1},
		{ID: "mb-skill", SourcePluginID: "member-b", TargetPluginID: "skill-shared", Type: "expert_skill", TargetPluginType: "skill", Status: 1},
	}
	seed(t, database, seedPlugin{id: "member-a", typ: "expert", visibility: "space", listingState: "published", currentVersion: "1.0.0"})
	seedReleasedVersion(t, database, "member-a", "member-a-v1", "1.0.0", memberRels[:2])
	if _, err := database.Exec(`UPDATE plugins SET published_version_id='member-a-v1' WHERE plugin_id='member-a'`); err != nil {
		t.Fatal(err)
	}
	seed(t, database, seedPlugin{id: "member-b", typ: "expert", visibility: "space", listingState: "published", currentVersion: "1.0.0"})
	seedReleasedVersion(t, database, "member-b", "member-b-v1", "1.0.0", memberRels[2:])
	if _, err := database.Exec(`UPDATE plugins SET published_version_id='member-b-v1' WHERE plugin_id='member-b'`); err != nil {
		t.Fatal(err)
	}
	seed(t, database, seedPlugin{id: "team-src", typ: "expert_team", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	teamRels := []model.PluginRelation{
		{ID: "t-a", SourcePluginID: "team-src", TargetPluginID: "member-a", Type: "expert_team_expert", TargetPluginType: "expert", Status: 1, Data: json.RawMessage(`{"member_key":"a","is_leader":true}`)},
		{ID: "t-b", SourcePluginID: "team-src", TargetPluginID: "member-b", Type: "expert_team_expert", TargetPluginType: "expert", Status: 1, Data: json.RawMessage(`{"member_key":"b"}`)},
	}
	req := newRequest("team-src", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Team"}`, `{"attachments":[]}`, teamRels)); err != nil {
		t.Fatalf("InsertReviewRequest: %v", err)
	}
	deps, conns, present := storedReviewObservations(t, database, req.ID)
	if !present {
		t.Fatal("team submit did not persist observations")
	}
	wantDeps := map[string]string{
		"member-a":     "member-a-v1",
		"member-b":     "member-b-v1",
		"skill-shared": "skill-shared-v1",
	}
	if len(deps) != len(wantDeps) {
		t.Fatalf("team dependency observations = %+v", deps)
	}
	for _, obs := range deps {
		if wantDeps[obs.PluginID] != obs.VersionID {
			t.Fatalf("team dependency %s = %s, want %q", obs.PluginID, obs.VersionID, wantDeps[obs.PluginID])
		}
	}
	if len(conns) != 1 || conns[0].PluginID != "connector-shared" || conns[0].EffectiveRevision == nil || *conns[0].EffectiveRevision != 2 {
		t.Fatalf("team connector observations = %+v", conns)
	}
}

// An unresolved dependency must fail the submit instead of persisting an empty
// observation set.
func TestReviewSubmitRefusesUnresolvableDependency(t *testing.T) {
	database, repo := reviewObservationRepo(t)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "skill-unpublished", typ: "skill", visibility: "space", listingState: "draft"})
	seed(t, database, seedPlugin{id: "expert-src", typ: "expert", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	rels := []model.PluginRelation{{ID: "r1", SourcePluginID: "expert-src", TargetPluginID: "skill-unpublished", Type: "expert_skill", TargetPluginType: "skill", Status: 1}}
	err := repo.InsertReviewRequest(ctx, tenantScope(), newRequest("expert-src", "1.0.0"), snapshotOf(`{"plugin_name":"Fixture"}`, `{"attachments":[]}`, rels))
	if err == nil {
		t.Fatal("submit accepted an unpublished dependency")
	}
}

// --- approve re-resolves and refuses drift ----------------------------------

func approvePending(t *testing.T, repo *pluginrepo.Repo, reviewID string) (*model.Plugin, error) {
	t.Helper()
	return repo.ApproveReview(context.Background(), reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: reviewID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	})
}

// A changed Skill release or Connector effective revision invalidates the
// review window; an unchanged graph approves normally.
func TestReviewApproveRejectsChangedDependencyObservations(t *testing.T) {
	t.Run("changed skill release", func(t *testing.T) {
		database, repo := reviewObservationRepo(t)
		ctx := context.Background()
		seedReleasedDependency(t, database, "skill-a", "skill", "skill-a-v1", "1.0.0", nil)
		seedReleasedVersion(t, database, "skill-a", "skill-a-v2", "2.0.0", nil)
		seed(t, database, seedPlugin{id: "expert-src", typ: "expert", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
		rels := []model.PluginRelation{{ID: "r1", SourcePluginID: "expert-src", TargetPluginID: "skill-a", Type: "expert_skill", TargetPluginType: "skill", Status: 1}}
		req := newRequest("expert-src", "1.0.0")
		if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Fixture"}`, `{"attachments":[]}`, rels)); err != nil {
			t.Fatal(err)
		}
		setPublishedVersion(t, database, "skill-a", "skill-a-v2")
		if _, err := approvePending(t, repo, req.ID); !errors.Is(err, pluginrepo.ErrDependencyChanged) {
			t.Fatalf("approve = %v, want ErrDependencyChanged", err)
		}
		var status, listing string
		if err := database.QueryRow(`SELECT status FROM plugin_review_requests WHERE review_id=?`, req.ID).Scan(&status); err != nil || status != "pending" {
			t.Fatalf("request status=%q err=%v, want pending", status, err)
		}
		if err := database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id='expert-src'`).Scan(&listing); err != nil || listing != "draft" {
			t.Fatalf("source listing=%q err=%v, want draft", listing, err)
		}
	})

	t.Run("changed team member release", func(t *testing.T) {
		database, repo := reviewObservationRepo(t)
		ctx := context.Background()
		seedReleasedDependency(t, database, "skill-a", "skill", "skill-a-v1", "1.0.0", nil)
		memberRels := []model.PluginRelation{{ID: "m-skill", SourcePluginID: "member-a", TargetPluginID: "skill-a", Type: "expert_skill", TargetPluginType: "skill", Status: 1}}
		seedReleasedDependency(t, database, "member-a", "expert", "member-a-v1", "1.0.0", memberRels)
		seedReleasedVersion(t, database, "member-a", "member-a-v2", "2.0.0", memberRels)
		seed(t, database, seedPlugin{id: "team-src", typ: "expert_team", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
		teamRels := []model.PluginRelation{{ID: "t-a", SourcePluginID: "team-src", TargetPluginID: "member-a", Type: "expert_team_expert", TargetPluginType: "expert", Status: 1, Data: json.RawMessage(`{"member_key":"a","is_leader":true}`)}}
		req := newRequest("team-src", "1.0.0")
		if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Team"}`, `{"attachments":[]}`, teamRels)); err != nil {
			t.Fatal(err)
		}
		setPublishedVersion(t, database, "member-a", "member-a-v2")
		if _, err := approvePending(t, repo, req.ID); !errors.Is(err, pluginrepo.ErrDependencyChanged) {
			t.Fatalf("approve = %v, want ErrDependencyChanged", err)
		}
	})

	t.Run("changed connector effective", func(t *testing.T) {
		database, repo := reviewObservationRepo(t)
		ctx := context.Background()
		seedConnectorWithEffective(t, database, "connector-c", "published", 1, "sha256:111111111111111111111111111111111111111111111111111111111111111")
		seed(t, database, seedPlugin{id: "expert-src", typ: "expert", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
		rels := []model.PluginRelation{{ID: "r1", SourcePluginID: "expert-src", TargetPluginID: "connector-c", Type: "expert_connector", TargetPluginType: "connector", Status: 1}}
		req := newRequest("expert-src", "1.0.0")
		if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Fixture"}`, `{"attachments":[]}`, rels)); err != nil {
			t.Fatal(err)
		}
		bumpConnectorEffective(t, database, "connector-c", 2, "sha256:222222222222222222222222222222222222222222222222222222222222222")
		if _, err := approvePending(t, repo, req.ID); !errors.Is(err, pluginrepo.ErrDependencyChanged) {
			t.Fatalf("approve = %v, want ErrDependencyChanged", err)
		}
	})

	t.Run("unchanged graph approves", func(t *testing.T) {
		database, repo := reviewObservationRepo(t)
		ctx := context.Background()
		seedReleasedDependency(t, database, "skill-a", "skill", "skill-a-v1", "1.0.0", nil)
		seed(t, database, seedPlugin{id: "expert-src", typ: "expert", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
		rels := []model.PluginRelation{{ID: "r1", SourcePluginID: "expert-src", TargetPluginID: "skill-a", Type: "expert_skill", TargetPluginType: "skill", Status: 1}}
		req := newRequest("expert-src", "1.0.0")
		if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Fixture"}`, `{"attachments":[]}`, rels)); err != nil {
			t.Fatal(err)
		}
		if _, err := approvePending(t, repo, req.ID); err != nil {
			t.Fatalf("approve with unchanged observations: %v", err)
		}
		var status, listing string
		if err := database.QueryRow(`SELECT status FROM plugin_review_requests WHERE review_id=?`, req.ID).Scan(&status); err != nil || status != "approved" {
			t.Fatalf("status=%q err=%v", status, err)
		}
		if err := database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id='expert-src'`).Scan(&listing); err != nil || listing != "published" {
			t.Fatalf("listing=%q err=%v", listing, err)
		}
	})

	t.Run("cross space approve stays hidden", func(t *testing.T) {
		database, repo := reviewObservationRepo(t)
		ctx := context.Background()
		seedReleasedDependency(t, database, "skill-a", "skill", "skill-a-v1", "1.0.0", nil)
		seed(t, database, seedPlugin{id: "expert-src", typ: "expert", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
		rels := []model.PluginRelation{{ID: "r1", SourcePluginID: "expert-src", TargetPluginID: "skill-a", Type: "expert_skill", TargetPluginType: "skill", Status: 1}}
		req := newRequest("expert-src", "1.0.0")
		if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Fixture"}`, `{"attachments":[]}`, rels)); err != nil {
			t.Fatal(err)
		}
		other := pluginrepo.Scope{CallerUID: "admin-2", SpaceID: "space-b"}
		if _, err := repo.ApproveReview(ctx, other, pluginrepo.ApproveReviewParams{ReviewID: req.ID, ReviewerUID: "admin-2"}); !errors.Is(err, pluginrepo.ErrNotFound) {
			t.Fatalf("cross-Space approve = %v, want ErrNotFound", err)
		}
	})
}

// A Connector's own review observes its effective baseline; editing changes
// alone must not misjudge, while a new effective revision does.
func TestConnectorReviewObservesEffectiveBaseline(t *testing.T) {
	t.Run("first submit may have no effective", func(t *testing.T) {
		database, repo := reviewObservationRepo(t)
		ctx := context.Background()
		seed(t, database, seedPlugin{id: "connector-new", typ: "connector", visibility: "space", listingState: "draft", currentVersion: "1.0.0"})
		req := newRequest("connector-new", "1.0.0")
		if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Connector"}`, `{"attachments":[]}`, nil)); err != nil {
			t.Fatal(err)
		}
		deps, conns, present := storedReviewObservations(t, database, req.ID)
		if !present || len(deps) != 0 || len(conns) != 1 {
			t.Fatalf("observations present=%v deps=%+v conns=%+v", present, deps, conns)
		}
		if conns[0].PluginID != "connector-new" || conns[0].EffectiveRevision != nil || conns[0].EffectiveDigest != nil {
			t.Fatalf("first-submit baseline = %+v, want null/null", conns[0])
		}
		if _, err := approvePending(t, repo, req.ID); err != nil {
			t.Fatalf("first connector approval: %v", err)
		}
		var revision uint64
		if err := database.QueryRow(`SELECT effective_revision FROM plugin_connector_effective WHERE plugin_id='connector-new'`).Scan(&revision); err != nil || revision != 1 {
			t.Fatalf("effective revision=%d err=%v, want 1", revision, err)
		}
	})

	t.Run("editing change alone does not misjudge", func(t *testing.T) {
		database, repo := reviewObservationRepo(t)
		ctx := context.Background()
		seedConnectorWithEffective(t, database, "connector-c", "draft", 7, "sha256:777777777777777777777777777777777777777777777777777777777777777")
		req := newRequest("connector-c", "1.0.0")
		if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Connector"}`, `{"attachments":[]}`, nil)); err != nil {
			t.Fatal(err)
		}
		// A newer editing save only (no new effective row) must not be treated as
		// dependency drift.
		if _, err := database.Exec(`UPDATE plugins SET plugin_name='edited draft',manifest_json=JSON_OBJECT('plugin_name','edited draft') WHERE plugin_id='connector-c'`); err != nil {
			t.Fatal(err)
		}
		if _, err := approvePending(t, repo, req.ID); err != nil {
			t.Fatalf("approve after editing-only change: %v", err)
		}
	})

	t.Run("new effective revision blocks approval", func(t *testing.T) {
		database, repo := reviewObservationRepo(t)
		ctx := context.Background()
		seedConnectorWithEffective(t, database, "connector-c", "draft", 7, "sha256:777777777777777777777777777777777777777777777777777777777777777")
		req := newRequest("connector-c", "1.0.0")
		if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Connector"}`, `{"attachments":[]}`, nil)); err != nil {
			t.Fatal(err)
		}
		bumpConnectorEffective(t, database, "connector-c", 8, "sha256:888888888888888888888888888888888888888888888888888888888888888")
		if _, err := approvePending(t, repo, req.ID); !errors.Is(err, pluginrepo.ErrDependencyChanged) {
			t.Fatalf("approve = %v, want ErrDependencyChanged", err)
		}
		var status string
		if err := database.QueryRow(`SELECT status FROM plugin_review_requests WHERE review_id=?`, req.ID).Scan(&status); err != nil || status != "pending" {
			t.Fatalf("status=%q err=%v, want pending", status, err)
		}
	})
}

// --- automatic approval retry ------------------------------------------------

// racingReviewRepo lets a test mutate dependency state between the automatic
// review's submit-time insert and its approve attempt, which is the exact race
// A15's single internal retry exists for.
type racingReviewRepo struct {
	*pluginrepo.Repo
	afterInsert  func()
	afterRefresh func()
}

func (r *racingReviewRepo) InsertReviewRequest(ctx context.Context, scope pluginrepo.Scope, req *model.PluginReviewRequest, snap pluginrepo.FrozenSnapshot) error {
	if err := r.Repo.InsertReviewRequest(ctx, scope, req, snap); err != nil {
		return err
	}
	if r.afterInsert != nil {
		r.afterInsert()
	}
	return nil
}

func (r *racingReviewRepo) RefreshReviewObservations(ctx context.Context, scope pluginrepo.Scope, reviewID string) error {
	if err := r.Repo.RefreshReviewObservations(ctx, scope, reviewID); err != nil {
		return err
	}
	if r.afterRefresh != nil {
		r.afterRefresh()
	}
	return nil
}

func autoReviewFixture(t *testing.T, afterInsert, afterRefresh func()) (*sql.DB, *racingReviewRepo, *pluginsvc.Service) {
	t.Helper()
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	racing := &racingReviewRepo{Repo: repo, afterInsert: afterInsert, afterRefresh: afterRefresh}
	svc := pluginsvc.New(racing).WithVersionManagement(true).WithReferences(true)
	if _, err := repo.UpsertReviewPolicy(context.Background(), tenantScope(), true, "reviewer", "Reviewer"); err != nil {
		t.Fatalf("auto policy: %v", err)
	}
	return database, racing, svc
}

func seedAutoReviewExpert(t *testing.T, database *sql.DB) {
	t.Helper()
	seedReleasedDependency(t, database, "skill-a", "skill", "skill-a-v1", "1.0.0", nil)
	seedReleasedVersion(t, database, "skill-a", "skill-a-v2", "2.0.0", nil)
	seedReleasedVersion(t, database, "skill-a", "skill-a-v3", "3.0.0", nil)
	seed(t, database, seedPlugin{id: "expert-src", typ: "expert", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	seedRelation(t, database, "expert-skill", "expert-src", "skill-a", "expert_skill")
}

// The automatic path must re-resolve once instead of surfacing a routine 409
// when a dependency published a new release between submit and approve.
func TestAutoApproveRetriesOnceAfterDependencyChange(t *testing.T) {
	var database *sql.DB
	database, _, svc := autoReviewFixture(t, func() {
		// The dependency moved between the insert and the policy decision.
		setPublishedVersion(t, database, "skill-a", "skill-a-v2")
	}, nil)
	seedAutoReviewExpert(t, database)

	review, err := svc.SubmitReview(context.Background(), managedOwner(), pluginsvc.ReviewSubmitParams{PluginID: "expert-src", Version: "1.0.0"})
	if err != nil {
		t.Fatalf("SubmitReview with auto retry: %v", err)
	}
	if review.Status != model.ReviewStatusApproved {
		t.Fatalf("status = %q, want approved after retry", review.Status)
	}
	deps, _, present := storedReviewObservations(t, database, review.ID)
	if !present || len(deps) != 1 || deps[0].VersionID != "skill-a-v2" {
		t.Fatalf("refreshed observations = %+v present=%v, want skill-a-v2", deps, present)
	}
	var listing string
	if err := database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id='expert-src'`).Scan(&listing); err != nil || listing != "published" {
		t.Fatalf("listing=%q err=%v, want published", listing, err)
	}
}

// The retry is exactly one attempt: if the graph moves again between refresh
// and the retried approve, the request must not be silently approved.
func TestAutoApproveRetryExhaustionReturnsDependencyChanged(t *testing.T) {
	var database *sql.DB
	database, _, svc := autoReviewFixture(t, func() {
		setPublishedVersion(t, database, "skill-a", "skill-a-v2")
	}, func() {
		setPublishedVersion(t, database, "skill-a", "skill-a-v3")
	})
	seedAutoReviewExpert(t, database)

	_, err := svc.SubmitReview(context.Background(), managedOwner(), pluginsvc.ReviewSubmitParams{PluginID: "expert-src", Version: "1.0.0"})
	if !errors.Is(err, pluginsvc.ErrDependencyChanged) {
		t.Fatalf("SubmitReview = %v, want ErrDependencyChanged", err)
	}
	var status, listing string
	if err := database.QueryRow(`SELECT status FROM plugin_review_requests WHERE plugin_id='expert-src' ORDER BY submitted_at DESC LIMIT 1`).Scan(&status); err != nil || status != "canceled" {
		t.Fatalf("review status=%q err=%v, want canceled compensation", status, err)
	}
	if err := database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id='expert-src'`).Scan(&listing); err != nil || listing != "draft" {
		t.Fatalf("listing=%q err=%v, want draft (nothing published)", listing, err)
	}
}

// Manual approval surfaces the conflict through the service boundary so the
// handler can answer 409 dependency_changed; the request stays pending.
func TestManualApproveMapsDependencyChanged(t *testing.T) {
	database, repo := reviewObservationRepo(t)
	ctx := context.Background()
	seedReleasedDependency(t, database, "skill-a", "skill", "skill-a-v1", "1.0.0", nil)
	seedReleasedVersion(t, database, "skill-a", "skill-a-v2", "2.0.0", nil)
	seed(t, database, seedPlugin{id: "expert-src", typ: "expert", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	rels := []model.PluginRelation{{ID: "r1", SourcePluginID: "expert-src", TargetPluginID: "skill-a", Type: "expert_skill", TargetPluginType: "skill", Status: 1}}
	req := newRequest("expert-src", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Fixture"}`, `{"attachments":[]}`, rels)); err != nil {
		t.Fatal(err)
	}
	setPublishedVersion(t, database, "skill-a", "skill-a-v2")

	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	reviewer := managedOwner()
	reviewer.UID, reviewer.Name, reviewer.SpaceRole = "admin-1", "Adam", pluginsvc.SpaceRoleAdmin
	if _, err := svc.ApproveReview(ctx, reviewer, req.ID); !errors.Is(err, pluginsvc.ErrDependencyChanged) {
		t.Fatalf("ApproveReview = %v, want ErrDependencyChanged", err)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM plugin_review_requests WHERE review_id=?`, req.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("status=%q err=%v, want pending", status, err)
	}
}
