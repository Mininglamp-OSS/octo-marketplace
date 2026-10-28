package db

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/router"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/gin-gonic/gin"
)

// Use production router construction rather than a manually configured Service:
// a passing repository test alone does not prove upload/import/review HTTP wiring.
func skillReleaseRouter(t *testing.T, database *sql.DB, dir string, managed bool, uid, space string) http.Handler {
	t.Helper()
	identity := model.Identity{UID: uid, Name: uid, Spaces: []string{space}, SpaceRoles: map[string]int{space: model.SpaceRoleAdmin}}
	return router.Public(database,
		middleware.NewAuthenticator(false, nil, identity, space),
		middleware.NewAdminAuthenticator(false, nil, identity),
		router.StorageConfig{Driver: "local", LocalDir: dir, BaseURL: "http://localhost", MaxMB: 2,
			PluginVersionManagementEnabled: managed, PluginReferencesEnabled: managed,
			PluginPinnedInstallationEnabled: managed, InstallationHMACKeyID: "test",
			InstallationHMACKeys: map[string]string{"test": "synthetic-release-http-test-key-32-bytes"}},
		nil, nil, router.ParseConfig{WorkerPoolSize: 1}, nil, router.ReviewConfig{})
}

func releaseHTTPRequest(t *testing.T, h http.Handler, method, path string, body any, status int) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != status {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, rec.Code, status, rec.Body)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func importReleaseSkill(t *testing.T, h http.Handler) string {
	t.Helper()
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	f, err := w.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("---\nname: release-skill\ndescription: Reviewed release fixture\nversion: 1.0.0\n---\n# Reviewed skill\nApproved instructions.\n")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	out := releaseHTTPRequest(t, h, "POST", "/api/v1/skill_uploads", map[string]any{"file_name": "release-skill.zip", "file_size": archive.Len()}, 200)
	var upload struct {
		ID  string `json:"skill_upload_id"`
		URL string `json:"presigned_url"`
	}
	if err = json.Unmarshal(out["data"], &upload); err != nil || upload.ID == "" {
		t.Fatalf("upload response=%s err=%v", out["data"], err)
	}
	u, err := url.Parse(upload.URL)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PUT", u.RequestURI(), bytes.NewReader(archive.Bytes()))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("ZIP upload: %d %s", rec.Code, rec.Body)
	}
	out = releaseHTTPRequest(t, h, "POST", "/api/v1/skill_uploads/"+upload.ID+"/parse", nil, 200)
	var task struct {
		ID string `json:"skill_parse_task_id"`
	}
	if err = json.Unmarshal(out["data"], &task); err != nil || task.ID == "" {
		t.Fatalf("parse response=%s err=%v", out["data"], err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out = releaseHTTPRequest(t, h, "GET", "/api/v1/skill_parse_tasks/"+task.ID, nil, 200)
		var state struct {
			Status string `json:"status"`
		}
		if err = json.Unmarshal(out["data"], &state); err != nil {
			t.Fatal(err)
		}
		if state.Status == "success" {
			break
		}
		if state.Status == "failed" || time.Now().After(deadline) {
			t.Fatalf("parse did not succeed: %s", out["data"])
		}
		time.Sleep(10 * time.Millisecond)
	}
	out = releaseHTTPRequest(t, h, "POST", "/api/v1/plugins/import", map[string]any{"parse_task_id": task.ID, "visibility": "space"}, 200)
	var imported struct {
		Plugin struct {
			ID string `json:"plugin_id"`
		} `json:"plugin"`
	}
	if err = json.Unmarshal(out["data"], &imported); err != nil || imported.Plugin.ID == "" {
		t.Fatalf("import response=%s err=%v", out["data"], err)
	}
	return imported.Plugin.ID
}

func TestSkillReviewHTTPAlwaysCreatesReleasedSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, managed := range []bool{false, true} {
		for _, auto := range []bool{false, true} {
			t.Run(fmt.Sprintf("managed=%t/auto=%t", managed, auto), func(t *testing.T) {
				database := reviewDB(t)
				dir := t.TempDir()
				owner := skillReleaseRouter(t, database, dir, managed, "user-1", "space-a")
				reviewer := skillReleaseRouter(t, database, dir, managed, "reviewer", "space-a")
				releaseHTTPRequest(t, reviewer, "PATCH", "/api/v1/plugin_review_policies", map[string]any{"is_auto_approve_enabled": auto}, 200)
				id := importReleaseSkill(t, owner)
				out := releaseHTTPRequest(t, owner, "POST", "/api/v1/plugins/publish", map[string]any{"plugin_id": id}, 200)
				if !auto {
					var pending struct {
						ReviewID string `json:"review_id"`
					}
					if err := json.Unmarshal(out["data"], &pending); err != nil || pending.ReviewID == "" {
						t.Fatalf("missing review: %s %v", out["data"], err)
					}
					if managed {
						// Approval must release the submission, not a newer editable save.
						draft := managedSkillWrite("2.0.0", "release-skill", model.PluginVisibilitySpace)
						releaseHTTPRequest(t, owner, "POST", "/api/v1/plugins/upsert", map[string]any{
							"plugin": map[string]any{"plugin_id": id, "plugin_name": draft.Name, "plugin_type": draft.Type,
								"visibility": draft.Visibility, "version": draft.Version, "tags": []string{},
								"manifest_json": draft.Manifest, "plugin_json": draft.Package}, "relations": []any{},
						}, 200)
					}
					releaseHTTPRequest(t, reviewer, "POST", "/api/v1/plugins/review_requests/"+pending.ReviewID+"/approve", nil, 200)
				}
				var state, approved string
				var published, released sql.NullString
				var frozenMatch, indexed sql.NullBool
				if err := database.QueryRow(`SELECT p.listing_state,p.published_version_id,r.version_id,rr.status,
v.manifest_hash=rr.manifest_hash AND v.plugin_hash=rr.plugin_hash AND r.version_label=rr.version,
idx.snapshot_digest=SHA2(CAST(v.relations_json AS CHAR CHARACTER SET utf8mb4),256) AND idx.relation_count=(SELECT COUNT(*) FROM plugin_version_relation_refs edge WHERE edge.source_plugin_id=v.plugin_id AND edge.source_version_id=v.version_id)
FROM plugins p LEFT JOIN plugin_version_releases r ON r.plugin_id=p.plugin_id AND r.version_id=p.published_version_id
LEFT JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND v.version_id=r.version_id
LEFT JOIN plugin_version_reference_index_state idx ON idx.source_plugin_id=v.plugin_id AND idx.source_version_id=v.version_id
JOIN plugin_review_requests rr ON rr.plugin_id=p.plugin_id WHERE p.plugin_id=?`, id).Scan(&state, &published, &released, &approved, &frozenMatch, &indexed); err != nil {
					t.Fatal(err)
				}
				if state != "published" || approved != "approved" || !published.Valid || !released.Valid || published.String != released.String || !frozenMatch.Valid || !frozenMatch.Bool || !indexed.Valid || !indexed.Bool {
					t.Fatalf("approval committed without matching indexed release: state=%s review=%s published=%v release=%v frozen_match=%v indexed=%v", state, approved, published, released, frozenMatch, indexed)
				}
				// Readers/reference writers may be newer replicas or start after rollout.
				modern := skillReleaseRouter(t, database, dir, true, "user-1", "space-a")
				out = releaseHTTPRequest(t, modern, "GET", "/api/v1/plugins/versions?plugin_id="+id, nil, 200)
				var versions []struct {
					ID        string `json:"version_id"`
					Published bool   `json:"is_published"`
					Release   *struct {
						Label string `json:"version_label"`
					} `json:"release"`
				}
				if err := json.Unmarshal(out["data"], &versions); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, v := range versions {
					if v.ID == published.String && v.Published && v.Release != nil && v.Release.Label == "1.0.0" {
						found = true
					}
				}
				if !found {
					t.Fatalf("published snapshot missing from versions: %s", out["data"])
				}
				req := managedWrite("1.0.0", "Use reviewed skill", model.PluginVisibilitySpace)
				write := map[string]any{"plugin": map[string]any{"plugin_name": req.Name, "plugin_type": req.Type, "visibility": req.Visibility, "version": req.Version,
					"tags": []string{}, "manifest_json": req.Manifest, "plugin_json": req.Package},
					"relations": []map[string]any{{"relation_type": "expert_skill", "target_plugin_id": id, "target_version_id": published.String}}}
				releaseHTTPRequest(t, modern, "POST", "/api/v1/plugins/upsert", write, 200)
				other := skillReleaseRouter(t, database, dir, true, "other-user", "space-b")
				releaseHTTPRequest(t, other, "GET", "/api/v1/plugins/versions?plugin_id="+id, nil, 404)
				releaseHTTPRequest(t, other, "POST", "/api/v1/plugins/upsert", write, 404)
			})
		}
	}
}

func TestSkillReviewReleaseFailureRollsBackApproval(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			database := reviewDB(t)
			dir := t.TempDir()
			owner := skillReleaseRouter(t, database, dir, managed, "user-1", "space-a")
			reviewer := skillReleaseRouter(t, database, dir, managed, "reviewer", "space-a")
			releaseHTTPRequest(t, reviewer, "PATCH", "/api/v1/plugin_review_policies", map[string]any{"is_auto_approve_enabled": false}, 200)
			id := importReleaseSkill(t, owner)
			out := releaseHTTPRequest(t, owner, "POST", "/api/v1/plugins/publish", map[string]any{"plugin_id": id}, 200)
			var pending struct {
				ID string `json:"review_id"`
			}
			if err := json.Unmarshal(out["data"], &pending); err != nil || pending.ID == "" {
				t.Fatalf("review=%s err=%v", out["data"], err)
			}
			var before int
			if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER reject_test_release BEFORE INSERT ON plugin_version_releases FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='synthetic release persistence failure'`); err != nil {
				t.Fatal(err)
			}
			releaseHTTPRequest(t, reviewer, "POST", "/api/v1/plugins/review_requests/"+pending.ID+"/approve", nil, 500)
			var state, reviewState string
			var published sql.NullString
			if err := database.QueryRow(`SELECT p.listing_state,p.published_version_id,rr.status FROM plugins p JOIN plugin_review_requests rr ON rr.plugin_id=p.plugin_id WHERE p.plugin_id=?`, id).Scan(&state, &published, &reviewState); err != nil {
				t.Fatal(err)
			}
			if state != "draft" || published.Valid || reviewState != "pending" {
				t.Fatalf("partial approval: state=%s published=%v review=%s", state, published, reviewState)
			}
			var after, releases, audits int
			if err := database.QueryRow(`SELECT (SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?),(SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=?),(SELECT COUNT(*) FROM plugin_audit_logs WHERE plugin_id=? AND action='review_approve')`, id, id, id).Scan(&after, &releases, &audits); err != nil {
				t.Fatal(err)
			}
			if after != before || releases != 0 || audits != 0 {
				t.Fatalf("rollback lost: versions=%d/%d releases=%d audits=%d", after, before, releases, audits)
			}
			if _, err := database.Exec(`DROP TRIGGER reject_test_release`); err != nil {
				t.Fatal(err)
			}
			releaseHTTPRequest(t, reviewer, "POST", "/api/v1/plugins/review_requests/"+pending.ID+"/approve", nil, 200)
			var releasedID string
			if err := database.QueryRow(`SELECT r.version_id FROM plugins p JOIN plugin_version_releases r ON r.plugin_id=p.plugin_id AND r.version_id=p.published_version_id WHERE p.plugin_id=?`, id).Scan(&releasedID); err != nil || releasedID == "" {
				t.Fatalf("retry did not record release: id=%q err=%v", releasedID, err)
			}
		})
	}
}

func TestSkillReviewCardApprovalCreatesReleasedSnapshot(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			f := newCardHTTPFixture(t, managed)
			reviewID, pluginID := f.pending(t, "release")
			checkCardResult(t, f.send("92001", "reviewer", "approve", reviewID), "applied", "approved")
			var releasedID string
			if err := f.db.QueryRow(`SELECT r.version_id FROM plugins p JOIN plugin_version_releases r ON r.plugin_id=p.plugin_id AND r.version_id=p.published_version_id WHERE p.plugin_id=? AND p.listing_state='published'`, pluginID).Scan(&releasedID); err != nil || releasedID == "" {
				t.Fatalf("card approval did not record release: id=%q err=%v", releasedID, err)
			}
		})
	}
}
