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

func (stubStorage) DeleteObject(context.Context, string) error {
	return nil
}

func (stubStorage) CopyObject(context.Context, string, string) error {
	return nil
}

var _ storage.Storage = (*stubStorage)(nil)

func TestInitUploadRejectsUnsafeZipFileName(t *testing.T) {
	svc := NewService(stubStorage{}, nil, nil, func() string { return "upload-1" }, 20)
	for _, fileName := range []string{
		"../skill.zip",
		"nested/skill.zip",
		`nested\skill.zip`,
		"skill..zip",
	} {
		t.Run(fileName, func(t *testing.T) {
			_, err := svc.InitUpload(context.Background(), fileName, 1024, "user-1", "space-1")
			if err != ErrInvalidFileName {
				t.Fatalf("expected ErrInvalidFileName, got %v", err)
			}
		})
	}
}

func TestInitIconUploadRejectsUnsafeFileName(t *testing.T) {
	svc := NewService(stubStorage{}, nil, nil, func() string { return "icon-1" }, 20)
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

func TestInitMcpIconUploadRejectsUnsafeFileName(t *testing.T) {
	svc := NewService(stubStorage{}, nil, nil, func() string { return "icon-1" }, 20)
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

func TestTriggerParseReturnsConflictWhenPendingStateWasConsumed(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewRepo(db)
	svc := NewService(stubStorage{}, repo, nil, func() string { return "upload-1" }, 20)
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)

	rows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"file_sha256", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-1", "upload-1", "skill.zip", int64(1), "skills/upload-1/skill.zip", "pending",
		"", "", "", nil, "", []byte("[]"), nil,
		"", "user-1", "space-1", "", now, now,
	)
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("upload-1").
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE parse_tasks SET status = 'parsing' WHERE id = \\? AND status = 'pending'").
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

func TestGetParseStatusMasksStoredFailureDetailsAndSanitizesReadme(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewRepo(db)
	svc := NewService(stubStorage{}, repo, nil, func() string { return "upload-1" }, 20)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)

	successRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"file_sha256", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-success", "upload-1", "skill.zip", int64(1), "skills/upload-1/skill.zip", "success",
		"", "", "safe-skill", "desc", "1.0.0", []byte(`["tag"]`), "# Demo\n\n<script>alert(1)</script>\n<div>ok</div>",
		"sha", "user-1", "space-1", "", now, now,
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
		"file_sha256", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-failed", "upload-2", "skill.zip", int64(1), "skills/upload-2/skill.zip", "failed",
		"INTERNAL_ERROR", "panic: db password leaked", "", nil, "", []byte("[]"), nil,
		"", "user-1", "space-1", "", now, now,
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

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
