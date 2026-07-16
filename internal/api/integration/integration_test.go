package integration

import (
	"bytes"
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
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
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

var skillCols = []string{"id", "name", "display_name", "icon_url", "description", "category_id", "tags",
	"owner_id", "owner_name", "space_id", "visibility", "version",
	"readme_content", "file_name", "file_url", "file_size", "file_sha256",
	"created_at", "updated_at"}

func skillRow(id, name, ownerID, ownerName, spaceID, visibility string) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows(skillCols).AddRow(
		id, name, name, "", "description", "cat-1", []byte(`[]`),
		ownerID, ownerName, spaceID, visibility, "1.0.0",
		"readme", "file.zip", fmt.Sprintf("skills/%s/v1.0.0/file.zip", id), int64(1024), "sha256",
		now, now,
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

	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNoContent, w.Body.String())
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
	if body["code"] != errcode.CategoryInUse {
		t.Errorf("code=%v want=%v", body["code"], errcode.CategoryInUse)
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
	if body["code"] != errcode.NotFound {
		t.Errorf("code=%v want=%v", body["code"], errcode.NotFound)
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

	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNoContent, w.Body.String())
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
	mock.ExpectExec("UPDATE skills SET").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Re-fetch after update
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols).AddRow(
			"skill-8", "Updated Skill", "Updated Skill", "", "new desc", "cat-1", []byte(`["updated"]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"readme", "file.zip", "skills/skill-8/v1.0.0/file.zip", int64(1024), "sha256",
			now, now,
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
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols).
			AddRow("s1", "Skill 1", "Skill 1", "", "desc1", "cat-1", []byte(`[]`),
				"user-1", "Alice", "space-1", "space", "1.0.0",
				"", "f.zip", "url", int64(100), "sha", now, now).
			AddRow("s2", "Skill 2", "Skill 2", "", "desc2", "cat-1", []byte(`[]`),
				"user-2", "Bob", "space-1", "public", "1.0.0",
				"", "f.zip", "url", int64(200), "sha", now, now))

	w := doRequest(engine, "GET", "/api/v1/skill", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	body := parseBody(t, w)
	data := body["data"].(map[string]interface{})
	items := data["items"].([]interface{})
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestListSkillsWithCategoryFilter(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("s1", "Filtered", "user-1", "Alice", "space-1", "space"))

	w := doRequest(engine, "GET", "/api/v1/skill?category_id=cat-1", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestListSkillsSearch(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols))

	w := doRequest(engine, "GET", "/api/v1/skill?q=test", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestListMine(t *testing.T) {
	engine, mock, db := testSetup(t)
	defer db.Close()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(skillRow("s1", "My Skill", "user-1", "Alice", "space-1", "private"))

	w := doRequest(engine, "GET", "/api/v1/skill/mine", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
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
	if body["code"] != errcode.FileTooLarge {
		t.Errorf("code=%v want=%v", body["code"], errcode.FileTooLarge)
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
	if data["upload_id"] == nil || data["upload_id"] == "" {
		t.Error("expected upload_id in response")
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
			"skill-dl", "Download Skill", "Download Skill", "", "desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"", "file.zip", fileKey, int64(1024), "sha",
			now, now,
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
			"skill-dl-json", "Download Skill", "Download Skill", "", "desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"", "file.zip", fileKey, int64(1024), "sha", now, now,
		))

	w := doRequest(engine, "GET", "/api/v1/skill/skill-dl-json/download?format=json", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	body := parseBody(t, w)
	data, ok := body["data"].(map[string]interface{})
	if !ok || data["url"] == "" {
		t.Fatalf("missing download URL: %v", body)
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
	code, hasCode := body["code"]
	msg, hasMsg := body["message"]
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

func TestAdminCreateCategoryNonAdminForbidden(t *testing.T) {
	// Create a router with AUTH_ENABLED=true but a non-admin user
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = mock

	// Auth enabled, user role is "member" (not admin)
	auth := marketmiddleware.NewAuthenticator(false, nil, model.Identity{
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
	// Use PublicWithAuthEnabled to test with auth enabled admin checks
	engine := router.PublicWithDBAndAuth(db, auth, storageCfg, true)

	w := doRequest(engine, "POST", "/api/v1/skill/admin/categories", map[string]interface{}{
		"name":     "Test",
		"icon_key": "star",
	})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusForbidden, w.Body.String())
	}
	body := parseBody(t, w)
	if body["code"] != errcode.PermissionDenied {
		t.Errorf("code=%v want=%v", body["code"], errcode.PermissionDenied)
	}
}

func TestAdminCreateCategoryAdminAllowed(t *testing.T) {
	// Create a router with AUTH_ENABLED=true and an admin user
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Auth enabled, user role is "admin"
	auth := marketmiddleware.NewAuthenticator(false, nil, model.Identity{
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
	engine := router.PublicWithDBAndAuth(db, auth, storageCfg, true)

	mock.ExpectExec("INSERT INTO categories").
		WillReturnResult(sqlmock.NewResult(1, 1))

	w := doRequest(engine, "POST", "/api/v1/skill/admin/categories", map[string]interface{}{
		"name":       "Admin Category",
		"icon_key":   "shield",
		"sort_order": 1,
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusCreated, w.Body.String())
	}
}

// Unused but needed by compiler if tests reference it
var _ = fmt.Sprint
