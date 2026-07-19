package upload

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"context"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
	metricssvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/metrics"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/gin-gonic/gin"
)

// testableMetricsRedis records TrackDownload calls for assertions.
type testableMetricsRedis struct {
	downloadCalled bool
	downloadType   string
	downloadID     string
	downloadErr    error
}

func (m *testableMetricsRedis) TrackView(_ context.Context, _, _ string) error { return nil }
func (m *testableMetricsRedis) TrackDownload(_ context.Context, resourceType, resourceID string) error {
	m.downloadCalled = true
	m.downloadType = resourceType
	m.downloadID = resourceID
	return m.downloadErr
}
func (m *testableMetricsRedis) TrackInstall(_ context.Context, _, _ string) error { return nil }

// alwaysVisibleResolver satisfies metricssvc.ResourceResolver for tests.
type alwaysVisibleResolver struct{}

func (r *alwaysVisibleResolver) CanView(_ context.Context, _ string, _ metricssvc.Caller) (bool, error) {
	return true, nil
}

var skillCols = []string{
	"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
	"description", "category_id", "tags",
	"owner_id", "owner_name", "space_id", "visibility", "version",
	"readme_content", "file_name", "file_url", "file_size", "file_sha256",
	"created_at", "updated_at", "resolved_version", "version_storage",
	"view_count", "download_count",
}

// buildDownloadTestRouter wires a gin engine with skill download routes, using the
// given sqlmock DB and a testable metrics redis. Returns the router, mock, and a temp dir.
func buildDownloadTestRouter(t *testing.T, db *sql.DB, metricsRedis *testableMetricsRedis) (*gin.Engine, string) {
	t.Helper()
	tmpDir := t.TempDir()

	auth := middleware.NewAuthenticator(false, nil, model.Identity{
		UID: "user-1", Name: "Alice",
	}, "space-1")
	ls := storage.NewLocal(tmpDir, "http://localhost:8092")

	skRepo := skillrepo.New(db)
	skSvc := skillsvc.New(skRepo, nil, ls, func() string { return "id" })

	parseRepo := parse.NewRepo(db)
	worker := parse.NewWorker(ls, parseRepo, db, parse.WorkerConfig{})
	pSvc := parse.NewService(ls, parseRepo, worker, func() string { return "id" }, 20, parse.ServiceConfig{})

	mSvc := metricssvc.New(metricsRedis)
	metricssvc.RegisterResolver("skill", &alwaysVisibleResolver{})

	h := New(pSvc, skSvc, ls, 20)
	h.SetMetricsService(mSvc)

	r := gin.New()
	v1 := r.Group("/api/v1", auth.Handler())
	h.Register(v1)

	return r, tmpDir
}

// TestDownloadTracksMetricsOnSuccess verifies that a successful download (format=json)
// triggers metrics TrackDownload with the correct resource type and ID.
func TestDownloadTracksMetricsOnSuccess(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mockRedis := &testableMetricsRedis{}
	r, tmpDir := buildDownloadTestRouter(t, db, mockRedis)

	// Pre-create file on disk so PresignGet (local storage) succeeds
	fileKey := "skills/skill-dl/v1.0.0/file.zip"
	os.MkdirAll(tmpDir+"/skills/skill-dl/v1.0.0", 0o755)
	os.WriteFile(tmpDir+"/"+fileKey, []byte("zip content"), 0o644)

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols).AddRow(
			"skill-dl", "Test", "Test", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"", "file.zip", fileKey, int64(1024), "sha",
			now, now, "1.0.0", "", int64(5), int64(3),
		))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills/skill-dl/download?format=json", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	// Assert metrics was tracked
	if !mockRedis.downloadCalled {
		t.Fatal("expected TrackDownload to be called after successful URL generation")
	}
	if mockRedis.downloadType != "skill" {
		t.Errorf("download type = %q, want %q", mockRedis.downloadType, "skill")
	}
	if mockRedis.downloadID != "skill-dl" {
		t.Errorf("download id = %q, want %q", mockRedis.downloadID, "skill-dl")
	}
}

// TestDownloadDoesNotTrackWhenSkillNotFound verifies no metrics call when skill 404.
func TestDownloadDoesNotTrackWhenSkillNotFound(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mockRedis := &testableMetricsRedis{}
	r, _ := buildDownloadTestRouter(t, db, mockRedis)

	// Return empty result set → skill not found
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills/nonexist/download", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
	if mockRedis.downloadCalled {
		t.Error("TrackDownload should NOT be called when skill is not found")
	}
}

// TestDownloadDoesNotTrackWhenURLGenerationFails verifies no metrics when the
// download URL cannot be generated (file does not exist on storage).
func TestDownloadDoesNotTrackWhenURLGenerationFails(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mockRedis := &testableMetricsRedis{}
	r, _ := buildDownloadTestRouter(t, db, mockRedis)
	// Do NOT pre-create the file → PresignGet will fail

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols).AddRow(
			"skill-fail", "Fail", "Fail", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"", "file.zip", "skills/missing/v1.0.0/file.zip", int64(1024), "sha",
			now, now, "1.0.0", "", int64(0), int64(0),
		))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills/skill-fail/download", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if mockRedis.downloadCalled {
		t.Error("TrackDownload should NOT be called when URL generation fails")
	}
}

// TestDownloadMetricsFailureDoesNotBlockResponse verifies that when metrics Redis
// returns an error, the download still succeeds (200 with format=json).
func TestDownloadMetricsFailureDoesNotBlockResponse(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Redis that always errors
	mockRedis := &testableMetricsRedis{downloadErr: errors.New("redis connection refused")}
	r, tmpDir := buildDownloadTestRouter(t, db, mockRedis)

	fileKey := "skills/skill-ok/v1.0.0/file.zip"
	os.MkdirAll(tmpDir+"/skills/skill-ok/v1.0.0", 0o755)
	os.WriteFile(tmpDir+"/"+fileKey, []byte("zip"), 0o644)

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(sqlmock.NewRows(skillCols).AddRow(
			"skill-ok", "OK", "OK", "", "", "",
			"desc", "cat-1", []byte(`[]`),
			"user-1", "Alice", "space-1", "space", "1.0.0",
			"", "file.zip", fileKey, int64(1024), "sha",
			now, now, "1.0.0", "", int64(0), int64(0),
		))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills/skill-ok/download?format=json", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Download should still succeed despite metrics failure
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	// Metrics should have been attempted
	if !mockRedis.downloadCalled {
		t.Error("expected TrackDownload to be attempted even when it fails")
	}
}
