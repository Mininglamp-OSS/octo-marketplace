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
	mock.ExpectExec("DELETE FROM categories").
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
	mock.ExpectExec("DELETE FROM categories").
		WillReturnResult(sqlmock.NewResult(0, 0))

	w := doRequest(engine, "DELETE", "/api/v1/skill/admin/categories/nonexist", nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

// --- Skill Visibility Tests ---

func TestGetSkillVisibilityPublicSameSpace(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	// Public skill by another user in the same space - should be visible
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-1", "Public Skill", "other-user", "Bob", "space-1", "public"))

	w := doRequest(engine, "GET", "/api/v1/skill/skill-1", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestGetSkillVisibilityPublicCrossSpace(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	// Public skill in another space - should return 404 (Space isolation)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("skill-1x", "Public Skill", "other-user", "Bob", "other-space", "public"))

	w := doRequest(engine, "GET", "/api/v1/skill/skill-1x", nil)

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
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM skill_versions").
		WithArgs("skill-6").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM skills").
		WithArgs("skill-6").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

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
	// Default sort is comprehensive → offset pagination with COUNT first
	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
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

	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillListRow("s1", "Filtered", "user-1", "Alice", "space-1", "space"))

	w := doRequest(engine, "GET", "/api/v1/skill?category_id=cat-1", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestListSkillsSearch(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillListCols))

	w := doRequest(engine, "GET", "/api/v1/skill?q=test", nil)

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
	mock.ExpectQuery("SELECT space_id, name, created_by, created_at, updated_at").
		WithArgs("space-1", "%auto%", 10).
		WillReturnRows(sqlmock.NewRows([]string{"space_id", "name", "created_by", "created_at", "updated_at"}).
			AddRow("space-1", "automation", "user-2", now, now))

	w := doRequest(engine, "GET", "/api/v1/skill/tags?q=auto&limit=10", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	body := parseBody(t, w)
	data := body["data"].(map[string]interface{})
	items := data["items"].([]interface{})
	if len(items) != 1 || items[0].(map[string]interface{})["name"] != "automation" {
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
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectExec("INSERT INTO parse_tasks").
		WillReturnResult(sqlmock.NewResult(1, 1))

	w := doRequest(engine, "POST", "/api/v1/skill/upload/init", map[string]interface{}{
		"file_name": "my-skill.zip",
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

// Unused but needed by compiler if tests reference it
var _ = fmt.Sprint
