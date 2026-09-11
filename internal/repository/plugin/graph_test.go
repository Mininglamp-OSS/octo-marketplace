package plugin

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// visibilityPredicateRE matches the caller-scoped visibility predicate
// literally. Graph query expectations embed it rather than `.*` so a test fails
// if an edge or node query ever stops filtering by caller visibility — the
// mock returns whatever rows it is given, so only the predicate text itself
// proves the filtering is in the SQL.
const visibilityPredicateRE = `\(p\.visibility IN \('public','system'\) OR \(p\.space_id = \? AND \(\(p\.visibility = 'space' AND p\.listing_state = 'published'\) OR p\.owner_uid = \?\)\)\)`

// Edge-query expectations end with these tails so the server-side LIMIT is
// pinned too. The limit is derived from maxGraphEdges rather than hardcoded:
// the two must never desynchronise, because a LIMIT at or below the cap would
// silently truncate an exact overflow into a successful partial response
// instead of the 413 the fail-closed design promises. Deleting either LIMIT
// clause, or splicing any value other than maxGraphEdges+1, fails these.
var (
	edgeLimitRE = ` LIMIT ` + strconv.Itoa(maxGraphEdges+1)
	l1TailRE    = ` ORDER BY r.sort_order,r.relation_id` + edgeLimitRE
	l2TailRE    = ` ORDER BY r.source_plugin_id,r.sort_order,r.relation_id` + edgeLimitRE
)

func graphRootTestColumns() []string {
	return append(pluginTestColumns(), "view_count", "install_count", "download_count", "has_pending_review", "latest_review_id", "latest_review_status")
}

// graphNodeTestColumns includes full package data, metrics, and owner-only
// review state, matching the installable related-node projection.
func graphNodeTestColumns() []string {
	return graphRootTestColumns()
}

// Expected bind order is independent of the production argument helper.
func graphReviewTestArgs(scope Scope, args ...driver.Value) []driver.Value {
	return append([]driver.Value{scope.CallerUID, scope.SpaceID, scope.CallerUID, scope.SpaceID, scope.CallerUID, scope.SpaceID}, args...)
}

func graphEdgeTestColumns() []string {
	return []string{"relation_id", "source_plugin_id", "target_plugin_id", "plugin_type", "relation_type", "sort_order", "relation_json", "status", "created_by", "created_at", "updated_at", "deleted_at"}
}

func TestAddGraphPayloadBytesEnforcesAggregateLimit(t *testing.T) {
	current, ok := addGraphPayloadBytes(maxGraphPayloadBytes-3, []byte("ab"))
	if !ok || current != maxGraphPayloadBytes-1 {
		t.Fatalf("within-limit charge = (%d, %v)", current, ok)
	}
	if _, ok := addGraphPayloadBytes(current, []byte("cd")); ok {
		t.Fatal("expected aggregate payload overflow")
	}
}

func TestGraphEdgesDrainEnforcesPayloadLimitBeforeMaterializing(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT`).WillReturnRows(sqlmock.NewRows(graphEdgeTestColumns()).
		AddRow("rel-1", "expert-1", "skill-1", model.PluginTypeSkill, "expert_skill", 0, []byte(`{}`), 1, "owner", now, now, nil))
	rows, err := db.Query("SELECT graph edges")
	if err != nil {
		t.Fatal(err)
	}
	edges := newGraphEdges(maxGraphPayloadBytes - 1)
	if err := edges.drain(rows); !errors.Is(err, ErrGraphTooLarge) {
		t.Fatalf("drain error = %v, want ErrGraphTooLarge", err)
	}
	if len(edges.rels) != 0 {
		t.Fatalf("materialized %d relations after payload cap", len(edges.rels))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetGraphClosure_ExpertRoot_OneHop(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	// Query 1: root
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL AND \(p.visibility IN`).
		WithArgs(graphReviewTestArgs(scope, "expert-1", "space", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphRootTestColumns()).
			AddRow("expert-1", "Expert 1", model.PluginTypeExpert, 0, nil, []byte(`[]`), "pub", "caller", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, true, "root-review", model.ReviewStatusPending))
	// Query 2: level-1 edges, filtered by the caller's visibility predicate.
	mock.ExpectQuery(`SELECT .* FROM plugin_relations r JOIN plugins p ON p.plugin_id=r.target_plugin_id WHERE r.source_plugin_id=\? AND r.status=1 AND r.deleted_at IS NULL AND p.status=1 AND p.deleted_at IS NULL AND `+visibilityPredicateRE+l1TailRE).
		WithArgs("expert-1", "space", "caller").
		WillReturnRows(sqlmock.NewRows(graphEdgeTestColumns()).
			AddRow("rel-1", "expert-1", "skill-1", model.PluginTypeSkill, "expert_skill", 0, []byte(`{"source_index":0}`), 1, "owner", now, now, nil))
	// Query 3: full node payloads, same visibility predicate.
	mock.ExpectQuery(`SELECT .*p.plugin_id IN \(\?\) AND ` + visibilityPredicateRE).
		WithArgs(graphReviewTestArgs(scope, "skill-1", "space", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphNodeTestColumns()).
			AddRow("skill-1", "Skill 1", model.PluginTypeSkill, 0, nil, []byte(`[]`), "", "caller", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{"attachments":[{"path":"SKILL.md","raw_content":"skill"}]}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, true, "child-review", model.ReviewStatusPending))

	root, rels, nodes, err := r.GetGraphClosure(context.Background(), scope, "expert-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root == nil || root.ID != "expert-1" {
		t.Fatalf("root mismatch: %+v", root)
	}
	if len(rels) != 1 || rels[0].TargetPluginID != "skill-1" {
		t.Fatalf("rels = %+v", rels)
	}
	if len(nodes) != 1 || nodes[0].ID != "skill-1" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if !root.HasPendingReview || root.LatestReviewID != "root-review" || root.LatestReviewStatus != model.ReviewStatusPending {
		t.Fatalf("root review state = %+v", root)
	}
	if !nodes[0].HasPendingReview || nodes[0].LatestReviewID != "child-review" || nodes[0].LatestReviewStatus != model.ReviewStatusPending {
		t.Fatalf("child review state = %+v", nodes[0])
	}
	if !strings.Contains(string(nodes[0].Package), `"raw_content":"skill"`) {
		t.Fatalf("expected related node to carry plugin_json, got %s", nodes[0].Package)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestGetGraphClosure_LeafRoot_NoEdgeQueriesBeyondRoot(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\?`).
		WithArgs(graphReviewTestArgs(scope, "skill-1", "space", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphRootTestColumns()).
			AddRow("skill-1", "Skill 1", model.PluginTypeSkill, 0, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))

	root, rels, nodes, err := r.GetGraphClosure(context.Background(), scope, "skill-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.ID != "skill-1" {
		t.Fatalf("root = %+v", root)
	}
	if len(rels) != 0 || len(nodes) != 0 {
		t.Fatalf("expected no relations/nodes for leaf root, got rels=%d nodes=%d", len(rels), len(nodes))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestGetGraphClosure_ExpertTeam_TwoHops_DedupesShared(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\?`).
		WithArgs(graphReviewTestArgs(scope, "team-1", "space", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphRootTestColumns()).
			AddRow("team-1", "Team", model.PluginTypeExpertTeam, 0, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))
	// L1: two embedded members
	mock.ExpectQuery(`FROM plugin_relations r JOIN plugins p ON p.plugin_id=r.target_plugin_id WHERE r.source_plugin_id=\? AND .*`+visibilityPredicateRE+l1TailRE).
		WithArgs("team-1", "space", "caller").
		WillReturnRows(sqlmock.NewRows(graphEdgeTestColumns()).
			AddRow("rel-m1", "team-1", "m1", model.PluginTypeExpert, "expert_team_expert", 0, []byte(`{"member_key":"a","role":"member","is_leader":false,"source_index":0}`), 1, "owner", now, now, nil).
			AddRow("rel-m2", "team-1", "m2", model.PluginTypeExpert, "expert_team_expert", 1, []byte(`{"member_key":"b","role":"member","is_leader":true,"source_index":1}`), 1, "owner", now, now, nil))
	// L2 edges from m1 and m2 — m2 shares the same skill as m1
	mock.ExpectQuery(`r.source_plugin_id IN \(\?,\?\).*`+visibilityPredicateRE+l2TailRE).
		WithArgs("m1", "m2", "space", "caller").
		WillReturnRows(sqlmock.NewRows(graphEdgeTestColumns()).
			AddRow("rel-s1", "m1", "s1", model.PluginTypeSkill, "expert_skill", 0, []byte(`{"source_index":0}`), 1, "owner", now, now, nil).
			AddRow("rel-s2a", "m1", "s2", model.PluginTypeSkill, "expert_skill", 1, []byte(`{"source_index":1}`), 1, "owner", now, now, nil).
			AddRow("rel-s2b", "m2", "s2", model.PluginTypeSkill, "expert_skill", 0, []byte(`{"source_index":0}`), 1, "owner", now, now, nil))
	// Node payloads: m1, m2, s1, s2 (deduped); one visibility bind pair, no
	// separate embedded whitelist.
	mock.ExpectQuery(`SELECT .*p.plugin_id IN \(\?,\?,\?,\?\) AND ` + visibilityPredicateRE).
		WithArgs(graphReviewTestArgs(scope, "m1", "m2", "s1", "s2", "space", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphNodeTestColumns()).
			AddRow("m1", "Member 1", model.PluginTypeExpert, 1, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{"attachments":[{"path":"AGENTS.md","raw_content":"m1"}]}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil).
			AddRow("m2", "Member 2", model.PluginTypeExpert, 1, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{"attachments":[{"path":"AGENTS.md","raw_content":"m2"}]}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil).
			AddRow("s1", "Skill 1", model.PluginTypeSkill, 1, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{"attachments":[{"path":"SKILL.md","raw_content":"s1"}]}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil).
			AddRow("s2", "Skill 2", model.PluginTypeSkill, 1, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySystem, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{"attachments":[{"path":"SKILL.md","raw_content":"s2"}]}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))

	root, rels, nodes, err := r.GetGraphClosure(context.Background(), scope, "team-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if root.ID != "team-1" || root.Type != model.PluginTypeExpertTeam {
		t.Fatalf("root: %+v", root)
	}
	// 2 member edges + 3 skill edges = 5
	if len(rels) != 5 {
		t.Fatalf("want 5 rels, got %d: %+v", len(rels), rels)
	}
	// Deduped nodes: m1, m2, s1, s2 = 4
	if len(nodes) != 4 {
		t.Fatalf("want 4 nodes, got %d: %+v", len(nodes), nodes)
	}
	// Round-trip edge data for the is_leader=true edge.
	var foundLeader bool
	for _, rel := range rels {
		if rel.TargetPluginID == "m2" && rel.Type == "expert_team_expert" {
			var data map[string]any
			if err := json.Unmarshal(rel.Data, &data); err != nil {
				t.Fatalf("leader edge data unmarshal: %v", err)
			}
			if v, _ := data["is_leader"].(bool); v {
				foundLeader = true
			}
		}
	}
	if !foundLeader {
		t.Fatal("is_leader=true edge data lost")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestGetGraphClosure_HiddenStandaloneChild_Omitted(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space-a"}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\?`).
		WithArgs(graphReviewTestArgs(scope, "team-1", "space-a", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphRootTestColumns()).
			AddRow("team-1", "Team", model.PluginTypeExpertTeam, 0, nil, []byte(`[]`), "", "owner", "space-a", model.PluginVisibilitySpace, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))
	// The cross-space member is filtered out by the visibility predicate, which
	// the expectation below pins literally — the team declares two members and
	// only the visible one comes back.
	mock.ExpectQuery(`FROM plugin_relations r JOIN plugins p ON p.plugin_id=r.target_plugin_id WHERE r.source_plugin_id=\? AND .*`+visibilityPredicateRE+l1TailRE).
		WithArgs("team-1", "space-a", "caller").
		WillReturnRows(sqlmock.NewRows(graphEdgeTestColumns()).
			AddRow("rel-m1", "team-1", "m1", model.PluginTypeExpert, "expert_team_expert", 0, nil, 1, "owner", now, now, nil))
	mock.ExpectQuery(`r.source_plugin_id IN \(\?\).*`+visibilityPredicateRE+l2TailRE).
		WithArgs("m1", "space-a", "caller").
		WillReturnRows(sqlmock.NewRows(graphEdgeTestColumns())) // no skills returned
	mock.ExpectQuery(`p.plugin_id IN \(\?\) AND ` + visibilityPredicateRE).
		WithArgs(graphReviewTestArgs(scope, "m1", "space-a", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphNodeTestColumns()).
			AddRow("m1", "Member", model.PluginTypeExpert, 1, nil, []byte(`[]`), "", "owner", "space-a", model.PluginVisibilitySpace, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))

	_, rels, nodes, err := r.GetGraphClosure(context.Background(), scope, "team-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(rels) != 1 || rels[0].TargetPluginID != "m1" {
		t.Fatalf("expected only visible m1 edge, got %+v", rels)
	}
	if len(nodes) != 1 || nodes[0].ID != "m1" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// Embedded children get no visibility relaxation: the node payload query binds
// exactly one visibility pair and no embedded-ID whitelist, so an embedded row
// the caller cannot see is dropped like any other. This pins the predicate
// parity with GetWithRelations that /plugins/detail relies on.
func TestGetGraphClosure_EmbeddedChild_StillVisibilityFiltered(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space-a"}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\?`).
		WithArgs(graphReviewTestArgs(scope, "expert-1", "space-a", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphRootTestColumns()).
			AddRow("expert-1", "Expert", model.PluginTypeExpert, 0, nil, []byte(`[]`), "", "owner", "space-a", model.PluginVisibilitySpace, model.PluginListingStatePublished, "C", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))
	mock.ExpectQuery(`WHERE r.source_plugin_id=\? AND .*`+visibilityPredicateRE+l1TailRE).
		WithArgs("expert-1", "space-a", "caller").
		WillReturnRows(sqlmock.NewRows(graphEdgeTestColumns()).
			AddRow("rel-s1", "expert-1", "s1", model.PluginTypeSkill, "expert_skill", 0, nil, 1, "owner", now, now, nil))
	// Exactly three binds: the target ID plus one (space, caller) pair. A
	// relaxed embedded branch would add either an extra space bind or an
	// embedded-ID list here.
	mock.ExpectQuery(`p.plugin_id IN \(\?\) AND ` + visibilityPredicateRE + `$`).
		WithArgs(graphReviewTestArgs(scope, "s1", "space-a", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphNodeTestColumns()))

	_, rels, nodes, err := r.GetGraphClosure(context.Background(), scope, "expert-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected the invisible embedded child to be dropped, got %+v", nodes)
	}
	if len(rels) != 0 {
		t.Fatalf("expected the edge to the dropped child to be removed, got %+v", rels)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestGetGraphClosure_Admin_SeesCrossSpaceStandalone(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "admin", SpaceID: "", Admin: true}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	// Admin root query has NO visibility predicate.
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\? AND p.status=1 AND p.deleted_at IS NULL$`).
		WithArgs(graphReviewTestArgs(scope, "team-1")...).
		WillReturnRows(sqlmock.NewRows(graphRootTestColumns()).
			AddRow("team-1", "Team", model.PluginTypeExpertTeam, 0, nil, []byte(`[]`), "", "owner", "space-b", model.PluginVisibilitySpace, model.PluginListingStatePublished, "Creator", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))
	// Admin edge predicate is 1=1 (no visibility/space bind).
	mock.ExpectQuery(`WHERE r.source_plugin_id=\? AND r.status=1 AND r.deleted_at IS NULL AND p.status=1 AND p.deleted_at IS NULL AND 1=1` + l1TailRE).
		WithArgs("team-1").
		WillReturnRows(sqlmock.NewRows(graphEdgeTestColumns()).
			AddRow("rel-m1", "team-1", "m1", model.PluginTypeExpert, "expert_team_expert", 0, nil, 1, "owner", now, now, nil))
	mock.ExpectQuery(`r.source_plugin_id IN \(\?\).*AND 1=1` + l2TailRE).
		WithArgs("m1").
		WillReturnRows(sqlmock.NewRows(graphEdgeTestColumns()))
	// Admin node WHERE is 1=1 (no visibility).
	mock.ExpectQuery(`p.plugin_id IN \(\?\).*AND 1=1`).
		WithArgs(graphReviewTestArgs(scope, "m1")...).
		WillReturnRows(sqlmock.NewRows(graphNodeTestColumns()).
			AddRow("m1", "M", model.PluginTypeExpert, 0, nil, []byte(`[]`), "", "owner", "space-b", model.PluginVisibilitySpace, model.PluginListingStatePublished, "C", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))

	root, _, nodes, err := r.GetGraphClosure(context.Background(), scope, "team-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if root.ID != "team-1" || len(nodes) != 1 {
		t.Fatalf("root=%v nodes=%+v", root.ID, nodes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestGetGraphClosure_NodeCapEnforcedBeforeNodeQuery(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\?`).
		WithArgs(graphReviewTestArgs(scope, "expert-1", "space", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphRootTestColumns()).
			AddRow("expert-1", "Expert", model.PluginTypeExpert, 0, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "C", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))
	// maxGraphNodes+1 distinct targets, one edge each: the node cap trips while
	// the edge count is still under maxGraphEdges.
	rows := sqlmock.NewRows(graphEdgeTestColumns())
	for i := 0; i < maxGraphNodes+1; i++ {
		tid := "s" + strconv.Itoa(i)
		rows.AddRow("r-"+tid, "expert-1", tid, model.PluginTypeSkill, "expert_skill", i, nil, 1, "owner", now, now, nil)
	}
	mock.ExpectQuery(`FROM plugin_relations r JOIN plugins p ON p.plugin_id=r.target_plugin_id WHERE r.source_plugin_id=\? AND .*`+visibilityPredicateRE+l1TailRE).
		WithArgs("expert-1", "space", "caller").
		WillReturnRows(rows)

	// No node-payload query is expected: the cap fires first.
	_, _, _, err := r.GetGraphClosure(context.Background(), scope, "expert-1")
	if !errors.Is(err, ErrGraphTooLarge) {
		t.Fatalf("want ErrGraphTooLarge, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// A wide-but-shallow graph — many members sharing a small set of skills — keeps
// the unique-node count far below maxGraphNodes while the edge count explodes.
// The node cap alone does not catch this shape; the edge cap must.
func TestGetGraphClosure_EdgeCapCatchesWideShallowGraph(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	const (
		members         = 20
		skillsPerMember = 101
	)
	// members + skillsPerMember unique nodes (skills are shared across every
	// member), vs members*skillsPerMember + members edges.
	if uniqueNodes := members + skillsPerMember; uniqueNodes > maxGraphNodes {
		t.Fatalf("test graph must stay under the node cap, got %d nodes", uniqueNodes)
	}
	if totalEdges := members + members*skillsPerMember; totalEdges <= maxGraphEdges {
		t.Fatalf("test graph must exceed the edge cap, got %d edges", totalEdges)
	}

	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\?`).
		WithArgs(graphReviewTestArgs(scope, "team-1", "space", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphRootTestColumns()).
			AddRow("team-1", "Team", model.PluginTypeExpertTeam, 0, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "C", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))

	l1 := sqlmock.NewRows(graphEdgeTestColumns())
	for i := 0; i < members; i++ {
		mid := "m" + strconv.Itoa(i)
		l1.AddRow("rel-"+mid, "team-1", mid, model.PluginTypeExpert, "expert_team_expert", i, nil, 1, "owner", now, now, nil)
	}
	mock.ExpectQuery(`WHERE r.source_plugin_id=\? AND .*`+visibilityPredicateRE+l1TailRE).
		WithArgs("team-1", "space", "caller").
		WillReturnRows(l1)

	l2 := sqlmock.NewRows(graphEdgeTestColumns())
	for i := 0; i < members; i++ {
		mid := "m" + strconv.Itoa(i)
		for j := 0; j < skillsPerMember; j++ {
			sid := "shared-s" + strconv.Itoa(j) // shared by every member
			l2.AddRow("rel-"+mid+"-"+sid, mid, sid, model.PluginTypeSkill, "expert_skill", j, nil, 1, "owner", now, now, nil)
		}
	}
	mock.ExpectQuery(`r.source_plugin_id IN \(.*` + visibilityPredicateRE + l2TailRE).
		WillReturnRows(l2)

	// No node-payload query is expected: the edge cap fires mid-scan.
	_, _, _, err := r.GetGraphClosure(context.Background(), scope, "team-1")
	if !errors.Is(err, ErrGraphTooLarge) {
		t.Fatalf("want ErrGraphTooLarge for a wide-but-shallow graph, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// A maximum-size legal container import — containerMaxMembers members, each
// declaring containerMaxSkills skills under distinct (file,name) pairs, so no
// embedded skill node is shared — must render, not 413. This is the shape the
// endpoint exists to serve, and the caps are chosen to clear it; the assertion
// below fails if either cap is ever lowered under the import ceiling.
func TestGetGraphClosure_MaxContainerImport_Renders(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	const (
		members         = containerImportMaxMembers
		skillsPerMember = containerImportMaxSkillsPerMember
	)
	nodeCount := members + members*skillsPerMember
	edgeCount := nodeCount // every embedded child has exactly one parent

	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\?`).
		WithArgs(graphReviewTestArgs(scope, "team-1", "space", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphRootTestColumns()).
			AddRow("team-1", "Team", model.PluginTypeExpertTeam, 0, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "C", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))

	summaryRow := func(rows *sqlmock.Rows, id string, typ model.PluginType) {
		rows.AddRow(id, id, typ, 1, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "C", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil)
	}
	l1 := sqlmock.NewRows(graphEdgeTestColumns())
	nodeRows := sqlmock.NewRows(graphNodeTestColumns())
	for i := 0; i < members; i++ {
		mid := "m" + strconv.Itoa(i)
		l1.AddRow("rel-"+mid, "team-1", mid, model.PluginTypeExpert, "expert_team_expert", i, nil, 1, "owner", now, now, nil)
		summaryRow(nodeRows, mid, model.PluginTypeExpert)
	}
	mock.ExpectQuery(`WHERE r.source_plugin_id=\? AND .*`+visibilityPredicateRE+l1TailRE).
		WithArgs("team-1", "space", "caller").
		WillReturnRows(l1)

	l2 := sqlmock.NewRows(graphEdgeTestColumns())
	for i := 0; i < members; i++ {
		mid := "m" + strconv.Itoa(i)
		for j := 0; j < skillsPerMember; j++ {
			// Embedded skills are per-member copies: distinct (file,name) pairs
			// mint distinct nodes, so nothing dedupes across members.
			sid := mid + "-s" + strconv.Itoa(j)
			l2.AddRow("rel-"+sid, mid, sid, model.PluginTypeSkill, "expert_skill", j, nil, 1, "owner", now, now, nil)
			summaryRow(nodeRows, sid, model.PluginTypeSkill)
		}
	}
	mock.ExpectQuery(`r.source_plugin_id IN \(.*` + visibilityPredicateRE + l2TailRE).WillReturnRows(l2)
	mock.ExpectQuery(`p.plugin_id IN \(.*` + visibilityPredicateRE).WillReturnRows(nodeRows)

	root, rels, nodes, err := r.GetGraphClosure(context.Background(), scope, "team-1")
	if err != nil {
		t.Fatalf("a maximum-size legal container import must render, got %v", err)
	}
	if root.ID != "team-1" {
		t.Fatalf("root = %+v", root)
	}
	if len(rels) != edgeCount {
		t.Fatalf("rels = %d, want %d", len(rels), edgeCount)
	}
	if len(nodes) != nodeCount {
		t.Fatalf("nodes = %d, want %d", len(nodes), nodeCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// When the node-payload query returns fewer rows than the edge closure — a
// target deleted or made invisible between the two queries — both the vanished
// node and every edge touching it (as target or as ancestor) must be dropped.
func TestGetGraphClosure_VanishedNode_DropsItsEdges(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\?`).
		WithArgs(graphReviewTestArgs(scope, "team-1", "space", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphRootTestColumns()).
			AddRow("team-1", "Team", model.PluginTypeExpertTeam, 0, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "C", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))
	mock.ExpectQuery(`WHERE r.source_plugin_id=\? AND .*`+visibilityPredicateRE+l1TailRE).
		WithArgs("team-1", "space", "caller").
		WillReturnRows(sqlmock.NewRows(graphEdgeTestColumns()).
			AddRow("rel-m1", "team-1", "m1", model.PluginTypeExpert, "expert_team_expert", 0, nil, 1, "owner", now, now, nil).
			AddRow("rel-m2", "team-1", "m2", model.PluginTypeExpert, "expert_team_expert", 1, nil, 1, "owner", now, now, nil))
	mock.ExpectQuery(`r.source_plugin_id IN \(\?,\?\).*`+visibilityPredicateRE+l2TailRE).
		WithArgs("m1", "m2", "space", "caller").
		WillReturnRows(sqlmock.NewRows(graphEdgeTestColumns()).
			AddRow("rel-s1", "m1", "s1", model.PluginTypeSkill, "expert_skill", 0, nil, 1, "owner", now, now, nil).
			AddRow("rel-s2", "m2", "s2", model.PluginTypeSkill, "expert_skill", 0, nil, 1, "owner", now, now, nil))
	// The payload query returns only m1 and s1: m2 and s2 vanished between the
	// edge scan and here.
	mock.ExpectQuery(`p.plugin_id IN \(\?,\?,\?,\?\) AND ` + visibilityPredicateRE).
		WithArgs(graphReviewTestArgs(scope, "m1", "m2", "s1", "s2", "space", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphNodeTestColumns()).
			AddRow("m1", "Member 1", model.PluginTypeExpert, 1, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "C", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil).
			AddRow("s1", "Skill 1", model.PluginTypeSkill, 1, nil, []byte(`[]`), "", "owner", "space", model.PluginVisibilitySpace, model.PluginListingStatePublished, "C", "human", nil, nil, "", 0, nil, []byte(`{}`), []byte(`{}`), nil, "mh", "ph", nil, nil, 1, now, now, nil, 0, 0, 0, false, nil, nil))

	_, rels, nodes, err := r.GetGraphClosure(context.Background(), scope, "team-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(nodes) != 2 || nodes[0].ID != "m1" || nodes[1].ID != "s1" {
		t.Fatalf("want nodes [m1 s1], got %+v", nodes)
	}
	// team-1 -> m2 drops (vanished target); m2 -> s2 drops (vanished ancestor).
	if len(rels) != 2 {
		t.Fatalf("want 2 surviving edges, got %d: %+v", len(rels), rels)
	}
	for _, rel := range rels {
		if rel.TargetPluginID == "m2" || rel.SourcePluginID == "m2" {
			t.Fatalf("edge touching the vanished node survived: %+v", rel)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestGetGraphClosure_RootNotFound(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer db.Close()
	r := New(db)
	scope := Scope{CallerUID: "caller", SpaceID: "space"}
	mock.ExpectQuery(`SELECT .* FROM plugins p WHERE p.plugin_id=\?`).
		WithArgs(graphReviewTestArgs(scope, "missing", "space", "caller")...).
		WillReturnRows(sqlmock.NewRows(graphRootTestColumns()))
	_, _, _, err := r.GetGraphClosure(context.Background(), scope, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
