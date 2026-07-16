package parse

import (
	"context"
	"io"
	"net/http"
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
