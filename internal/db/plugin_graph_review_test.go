package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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

// Exercise both HTTP handlers through the real service, repository and MySQL.
// Feeding a pre-enriched root into either fake would miss the original bug.
func TestDetailGraphReviewStateMatchesDetail(t *testing.T) {
	for _, tc := range []struct {
		name, listing, review, display string
	}{
		{"published upgrade pending", "published", "pending", "pending_review"},
		{"draft pending", "draft", "pending", "pending_review"},
		{"draft rejected", "draft", "rejected", "rejected"},
		{"draft canceled", "draft", "canceled", "draft"},
		{"published approved", "published", "approved", "published"},
		{"no review", "draft", "", "draft"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := reviewDB(t)
			repo := pluginrepo.New(database)
			reviews := map[string]string{}
			for _, p := range []seedPlugin{
				{id: "team", typ: "expert_team"},
				{id: "member", typ: "expert", embedded: true},
				{id: "skill", typ: "skill"},
			} {
				p.visibility, p.listingState, p.currentVersion = "space", tc.listing, "1.0.0"
				seed(t, database, p)
				if tc.review == "pending" {
					terminal := seedGraphReview(t, database, repo, p.id, tenantScope(), "canceled")
					// Pending must win even over a more recently submitted terminal review.
					if _, err := database.Exec(`UPDATE plugin_review_requests SET submitted_at=DATE_ADD(NOW(3), INTERVAL 1 DAY) WHERE review_id=?`, terminal); err != nil {
						t.Fatal(err)
					}
				}
				if tc.review != "" {
					reviews[p.id] = seedGraphReview(t, database, repo, p.id, tenantScope(), tc.review)
				}
			}
			seedRelation(t, database, "team-member", "team", "member", "expert_team_expert")
			seedRelation(t, database, "member-skill", "member", "skill", "expert_skill")
			engine := graphReviewEngine(database, "user-1", "space-a")
			graph := graphReviewGET(t, engine, "/api/v1/plugins/detail_graph?plugin_id=team", http.StatusOK)
			detail := graphReviewGET(t, engine, "/api/v1/plugins/detail?plugin_id=team", http.StatusOK)
			if !bytes.Equal(graph["plugin"], detail["plugin"]) {
				t.Fatalf("root projection mismatch:\ngraph: %s\ndetail: %s", graph["plugin"], detail["plugin"])
			}
			assertGraphReviewFields(t, graph["plugin"], tc.display, reviews["team"])
			var related []json.RawMessage
			if err := json.Unmarshal(graph["related_plugins"], &related); err != nil {
				t.Fatal(err)
			}
			if len(related) != 2 {
				t.Fatalf("related count = %d, want both graph levels", len(related))
			}
			for _, raw := range related {
				var node struct {
					ID string `json:"plugin_id"`
				}
				if err := json.Unmarshal(raw, &node); err != nil {
					t.Fatal(err)
				}
				assertGraphReviewFields(t, raw, tc.display, reviews[node.ID])
				childDetail := graphReviewGET(t, engine, "/api/v1/plugins/detail?plugin_id="+node.ID, http.StatusOK)
				assertGraphReviewFields(t, childDetail["plugin"], tc.display, reviews[node.ID])
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				if _, exists := fields["plugin_json"]; !exists {
					t.Fatal("related node is missing the full package")
				}
			}
			// An author-only draft must still be absent to another Space member.
			if tc.listing == "draft" {
				other := graphReviewEngine(database, "other-user", "space-a")
				graphReviewGET(t, other, "/api/v1/plugins/detail_graph?plugin_id=team", http.StatusNotFound)
			}
		})
	}
}

func TestDetailGraphReviewStateIsOwnerAndSpaceScoped(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	for _, p := range []seedPlugin{
		{id: "team", typ: "expert_team", owner: "other-user", space: "space-a"},
		{id: "member", typ: "expert", owner: "user-1", space: "space-a"},
		{id: "other-skill", owner: "other-user", space: "space-a"},
		{id: "foreign-skill", owner: "user-1", space: "space-b"},
		{id: "hidden-skill", owner: "user-1", space: "space-b"},
	} {
		p.visibility, p.listingState, p.currentVersion = "space", "published", "1.0.0"
		seed(t, database, p)
		seedGraphReview(t, database, repo, p.id, pluginrepo.Scope{CallerUID: p.owner, SpaceID: p.space}, "pending")
	}
	// A globally visible row can have the same owner in a different Space.
	// Its review is not part of the current Space's owner-only review surface.
	if _, err := database.Exec(`UPDATE plugins SET visibility='system' WHERE plugin_id='foreign-skill'`); err != nil {
		t.Fatal(err)
	}
	seedRelation(t, database, "team-member", "team", "member", "expert_team_expert")
	for _, id := range []string{"other-skill", "foreign-skill", "hidden-skill"} {
		seedRelation(t, database, "member-"+id, "member", id, "expert_skill")
	}
	engine := graphReviewEngine(database, "user-1", "space-a")
	graph := graphReviewGET(t, engine, "/api/v1/plugins/detail_graph?plugin_id=team", http.StatusOK)
	detail := graphReviewGET(t, engine, "/api/v1/plugins/detail?plugin_id=team", http.StatusOK)
	if !bytes.Equal(graph["plugin"], detail["plugin"]) {
		t.Fatal("non-owner root differs from detail")
	}
	assertGraphReviewFields(t, graph["plugin"], "published", "")
	var nodes []struct {
		ID       string `json:"plugin_id"`
		Display  string `json:"display_status"`
		ReviewID string `json:"review_id"`
	}
	if err := json.Unmarshal(graph["related_plugins"], &nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("visible children = %d, want 3", len(nodes))
	}
	for _, node := range nodes {
		if node.ID == "member" {
			if node.Display != "pending_review" || node.ReviewID == "" {
				t.Errorf("owned child missing review: %+v", node)
			}
		} else if node.ID == "hidden-skill" || node.Display != "published" || node.ReviewID != "" {
			t.Errorf("review or plugin leaked across owner/Space: %+v", node)
		}
	}
	// Check the same-Space guard on the root projection too, not only children.
	foreign := graphReviewGET(t, engine, "/api/v1/plugins/detail_graph?plugin_id=foreign-skill", http.StatusOK)
	assertGraphReviewFields(t, foreign["plugin"], "published", "")
	graphReviewGET(t, engine, "/api/v1/plugins/detail_graph?plugin_id=hidden-skill", http.StatusNotFound)
	// Service.Detail compares owner IDs as exact Go strings. MySQL's default
	// case-insensitive collation must not grant review access to another spelling.
	caseVariant := graphReviewEngine(database, "USER-1", "space-a")
	variantGraph := graphReviewGET(t, caseVariant, "/api/v1/plugins/detail_graph?plugin_id=member", http.StatusOK)
	variantDetail := graphReviewGET(t, caseVariant, "/api/v1/plugins/detail?plugin_id=member", http.StatusOK)
	if !bytes.Equal(variantGraph["plugin"], variantDetail["plugin"]) {
		t.Fatal("owner comparison differs from detail for a case-variant caller ID")
	}
	assertGraphReviewFields(t, variantGraph["plugin"], "published", "")
}

func seedGraphReview(t *testing.T, database *sql.DB, repo *pluginrepo.Repo, pluginID string, scope pluginrepo.Scope, status string) string {
	t.Helper()
	req := newRequest(pluginID, "2.0.0")
	req.SpaceID, req.ApplicantUID = scope.SpaceID, scope.CallerUID
	if err := repo.InsertReviewRequest(context.Background(), scope, req, snapshotOf(`{"plugin_name":"Next"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		if _, err := database.Exec(`UPDATE plugin_review_requests SET status=? WHERE review_id=?`, status, req.ID); err != nil {
			t.Fatal(err)
		}
	}
	return req.ID
}

func graphReviewEngine(database *sql.DB, uid, space string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: uid, Name: "Reader"}, space)
	pluginhandler.New(pluginsvc.New(pluginrepo.New(database))).Register(r.Group("/api/v1", auth.Handler()))
	return r
}

func graphReviewGET(t *testing.T, engine *gin.Engine, path string, status int) map[string]json.RawMessage {
	t.Helper()
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != status {
		t.Fatalf("GET %s: status=%d body=%s", path, rec.Code, rec.Body.String())
	}
	if status != http.StatusOK {
		return nil
	}
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}

func assertGraphReviewFields(t *testing.T, raw json.RawMessage, display, reviewID string) {
	t.Helper()
	var p struct {
		Display  string `json:"display_status"`
		ReviewID string `json:"review_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Display != display || p.ReviewID != reviewID {
		t.Errorf("status/review = %q/%q, want %q/%q", p.Display, p.ReviewID, display, reviewID)
	}
}
