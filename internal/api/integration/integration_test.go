package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/router"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/auth"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// testSetup creates a test router with sqlmock using regexp query matching.
func testSetup(t *testing.T) (*gin.Engine, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	auth := marketmiddleware.NewAuthenticator(false, nil, model.Identity{
		UID:  "user-1",
		Name: "Alice",
	}, "space-1")
	storageCfg := router.StorageConfig{
		Driver:   "local",
		LocalDir: t.TempDir(),
		BaseURL:  "http://localhost:8092",
		MaxMB:    20,
	}
	engine := router.PublicWithDB(db, auth, storageCfg)
	return engine, mock, db
}

func doRequest(engine *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	return doRequestWithHeaders(engine, method, path, body, nil)
}

func doRequestWithHeaders(engine *gin.Engine, method, path string, body interface{}, headers map[string]string) *httptest.ResponseRecorder {
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

func parseBody(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse response body: %v body=%s", err, w.Body.String())
	}
	return result
}

var skillCols = []string{"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
	"description", "category_id", "tags",
	"owner_id", "owner_name", "space_id", "visibility", "version",
	"readme_content", "file_name", "file_url", "file_size", "file_sha256",
	"created_at", "updated_at", "resolved_version", "version_storage", "view_count", "download_count"}

// skillListCols is the column set for List queries (includes version_storage and metrics).
var skillListCols = []string{"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
	"description", "category_id", "tags",
	"owner_id", "owner_name", "space_id", "visibility", "version",
	"readme_content", "file_name", "file_url", "file_size", "file_sha256",
	"created_at", "updated_at", "resolved_version", "version_storage", "view_count", "download_count"}

func skillRow(id, name, ownerID, ownerName, spaceID, visibility string) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows(skillCols).AddRow(
		id, name, name, "", "", "",
		"description", "cat-1", []byte(`[]`),
		ownerID, ownerName, spaceID, visibility, "1.0.0",
		"readme", "file.zip", fmt.Sprintf("skills/%s/v1.0.0/file.zip", id), int64(1024), "sha256",
		now, now, "1.0.0", "", int64(0), int64(0),
	)
}

func skillListRow(id, name, ownerID, ownerName, spaceID, visibility string) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows(skillListCols).AddRow(
		id, name, name, "", "", "",
		"description", "cat-1", []byte(`[]`),
		ownerID, ownerName, spaceID, visibility, "1.0.0",
		"readme", "file.zip", fmt.Sprintf("skills/%s/v1.0.0/file.zip", id), int64(1024), "sha256",
		now, now, "1.0.0", "", int64(0), int64(0),
	)
}

// --- Admin Category CRUD Tests ---

func TestAdminCreateCategory(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectExec("INSERT INTO categories").
		WillReturnResult(sqlmock.NewResult(1, 1))

	w := doRequest(engine, "POST", "/api/v1/skill/admin/categories", map[string]interface{}{
		"name":       "AI Tools",
		"icon_key":   "robot",
		"sort_order": 1,
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusCreated, w.Body.String())
	}
	body := parseBody(t, w)
	data := body["data"].(map[string]interface{})
	if data["name"] != "AI Tools" {
		t.Errorf("name=%v want=AI Tools", data["name"])
	}
}

func TestAdminCreateCategoryMissingName(t *testing.T) {
	engine, _, db := testSetup(t)
	defer db.Close()

	w := doRequest(engine, "POST", "/api/v1/skill/admin/categories", map[string]interface{}{
		"icon_key": "robot",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestAdminUpdateCategory(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectExec("UPDATE categories").
		WillReturnResult(sqlmock.NewResult(0, 1))

	w := doRequest(engine, "PUT", "/api/v1/skill/admin/categories/cat-1", map[string]interface{}{
		"name":       "Updated Name",
		"icon_key":   "star",
		"sort_order": 2,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestAdminUpdateCategoryNotFound(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectExec("UPDATE categories").
		WillReturnResult(sqlmock.NewResult(0, 0))

	w := doRequest(engine, "PUT", "/api/v1/skill/admin/categories/nonexist", map[string]interface{}{
		"name": "Name",
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestAdminDeleteCategoryEmpty(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("UPDATE categories").
		WillReturnResult(sqlmock.NewResult(0, 1))

	w := doRequest(engine, "DELETE", "/api/v1/skill/admin/categories/cat-1", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestAdminDeleteCategoryInUse(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	w := doRequest(engine, "DELETE", "/api/v1/skill/admin/categories/cat-1", nil)

	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusConflict, w.Body.String())
	}
	body := parseBody(t, w)
	errorBody := body["error"].(map[string]interface{})
	if errorBody["code"] != errcode.CategoryInUse {
		t.Errorf("code=%v want=%v", errorBody["code"], errcode.CategoryInUse)
	}
}

func TestAdminDeleteCategoryNotFound(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("UPDATE categories").
		WillReturnResult(sqlmock.NewResult(0, 0))

	w := doRequest(engine, "DELETE", "/api/v1/skill/admin/categories/nonexist", nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

// --- Skill Visibility Tests ---

func TestGetSkillVisibilitySystemSameSpace(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	// System skill by another user in the same space should be visible.
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-1", "System Skill", "other-user", "Bob", "space-1", "system"))

	w := doRequest(engine, "GET", "/api/v1/skill/skill-1", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestGetSkillVisibilitySystemCrossSpace(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	// System skill in another space should remain visible.
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-1x", "System Skill", "other-user", "Bob", "other-space", "system"))

	w := doRequest(engine, "GET", "/api/v1/skill/skill-1x", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestGetSkillVisibilityPublicIsHidden(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-public", "Public Skill", "other-user", "Alice", "other-space", "public"))

	w := doRequest(engine, "GET", "/api/v1/skill/skill-public", nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestGetSkillVisibilityPrivateOwner(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	// Private skill owned by the current user in same space
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-2", "Private Skill", "user-1", "Alice", "space-1", "private"))

	w := doRequest(engine, "GET", "/api/v1/skill/skill-2", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestGetSkillVisibilityPrivateNonOwner(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	// Private skill owned by another user - should return 404
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-3", "Other Private", "other-user", "Bob", "space-1", "private"))

	w := doRequest(engine, "GET", "/api/v1/skill/skill-3", nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestGetSkillVisibilitySpaceDifferentSpace(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	// Space-visible skill in a different space - should return 404
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-4", "Other Space", "other-user", "Bob", "other-space", "space"))

	w := doRequest(engine, "GET", "/api/v1/skill/skill-4", nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestGetSkillNotFound(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols)) // empty result

	w := doRequest(engine, "GET", "/api/v1/skill/nonexist", nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
	body := parseBody(t, w)
	errorBody := body["error"].(map[string]interface{})
	if errorBody["code"] != errcode.NotFound {
		t.Errorf("code=%v want=%v", errorBody["code"], errcode.NotFound)
	}
}

// --- Skill Owner Operation Tests ---

func TestDeleteSkillNonOwner(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	// Skill owned by another user - DELETE should return 404 (anti-enumeration)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-5", "Not Mine", "other-user", "Bob", "space-1", "space"))

	w := doRequest(engine, "DELETE", "/api/v1/skill/skill-5", nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestDeleteSkillOwner(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-6", "My Skill", "user-1", "Alice", "space-1", "space"))
	mock.ExpectExec("UPDATE skills").
		WithArgs("skill-6").
		WillReturnResult(sqlmock.NewResult(0, 1))

	w := doRequest(engine, "DELETE", "/api/v1/skill/skill-6", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestUpdateSkillNonOwner(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-7", "Not Mine", "other-user", "Bob", "space-1", "space"))

	w := doRequest(engine, "PUT", "/api/v1/skill/skill-7", map[string]interface{}{
		"name": "Hacked",
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestUpdateSkillOwner(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	now := time.Now().UTC()
	// First call: GetByID for ownership check
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-8", "My Skill", "user-1", "Alice", "space-1", "space"))
	// Update
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE skills SET").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// Re-fetch after update
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols).AddRow(
			"skill-8", "Updated Skill", "Updated Skill", "", "", "",
			"new desc", "cat-1", []byte(`["updated"]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"readme", "file.zip", "skills/skill-8/v1.0.0/file.zip", int64(1024), "sha256",
			now, now, "1.0.0", "", int64(0), int64(0),
		))

	w := doRequest(engine, "PUT", "/api/v1/skill/skill-8", map[string]interface{}{
		"name":        "Updated Skill",
		"description": "new desc",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

// --- List Tests ---

func TestListSkills(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	now := time.Now().UTC()
	// Default sort preserves the historical latest cursor contract.
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillListCols).
			AddRow("s1", "Skill 1", "Skill 1", "", "", "",
				"desc1", "cat-1", []byte(`[]`),
				"user-1", "Alice", "space-1", "space", "1.0.0",
				"", "f.zip", "url", int64(100), "sha", now, now, "1.0.0", "", int64(0), int64(0)).
			AddRow("s2", "Skill 2", "Skill 2", "", "", "",
				"desc2", "cat-1", []byte(`[]`),
				"user-2", "Bob", "space-1", "public", "1.0.0",
				"", "f.zip", "url", int64(200), "sha", now, now, "1.0.0", "", int64(0), int64(0)))

	w := doRequest(engine, "GET", "/api/v1/skill", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	body := parseBody(t, w)
	items := body["data"].([]interface{})
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestListSkillsWithCategoryFilter(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillListRow("s1", "Filtered", "user-1", "Alice", "space-1", "space"))

	w := doRequest(engine, "GET", "/api/v1/skill?category_id=cat-1&sort=comprehensive", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestListSkillsSearch(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillListCols))

	w := doRequest(engine, "GET", "/api/v1/skill?q=test&sort=comprehensive", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestListMine(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	// ListMine uses SortLatest → cursor pagination (no COUNT query, uses limit+1)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillListRow("s1", "My Skill", "user-1", "Alice", "space-1", "private"))

	w := doRequest(engine, "GET", "/api/v1/skill/mine", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	// Verify it returns cursor pagination format
	body := parseBody(t, w)
	pagination := body["pagination"].(map[string]interface{})
	if _, ok := pagination["has_more"]; !ok {
		t.Error("expected cursor pagination with has_more field")
	}
}

func TestListMineCursorPagination(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	now := time.Now().UTC()
	// Return limit+1 (21) items to trigger has_more=true with next_cursor
	rows := sqlmock.NewRows(skillCols)
	for i := range 21 {
		rows.AddRow(
			fmt.Sprintf("s%d", i), "Skill", "Skill", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "private", "1.0.0",
			"", "f.zip", "url", int64(100), "sha",
			now.Add(-time.Duration(i)*time.Minute), now, "1.0.0", "", int64(0), int64(0),
		)
	}
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(rows)

	w := doRequest(engine, "GET", "/api/v1/skill/mine", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	body := parseBody(t, w)
	items := body["data"].([]interface{})
	if len(items) != 20 {
		t.Errorf("expected 20 items, got %d", len(items))
	}
	pagination := body["pagination"].(map[string]interface{})
	if pagination["has_more"] != true {
		t.Error("expected has_more=true when more results exist")
	}
	if pagination["next_cursor"] == nil || pagination["next_cursor"] == "" {
		t.Error("expected non-empty next_cursor")
	}
}

func TestListSkillTags(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT ranked\\.id, ranked\\.space_id, ranked\\.name").
		WithArgs("space-1", "space-1", "", "%auto%", 10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "space_id", "name", "created_by", "created_at", "updated_at"}).
			AddRow(int64(1), "space-1", "automation", "user-2", now, now).
			AddRow(int64(2), "", "auto-global", "admin", now, now))

	w := doRequest(engine, "GET", "/api/v1/skill/tags?q=auto&limit=10", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	body := parseBody(t, w)
	data := body["data"].(map[string]interface{})
	items := data["items"].([]interface{})
	if len(items) != 2 || items[0].(map[string]interface{})["name"] != "automation" || items[1].(map[string]interface{})["name"] != "auto-global" {
		t.Fatalf("unexpected items: %#v", items)
	}
}

// --- Upload/Parse Tests ---

func TestInitUploadFileTooLarge(t *testing.T) {
	engine, _, db := testSetup(t)
	defer db.Close()

	w := doRequest(engine, "POST", "/api/v1/skill/upload/init", map[string]interface{}{
		"file_name": "big.zip",
		"file_size": 100 * 1024 * 1024, // 100MB > 20MB limit
	})

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusRequestEntityTooLarge, w.Body.String())
	}
	body := parseBody(t, w)
	errorBody := body["error"].(map[string]interface{})
	if errorBody["code"] != errcode.FileTooLarge {
		t.Errorf("code=%v want=%v", errorBody["code"], errcode.FileTooLarge)
	}
}

func TestInitUploadInvalidFileName(t *testing.T) {
	engine, _, db := testSetup(t)
	defer db.Close()

	w := doRequest(engine, "POST", "/api/v1/skill/upload/init", map[string]interface{}{
		"file_name": "not-a-zip.tar.gz",
		"file_size": 1024,
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestInitUploadHappyPath(t *testing.T) {
	for _, fileName := range []string{"my-skill.zip", "my-skill.skill"} {
		t.Run(fileName, func(t *testing.T) {
			engine, mock, db := testSetup(t)
			defer db.Close()

			mock.ExpectExec("INSERT INTO parse_tasks").
				WillReturnResult(sqlmock.NewResult(1, 1))

			w := doRequest(engine, "POST", "/api/v1/skill/upload/init", map[string]interface{}{
				"file_name": fileName,
				"file_size": 5120,
			})

			if w.Code != http.StatusOK {
				t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
			}
			body := parseBody(t, w)
			data := body["data"].(map[string]interface{})
			if data["skill_upload_id"] == nil || data["skill_upload_id"] == "" {
				t.Error("expected skill_upload_id in response")
			}
		})
	}
}

// --- Category List ---

func TestListCategories(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	cols := []string{"id", "name", "icon_key", "sort_order", "skill_count"}
	mock.ExpectQuery("SELECT .+ FROM categories").
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("cat-1", "AI Tools", "robot", 1, 5).
			AddRow("cat-2", "Development", "code", 2, 3))

	w := doRequest(engine, "GET", "/api/v1/skill/categories", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	body := parseBody(t, w)
	data := body["data"].([]interface{})
	if len(data) != 2 {
		t.Errorf("expected 2 categories, got %d", len(data))
	}
}

// --- Download Test ---

func TestDownloadSkillRedirect(t *testing.T) {
	// Use a specific temp dir and pre-create the file
	tmpDir := t.TempDir()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	auth := marketmiddleware.NewAuthenticator(false, nil, model.Identity{
		UID: "user-1", Name: "Alice",
	}, "space-1")
	storageCfg := router.StorageConfig{
		Driver:   "local",
		LocalDir: tmpDir,
		BaseURL:  "http://localhost:8092",
		MaxMB:    20,
	}
	engine := router.PublicWithDB(db, auth, storageCfg)

	// Create the file on disk so PresignGet succeeds
	fileKey := "skills/skill-dl/v1.0.0/file.zip"
	fullPath := tmpDir + "/" + fileKey
	if err := os.MkdirAll(tmpDir+"/skills/skill-dl/v1.0.0", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte("fake zip content"), 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols).AddRow(
			"skill-dl", "Download Skill", "Download Skill", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"", "file.zip", fileKey, int64(1024), "sha",
			now, now, "1.0.0", "", int64(0), int64(0),
		))

	w := doRequest(engine, "GET", "/api/v1/skill/skill-dl/download", nil)

	if w.Code != http.StatusFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusFound, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Error("expected Location header for redirect")
	}
}

func TestDownloadSkillJSON(t *testing.T) {
	tmpDir := t.TempDir()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	auth := marketmiddleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Alice"}, "space-1")
	engine := router.PublicWithDB(db, auth, router.StorageConfig{
		Driver: "local", LocalDir: tmpDir, BaseURL: "http://localhost:8092", MaxMB: 20,
	})
	fileKey := "skills/skill-dl-json/v1.0.0/file.zip"
	if err := os.MkdirAll(tmpDir+"/skills/skill-dl-json/v1.0.0", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpDir+"/"+fileKey, []byte("fake zip content"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols).AddRow(
			"skill-dl-json", "Download Skill", "Download Skill", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"", "file.zip", fileKey, int64(1024), "sha", now, now, "1.0.0", "", int64(0), int64(0),
		))

	w := doRequest(engine, "GET", "/api/v1/skills/skill-dl-json/download?format=json", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	body := parseBody(t, w)
	data, ok := body["data"].(map[string]interface{})
	if !ok || data["download_url"] == "" || data["file_sha256"] != "sha" {
		t.Fatalf("missing download metadata: %v", body)
	}
}

// --- Error Format Consistency ---

func TestErrorFormatConsistency(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols)) // empty = not found

	w := doRequest(engine, "GET", "/api/v1/skill/nonexist", nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
	body := parseBody(t, w)
	errorBody, ok := body["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("error response missing error envelope: %v", body)
	}
	code, hasCode := errorBody["code"]
	msg, hasMsg := errorBody["message"]
	if !hasCode || !hasMsg {
		t.Fatalf("error response missing code or message: %v", body)
	}
	if code != errcode.NotFound {
		t.Errorf("code=%v want=%v", code, errcode.NotFound)
	}
	if msg == "" {
		t.Error("message should not be empty")
	}
}

// --- Reupload Non-Owner Test ---

func TestReuploadNonOwner(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	// Skill owned by another user
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-x", "Not Mine", "other-user", "Bob", "space-1", "space"))

	w := doRequest(engine, "POST", "/api/v1/skill/skill-x/reupload/init", map[string]interface{}{
		"file_name": "new.zip",
		"file_size": 1024,
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

// --- Admin Auth Tests (AUTH_ENABLED=true) ---

// stubSuperAdminResolver returns a fixed SuperAdmin identity for any non-empty
// token so admin integration tests can exercise the resolver + role-check
// chain without a real octo-server.
type stubSuperAdminResolver struct{}

func (stubSuperAdminResolver) Resolve(_ context.Context, token string) (model.Identity, error) {
	if token == "" {
		return model.Identity{}, nil
	}
	return model.Identity{
		UID:             "platform-admin",
		Name:            "Platform",
		Role:            marketmiddleware.RoleSuperAdmin,
		ContextIncluded: true,
	}, nil
}

func TestAdminCreateCategoryAcceptsSuperAdminToken(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	pubAuth := marketmiddleware.NewAuthenticator(false, nil, model.Identity{
		UID:  "user-1",
		Name: "Alice",
		Role: "member",
	}, "space-1")
	storageCfg := router.StorageConfig{
		Driver:   "local",
		LocalDir: t.TempDir(),
		BaseURL:  "http://localhost:8092",
		MaxMB:    20,
	}
	var adminResolver auth.Resolver = stubSuperAdminResolver{}
	engine := router.PublicWithDBAndAdminAuth(db, pubAuth, storageCfg, true, adminResolver)

	mock.ExpectExec("INSERT INTO categories").
		WillReturnResult(sqlmock.NewResult(1, 1))

	w := doRequestWithHeaders(engine, "POST", "/api/v1/skill/admin/categories", map[string]interface{}{
		"name":       "Test",
		"icon_key":   "star",
		"sort_order": 1,
	}, map[string]string{"Token": "super-admin-session"})

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusCreated, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminCreateCategoryMissingTokenUnauthorized(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = mock

	pubAuth := marketmiddleware.NewAuthenticator(false, nil, model.Identity{
		UID:  "admin-1",
		Name: "Admin",
		Role: "admin",
	}, "space-1")
	storageCfg := router.StorageConfig{
		Driver:   "local",
		LocalDir: t.TempDir(),
		BaseURL:  "http://localhost:8092",
		MaxMB:    20,
	}
	var adminResolver auth.Resolver = stubSuperAdminResolver{}
	engine := router.PublicWithDBAndAdminAuth(db, pubAuth, storageCfg, true, adminResolver)

	w := doRequest(engine, "POST", "/api/v1/skill/admin/categories", map[string]interface{}{
		"name":       "Admin Category",
		"icon_key":   "shield",
		"sort_order": 1,
	})

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
	body := parseBody(t, w)
	errorBody := body["error"].(map[string]interface{})
	if errorBody["code"] != errcode.Unauthorized {
		t.Errorf("code=%v want=%v", errorBody["code"], errcode.Unauthorized)
	}
}

// --- Expert admin surface: mount + role gate ---

// stubMemberResolver returns a fixed non-SuperAdmin identity for any non-empty
// token so the admin role gate's rejection path can be exercised end-to-end.
type stubMemberResolver struct{}

func (stubMemberResolver) Resolve(_ context.Context, token string) (model.Identity, error) {
	if token == "" {
		return model.Identity{}, nil
	}
	return model.Identity{
		UID:             "user-9",
		Name:            "Member",
		Role:            "member",
		ContextIncluded: true,
	}, nil
}

func expertAdminEngine(t *testing.T, resolver auth.Resolver) (*gin.Engine, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	pubAuth := marketmiddleware.NewAuthenticator(false, nil, model.Identity{
		UID:  "user-1",
		Name: "Alice",
		Role: "member",
	}, "space-1")
	storageCfg := router.StorageConfig{
		Driver:   "local",
		LocalDir: t.TempDir(),
		BaseURL:  "http://localhost:8092",
		MaxMB:    20,
	}
	return router.PublicWithDBAndAdminAuth(db, pubAuth, storageCfg, true, resolver), mock
}

func TestExpertAdminMountAcceptsSuperAdmin(t *testing.T) {
	engine, mock := expertAdminEngine(t, stubSuperAdminResolver{})

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM experts").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectQuery("SELECT .* FROM experts").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "short_name", "name", "summary", "category_id", "tags", "publisher",
			"owner_uid", "creator_name", "created_by_type", "created_by_bot_uid", "created_by_bot_name",
			"space_id", "visibility", "instruction", "mcp_config", "skills_json", "created_at", "updated_at", "deleted_at",
		}))

	w := doRequestWithHeaders(engine, "GET", "/api/v1/admin/experts", nil,
		map[string]string{"Token": "super-admin-session"})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExpertAdminMountRejectsPlainMember(t *testing.T) {
	engine, _ := expertAdminEngine(t, stubMemberResolver{})

	w := doRequestWithHeaders(engine, "GET", "/api/v1/admin/experts", nil,
		map[string]string{"Token": "member-session"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

func TestExpertAdminMountMissingTokenUnauthorized(t *testing.T) {
	engine, _ := expertAdminEngine(t, stubSuperAdminResolver{})

	w := doRequest(engine, "GET", "/api/v1/admin/experts", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
}

// Unused but needed by compiler if tests reference it
var _ = fmt.Sprint

// ─── marketAdmin scoping, against the real router ───────────────────────────
//
// internal/middleware/admin_test.go proves roleAdmitted itself, but it mounts
// routes the test registers by hand. The risk that survives it is a
// *registration* mistake, and a synthetic engine cannot catch either direction:
// a future admin group mounted with no gate at all (an open surface), or one
// that forgets RoleMarketAdmin and silently 403s the curators who are supposed
// to reach it. Admitting marketAdmin on the Expert Market groups is no longer
// on that list — it is the intent as of this change. These drive
// router.PublicWithDBAndAdminAuth, so the assertions are about the wiring that
// ships.

// requireGatePassed asserts that a request got past the admin role gate, without
// asserting a success code — reaching 2xx on these paths would need sqlmock
// expectations primed per route, which is not what is under test.
//
// The rejected set is every status that means "the gate did not admit this
// caller" or "there was no gate to admit them": 403 is the gate refusing, 401 is
// the auth layer refusing ahead of it, and 404/405 mean the route was never
// registered under that path or verb — each would otherwise let a broken case
// pass silently. What is left through is typically 500 here (sqlmock has nothing
// primed), which is a downstream failure and therefore proof the gate was
// cleared.
//
// 405 cannot occur today: gin.New() leaves HandleMethodNotAllowed at its false
// default, so a verb mismatch surfaces as 404. It is rejected anyway so that
// flipping that flag later cannot silently turn these into vacuous passes.
func requireGatePassed(t *testing.T, w *httptest.ResponseRecorder, label string) {
	t.Helper()
	switch w.Code {
	case http.StatusForbidden, http.StatusUnauthorized:
		t.Fatalf("%s: marketAdmin must pass the admin gate, got %d body=%s", label, w.Code, w.Body.String())
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		t.Fatalf("%s: route is not registered for this method+path, so this case proves nothing (got %d)", label, w.Code)
	}
}

type stubMarketAdminResolver struct{}

func (stubMarketAdminResolver) Resolve(_ context.Context, token string) (model.Identity, error) {
	if token == "" {
		return model.Identity{}, nil
	}
	return model.Identity{
		UID:             "catalog-curator",
		Name:            "Curator",
		Role:            marketmiddleware.RoleMarketAdmin,
		ContextIncluded: true,
	}, nil
}

// TestExpertAdminMountAdmitsMarketAdmin pins the Expert Market half of the role.
// marketAdmin runs the whole platform market, so these groups admit it — the
// same assertion shape as the catalog groups, including the explicit 404
// rejection so an unregistered route cannot pass the case vacuously.
func TestExpertAdminMountAdmitsMarketAdmin(t *testing.T) {
	for _, path := range []string{
		"/api/v1/admin/experts",
		"/api/v1/admin/squads",
		"/api/v1/admin/expert_categories",
		"/api/v1/admin/expert_tags",
	} {
		t.Run(path, func(t *testing.T) {
			engine, _ := expertAdminEngine(t, stubMarketAdminResolver{})
			w := doRequestWithHeaders(engine, "GET", path, nil,
				map[string]string{"Token": "market-admin-session"})
			requireGatePassed(t, w, path)
		})
	}
}

// TestExpertAdminMountAdmitsMarketAdminOnWrites covers the mutating verbs too,
// so a gate can't be correct on GET while a POST group was missed.
func TestExpertAdminMountAdmitsMarketAdminOnWrites(t *testing.T) {
	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: "POST", path: "/api/v1/admin/experts"},
		{method: "POST", path: "/api/v1/admin/squads"},
		{method: "POST", path: "/api/v1/admin/expert_categories"},
		{method: "POST", path: "/api/v1/admin/expert_skill_uploads"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			engine, _ := expertAdminEngine(t, stubMarketAdminResolver{})
			w := doRequestWithHeaders(engine, tc.method, tc.path, nil,
				map[string]string{"Token": "market-admin-session"})
			requireGatePassed(t, w, tc.method+" "+tc.path)
		})
	}
}

// TestCatalogAdminMountAdmitsMarketAdmin is the other half: every catalog group
// must let the role through the gate, so that dropping RoleMarketAdmin from any
// one registration fails CI rather than silently breaking curators.
//
// The gate is what is under test, not the handler, so the assertion is "neither
// refused nor missing" — a success code would depend on sqlmock expectations
// that are beside the point. 404 has to be excluded explicitly: without it a
// route that was never registered would satisfy "not 403" and the case would
// pass vacuously.
//
// /api/v1/admin/mcps and /admin/mcp_icon_uploads are absent here on purpose —
// PublicWithDBAndAdminAuth wires a nil AdminMCP handler, so registerAdminMCP
// returns early and those routes do not exist in this harness. They are covered
// against the real router in internal/api/router (TestAdminMcpsAdmitsMarketAdminInProd).
func TestCatalogAdminMountAdmitsMarketAdmin(t *testing.T) {
	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: "GET", path: "/api/v1/admin/skill_categories"},
		{method: "GET", path: "/api/v1/skill/admin/categories"},
		{method: "GET", path: "/api/v1/admin/skills"},
		{method: "POST", path: "/api/v1/admin/skill_uploads"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			engine, _ := expertAdminEngine(t, stubMarketAdminResolver{})
			w := doRequestWithHeaders(engine, tc.method, tc.path, nil,
				map[string]string{"Token": "market-admin-session"})
			requireGatePassed(t, w, tc.method+" "+tc.path)
		})
	}
}

// TestAdminMountStillRejectsPlainMember guards the direction that matters most
// now that every /api/v1/admin/* group admits marketAdmin: widening must not
// admit everyone.
//
// One case per independently mis-wireable Handler() call site that this harness
// mounts, since each carries its own gate instance and can be broken on its own:
// category/admin.go:16 and :22, skill/admin.go:16, upload/handler.go:93, and
// expert/admin.go:37, :45 and :56. Note skill_uploads and skills live on
// *different* groups despite the shared prefix, and expert_categories is a third
// group again — covering one does not cover the others.
//
// The MCP groups (router.go:243 and :252) are not reachable here:
// PublicWithDBAndAdminAuth passes a nil AdminMCP, so registerAdminMCP returns
// before mounting them. Their deny direction is covered in internal/api/router
// (TestAdminRejectsNonSuperAdminInProd).
func TestAdminMountStillRejectsPlainMember(t *testing.T) {
	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: "GET", path: "/api/v1/admin/skill_categories"},
		{method: "GET", path: "/api/v1/skill/admin/categories"},
		{method: "GET", path: "/api/v1/admin/skills"},
		{method: "POST", path: "/api/v1/admin/skill_uploads"},
		{method: "GET", path: "/api/v1/admin/experts"},
		{method: "GET", path: "/api/v1/admin/squads"},
		{method: "GET", path: "/api/v1/admin/expert_categories"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			engine, _ := expertAdminEngine(t, stubMemberResolver{})
			w := doRequestWithHeaders(engine, tc.method, tc.path, nil,
				map[string]string{"Token": "member-session"})
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s %s: status=%d want=%d body=%s",
					tc.method, tc.path, w.Code, http.StatusForbidden, w.Body.String())
			}
		})
	}
}
