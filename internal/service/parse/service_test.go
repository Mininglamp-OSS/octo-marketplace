package parse

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

type stubStorage struct{}

func (stubStorage) PresignPut(context.Context, string, string, time.Duration) (string, http.Header, error) {
	return "http://example.com/upload", http.Header{}, nil
}

func (stubStorage) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

func (stubStorage) PublicURL(context.Context, string) (string, error) {
	return "", nil
}

func (stubStorage) GetObject(context.Context, string) (io.ReadCloser, error) {
	return nil, nil
}

func (stubStorage) StatObject(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, nil
}

func (stubStorage) DeleteObject(context.Context, string) error {
	return nil
}

func (stubStorage) CopyObject(context.Context, string, string) error {
	return nil
}

func (stubStorage) PutObject(context.Context, string, io.Reader, int64, string) error {
	return nil
}

var _ storage.Storage = (*stubStorage)(nil)

func parseTaskRows(status string) *sqlmock.Rows {
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	return sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"result_id", "result_forked_from", "result_metadata",
		"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-1", "upload-1", "skill.zip", int64(1), "skills/upload-1/skill.zip", status,
		"", "", "", nil, "", []byte("[]"), nil,
		"", "", nil,
		"", 0, "user-1", "space-1", "", now, now,
	)
}

func TestInitUploadAcceptsSkillPackageFileName(t *testing.T) {
	for _, fileName := range []string{"skill.zip", "skill.skill", "Skill.SKILL"} {
		t.Run(fileName, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			mock.ExpectExec("INSERT INTO parse_tasks").
				WillReturnResult(sqlmock.NewResult(1, 1))

			repo := NewRepo(db)
			svc := NewService(stubStorage{}, repo, nil, func() string { return "upload-1" }, 20, ServiceConfig{})
			result, err := svc.InitUpload(context.Background(), fileName, 1024, "user-1", "space-1")
			if err != nil {
				t.Fatalf("InitUpload() error = %v", err)
			}
			if result.UploadID != "upload-1" {
				t.Fatalf("UploadID = %q, want upload-1", result.UploadID)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInitUploadRejectsUnsafeSkillPackageFileName(t *testing.T) {
	svc := NewService(stubStorage{}, nil, nil, func() string { return "upload-1" }, 20, ServiceConfig{})
	for _, fileName := range []string{
		"../skill.zip",
		"nested/skill.zip",
		`nested\skill.zip`,
		"skill..zip",
		"skill.tar.gz",
	} {
		t.Run(fileName, func(t *testing.T) {
			_, err := svc.InitUpload(context.Background(), fileName, 1024, "user-1", "space-1")
			if err != ErrInvalidFileName {
				t.Fatalf("expected ErrInvalidFileName, got %v", err)
			}
		})
	}
}

func TestInitUploadRejectsNonPositiveFileSize(t *testing.T) {
	svc := NewService(stubStorage{}, nil, nil, func() string { return "upload-1" }, 20, ServiceConfig{})
	for _, fileSize := range []int64{0, -1} {
		t.Run("upload", func(t *testing.T) {
			_, err := svc.InitUpload(context.Background(), "skill.zip", fileSize, "user-1", "space-1")
			if err != ErrInvalidFileSize {
				t.Fatalf("InitUpload error = %v, want ErrInvalidFileSize", err)
			}
		})
		t.Run("reupload", func(t *testing.T) {
			_, err := svc.InitReupload(context.Background(), "skill-1", "skill.zip", fileSize, "user-1", "space-1")
			if err != ErrInvalidFileSize {
				t.Fatalf("InitReupload error = %v, want ErrInvalidFileSize", err)
			}
		})
	}
}

func TestInitIconUploadRejectsUnsafeFileName(t *testing.T) {
	svc := NewService(stubStorage{}, nil, nil, func() string { return "icon-1" }, 20, ServiceConfig{})
	for _, fileName := range []string{
		"../icon.png",
		"nested/icon.png",
		`nested\icon.png`,
		"icon..png",
	} {
		t.Run(fileName, func(t *testing.T) {
			_, err := svc.InitIconUpload(context.Background(), fileName, 1024, "user-1")
			if err != ErrInvalidFileName {
				t.Fatalf("expected ErrInvalidFileName, got %v", err)
			}
		})
	}
}

func TestInitIconUploadRejectsSVG(t *testing.T) {
	svc := NewService(stubStorage{}, nil, nil, func() string { return "icon-1" }, 20, ServiceConfig{})
	for _, fileName := range []string{"evil.svg", "EVIL.SVG"} {
		t.Run(fileName, func(t *testing.T) {
			_, err := svc.InitIconUpload(context.Background(), fileName, 1024, "user-1")
			if err == nil {
				t.Fatal("expected SVG icon upload to be rejected")
			}
		})
	}
}

func TestInitMcpIconUploadRejectsUnsafeFileName(t *testing.T) {
	svc := NewService(stubStorage{}, nil, nil, func() string { return "icon-1" }, 20, ServiceConfig{})
	for _, fileName := range []string{
		"../icon.png",
		"nested/icon.png",
		`nested\icon.png`,
		"icon..png",
	} {
		t.Run(fileName, func(t *testing.T) {
			_, err := svc.InitMcpIconUpload(context.Background(), fileName, 1024)
			if err != ErrInvalidFileName {
				t.Fatalf("expected ErrInvalidFileName, got %v", err)
			}
		})
	}
}

func TestInitMcpIconUploadRejectsSVG(t *testing.T) {
	svc := NewService(stubStorage{}, nil, nil, func() string { return "icon-1" }, 20, ServiceConfig{})
	for _, fileName := range []string{"evil.svg", "EVIL.SVG"} {
		t.Run(fileName, func(t *testing.T) {
			_, err := svc.InitMcpIconUpload(context.Background(), fileName, 1024)
			if err == nil {
				t.Fatal("expected SVG icon upload to be rejected")
			}
		})
	}
}

func TestTriggerParseReturnsConflictWhenPendingStateWasConsumed(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewRepo(db)
	svc := NewService(stubStorage{}, repo, nil, func() string { return "upload-1" }, 20, ServiceConfig{})
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)

	rows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"result_id", "result_forked_from", "result_metadata",
		"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-1", "upload-1", "skill.zip", int64(1), "skills/upload-1/skill.zip", "pending",
		"", "", "", nil, "", []byte("[]"), nil,
		"", "", nil,
		"", 0, "user-1", "space-1", "", now, now,
	)
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("upload-1").
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE parse_tasks SET status = 'parsing', attempts = attempts \\+ 1 WHERE id = \\? AND status = 'pending'").
		WithArgs("task-1").
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = svc.TriggerParse(context.Background(), "upload-1", "user-1")
	if err != ErrTaskNotPending {
		t.Fatalf("expected ErrTaskNotPending, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerParseQueueFullRestoresPendingForRetry(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewRepo(db)
	fullWorker := &Worker{jobs: make(chan parseJob)}
	svc := NewService(stubStorage{}, repo, fullWorker, func() string { return "upload-1" }, 20, ServiceConfig{})

	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("upload-1").
		WillReturnRows(parseTaskRows("pending"))
	mock.ExpectExec("UPDATE parse_tasks SET status = 'parsing', attempts = attempts \\+ 1 WHERE id = \\? AND status = 'pending'").
		WithArgs("task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE parse_tasks").
		WithArgs("task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = svc.TriggerParse(context.Background(), "upload-1", "user-1")
	if err != ErrParseQueueFull {
		t.Fatalf("first TriggerParse error = %v, want ErrParseQueueFull", err)
	}

	svc.worker = &Worker{jobs: make(chan parseJob, 1)}
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("upload-1").
		WillReturnRows(parseTaskRows("pending"))
	mock.ExpectExec("UPDATE parse_tasks SET status = 'parsing', attempts = attempts \\+ 1 WHERE id = \\? AND status = 'pending'").
		WithArgs("task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	taskID, err := svc.TriggerParse(context.Background(), "upload-1", "user-1")
	if err != nil {
		t.Fatalf("second TriggerParse error = %v", err)
	}
	if taskID != "task-1" {
		t.Fatalf("taskID = %q, want task-1", taskID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetParseStatusMasksStoredFailureDetailsAndSanitizesReadme(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewRepo(db)
	svc := NewService(stubStorage{}, repo, nil, func() string { return "upload-1" }, 20, ServiceConfig{})
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)

	successRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"result_id", "result_forked_from", "result_metadata",
		"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-success", "upload-1", "skill.zip", int64(1), "skills/upload-1/skill.zip", "success",
		"", "", "safe-skill", "desc", "1.0.0", []byte(`["tag"]`), "# Demo\n\n<script>alert(1)</script>\n<div>ok</div>",
		"", "", nil,
		"sha", 0, "user-1", "space-1", "", now, now,
	)
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("task-success").
		WillReturnRows(successRows)

	successResult, err := svc.GetParseStatus(context.Background(), "task-success", "user-1")
	if err != nil {
		t.Fatalf("GetParseStatus success: %v", err)
	}
	if successResult.Result == nil {
		t.Fatal("expected success result")
	}
	if strings.Contains(successResult.Result.ReadmeContent, "<script>") {
		t.Fatalf("readme should be sanitized, got %q", successResult.Result.ReadmeContent)
	}
	if !strings.Contains(successResult.Result.ReadmeContent, "&lt;div&gt;ok&lt;/div&gt;") {
		t.Fatalf("expected escaped html, got %q", successResult.Result.ReadmeContent)
	}

	failedRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"result_id", "result_forked_from", "result_metadata",
		"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-failed", "upload-2", "skill.zip", int64(1), "skills/upload-2/skill.zip", "failed",
		"INTERNAL_ERROR", "panic: db password leaked", "", nil, "", []byte("[]"), nil,
		"", "", nil,
		"", 0, "user-1", "space-1", "", now, now,
	)
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("task-failed").
		WillReturnRows(failedRows)

	failedResult, err := svc.GetParseStatus(context.Background(), "task-failed", "user-1")
	if err != nil {
		t.Fatalf("GetParseStatus failed: %v", err)
	}
	if failedResult.Error == nil {
		t.Fatal("expected failed error payload")
	}
	if failedResult.Error.Message != publicParseErrorMessage("INTERNAL_ERROR") {
		t.Fatalf("unexpected public message %q", failedResult.Error.Message)
	}

	mismatchMessage := `重新上传的 Skill 与当前 Skill 不一致：上传 Skill name 为 "gstack-guard"，当前 Skill name 为 "octo-style"`
	mismatchRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"result_id", "result_forked_from", "result_metadata",
		"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-mismatch", "upload-3", "skill.zip", int64(1), "skills/upload-3/skill.zip", "failed",
		"SKILL_NAME_MISMATCH", mismatchMessage, "", nil, "", []byte("[]"), nil,
		"", "", nil,
		"", 0, "user-1", "space-1", "skill-1", now, now,
	)
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("task-mismatch").
		WillReturnRows(mismatchRows)

	mismatchResult, err := svc.GetParseStatus(context.Background(), "task-mismatch", "user-1")
	if err != nil {
		t.Fatalf("GetParseStatus mismatch: %v", err)
	}
	if mismatchResult.Error == nil {
		t.Fatal("expected mismatch error payload")
	}
	if mismatchResult.Error.Message != mismatchMessage {
		t.Fatalf("mismatch message = %q, want %q", mismatchResult.Error.Message, mismatchMessage)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGetParseStatusRecoversStaleParsing verifies the lazy recovery path:
// when a task has been in 'parsing' beyond staleTimeout, a poll atomically
// reclaims it and re-submits to the worker pool.
func TestGetParseStatusRecoversStaleParsing(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewRepo(db)
	worker := NewWorker(blockingStorage{}, repo, db, WorkerConfig{PoolSize: 5, ParseTimeout: 10 * time.Millisecond})
	svc := NewService(stubStorage{}, repo, worker, func() string { return "u1" }, 20, ServiceConfig{
		StaleTimeout: 2 * time.Minute,
		MaxAttempts:  2,
	})

	// Task updated_at is 10 minutes ago — well past 2-minute staleTimeout.
	staleTime := time.Now().Add(-10 * time.Minute)
	rows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"result_id", "result_forked_from", "result_metadata",
		"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-stale", "upload-1", "skill.zip", int64(1024), "skills/upload-1/skill.zip", "parsing",
		"", "", "", nil, "", []byte("[]"), nil,
		"", "", nil,
		"", 0, "user-1", "space-1", "", staleTime, staleTime,
	)
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("task-stale").
		WillReturnRows(rows)

	// Expect the atomic recovery UPDATE — this caller wins the race.
	mock.ExpectExec("UPDATE parse_tasks").
		WithArgs("task-stale", 120, 2).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE parse_tasks SET status = 'failed', error_code = \\?, error_message = \\? WHERE id = \\?").
		WithArgs("INTERNAL_ERROR", publicParseErrorMessage("INTERNAL_ERROR"), "task-stale").
		WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := svc.GetParseStatus(context.Background(), "task-stale", "user-1")
	if err != nil {
		t.Fatalf("GetParseStatus: %v", err)
	}
	if result.Status != "parsing" {
		t.Fatalf("status=%q want=parsing", result.Status)
	}
	// Worker was submitted — wait for it to finish (it will fail due to blocking storage).
	worker.Wait()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGetParseStatusConcurrentPollOnlyOneWins verifies that when multiple pods
// poll the same stale task, only the one with affected_rows=1 re-submits.
func TestGetParseStatusConcurrentPollOnlyOneWins(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewRepo(db)
	worker := NewWorker(blockingStorage{}, repo, db, WorkerConfig{PoolSize: 5, ParseTimeout: time.Minute})
	svc := NewService(stubStorage{}, repo, worker, func() string { return "u1" }, 20, ServiceConfig{
		StaleTimeout: 2 * time.Minute,
		MaxAttempts:  2,
	})

	staleTime := time.Now().Add(-10 * time.Minute)
	rows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"result_id", "result_forked_from", "result_metadata",
		"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-stale", "upload-1", "skill.zip", int64(1024), "skills/upload-1/skill.zip", "parsing",
		"", "", "", nil, "", []byte("[]"), nil,
		"", "", nil,
		"", 0, "user-1", "space-1", "", staleTime, staleTime,
	)
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("task-stale").
		WillReturnRows(rows)

	// This caller LOSES the race — affected_rows=0.
	mock.ExpectExec("UPDATE parse_tasks").
		WithArgs("task-stale", 120, 2).
		WillReturnResult(sqlmock.NewResult(0, 0))

	result, err := svc.GetParseStatus(context.Background(), "task-stale", "user-1")
	if err != nil {
		t.Fatalf("GetParseStatus: %v", err)
	}
	if result.Status != "parsing" {
		t.Fatalf("status=%q want=parsing (loser should not change status)", result.Status)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGetParseStatusMaxAttemptsExhausted verifies that once attempts >= maxAttempts
// the task is marked as failed with PARSE_RETRY_EXHAUSTED.
func TestGetParseStatusMaxAttemptsExhausted(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewRepo(db)
	svc := NewService(stubStorage{}, repo, nil, func() string { return "u1" }, 20, ServiceConfig{
		StaleTimeout: 2 * time.Minute,
		MaxAttempts:  2,
	})

	staleTime := time.Now().Add(-10 * time.Minute)
	rows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"result_id", "result_forked_from", "result_metadata",
		"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-exhausted", "upload-1", "skill.zip", int64(1024), "skills/upload-1/skill.zip", "parsing",
		"", "", "", nil, "", []byte("[]"), nil,
		"", "", nil,
		"", 2, "user-1", "space-1", "", staleTime, staleTime,
	)
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("task-exhausted").
		WillReturnRows(rows)

	// Expect MarkRetryExhausted call
	mock.ExpectExec("UPDATE parse_tasks SET status = 'failed', error_code = 'PARSE_RETRY_EXHAUSTED'").
		WithArgs("task-exhausted").
		WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := svc.GetParseStatus(context.Background(), "task-exhausted", "user-1")
	if err != nil {
		t.Fatalf("GetParseStatus: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status=%q want=failed", result.Status)
	}
	if result.Error == nil || result.Error.Code != "PARSE_RETRY_EXHAUSTED" {
		t.Fatalf("error=%+v want PARSE_RETRY_EXHAUSTED", result.Error)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGetParseStatusNonStaleParsingReturnsNormally verifies that a task in
// 'parsing' status that hasn't exceeded staleTimeout is returned as-is.
func TestGetParseStatusNonStaleParsingReturnsNormally(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewRepo(db)
	svc := NewService(stubStorage{}, repo, nil, func() string { return "u1" }, 20, ServiceConfig{
		StaleTimeout: 5 * time.Minute,
		MaxAttempts:  2,
	})

	// Updated just 1 minute ago — within staleTimeout.
	recentTime := time.Now().Add(-1 * time.Minute)
	rows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"result_id", "result_forked_from", "result_metadata",
		"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-active", "upload-1", "skill.zip", int64(1024), "skills/upload-1/skill.zip", "parsing",
		"", "", "", nil, "", []byte("[]"), nil,
		"", "", nil,
		"", 0, "user-1", "space-1", "", recentTime, recentTime,
	)
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("task-active").
		WillReturnRows(rows)

	result, err := svc.GetParseStatus(context.Background(), "task-active", "user-1")
	if err != nil {
		t.Fatalf("GetParseStatus: %v", err)
	}
	if result.Status != "parsing" {
		t.Fatalf("status=%q want=parsing", result.Status)
	}
	if result.Error != nil {
		t.Fatalf("non-stale parsing task should not have error, got %+v", result.Error)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
