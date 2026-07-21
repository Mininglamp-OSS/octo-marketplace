package parse

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

func TestSanitizeStringPreservesUTF8RuneBoundaries(t *testing.T) {
	withinLimit := strings.Repeat("中", 400)
	if got := sanitizeString(withinLimit, 1024); got != withinLimit || !utf8.ValidString(got) {
		t.Fatalf("valid CJK content was corrupted: runes=%d valid=%v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}

	overLimit := strings.Repeat("中", 1100)
	got := sanitizeString(overLimit, 1024)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 1024 {
		t.Fatalf("rune-aware truncation failed: runes=%d valid=%v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
}

func TestTruncateUTF8BytesPreservesRuneBoundary(t *testing.T) {
	const maxBytes = 1024 * 1024
	input := strings.Repeat("中", maxBytes/3+2)
	got := truncateUTF8Bytes(input, maxBytes)
	if len(got) > maxBytes || !utf8.ValidString(got) {
		t.Fatalf("byte-bounded truncation produced invalid output: bytes=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

type blockingStorage struct{}

func (blockingStorage) PresignPut(context.Context, string, string, time.Duration) (string, http.Header, error) {
	return "", http.Header{}, nil
}

func (blockingStorage) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

func (blockingStorage) PublicURL(context.Context, string) (string, error) {
	return "", nil
}

func (blockingStorage) GetObject(ctx context.Context, _ string) (io.ReadCloser, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingStorage) StatObject(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{Size: 1}, nil
}

func (blockingStorage) DeleteObject(context.Context, string) error {
	return nil
}

func (blockingStorage) CopyObject(context.Context, string, string) error {
	return nil
}

func (blockingStorage) PutObject(context.Context, string, io.Reader, int64, string) error {
	return nil
}

var _ storage.Storage = (*blockingStorage)(nil)

type panicStorage struct{}

func (panicStorage) PresignPut(context.Context, string, string, time.Duration) (string, http.Header, error) {
	return "", http.Header{}, nil
}

func (panicStorage) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

func (panicStorage) PublicURL(context.Context, string) (string, error) {
	return "", nil
}

func (panicStorage) GetObject(context.Context, string) (io.ReadCloser, error) {
	panic("storage secret leaked")
}

func (panicStorage) StatObject(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{Size: 1}, nil
}

func (panicStorage) DeleteObject(context.Context, string) error {
	return nil
}

func (panicStorage) CopyObject(context.Context, string, string) error {
	return nil
}

func (panicStorage) PutObject(context.Context, string, io.Reader, int64, string) error {
	return nil
}

type zipStorage struct {
	data []byte
}

func (s zipStorage) PresignPut(context.Context, string, string, time.Duration) (string, http.Header, error) {
	return "", http.Header{}, nil
}

func (s zipStorage) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

func (s zipStorage) PublicURL(context.Context, string) (string, error) {
	return "", nil
}

func (s zipStorage) GetObject(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

func (s zipStorage) StatObject(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{Size: int64(len(s.data))}, nil
}

func (s zipStorage) DeleteObject(context.Context, string) error {
	return nil
}

func (s zipStorage) CopyObject(context.Context, string, string) error {
	return nil
}

func (s zipStorage) PutObject(context.Context, string, io.Reader, int64, string) error {
	return nil
}

type oversizedStorage struct {
	size       int64
	deleteKeys []string
}

func (s *oversizedStorage) PresignPut(context.Context, string, string, time.Duration) (string, http.Header, error) {
	return "", http.Header{}, nil
}

func (s *oversizedStorage) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

func (s *oversizedStorage) PublicURL(context.Context, string) (string, error) {
	return "", nil
}

func (s *oversizedStorage) GetObject(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("GetObject must not be called for oversized object")
}

func (s *oversizedStorage) StatObject(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{Size: s.size}, nil
}

func (s *oversizedStorage) DeleteObject(_ context.Context, key string) error {
	s.deleteKeys = append(s.deleteKeys, key)
	return nil
}

func (s *oversizedStorage) CopyObject(context.Context, string, string) error {
	return nil
}

func (s *oversizedStorage) PutObject(context.Context, string, io.Reader, int64, string) error {
	return nil
}

type saturatingStorage struct {
	started chan struct{}
}

func (s saturatingStorage) PresignPut(context.Context, string, string, time.Duration) (string, http.Header, error) {
	return "", http.Header{}, nil
}

func (s saturatingStorage) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

func (s saturatingStorage) PublicURL(context.Context, string) (string, error) {
	return "", nil
}

func (s saturatingStorage) GetObject(ctx context.Context, _ string) (io.ReadCloser, error) {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s saturatingStorage) StatObject(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{Size: 1}, nil
}

func (s saturatingStorage) DeleteObject(context.Context, string) error {
	return nil
}

func (s saturatingStorage) CopyObject(context.Context, string, string) error {
	return nil
}

func (s saturatingStorage) PutObject(context.Context, string, io.Reader, int64, string) error {
	return nil
}

func TestWorkerMarksTaskFailedAfterParseTimeout(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE parse_tasks SET status = 'failed', error_code = \\?, error_message = \\? WHERE id = \\?").
		WithArgs("INTERNAL_ERROR", publicParseErrorMessage("INTERNAL_ERROR"), "task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	worker := NewWorker(blockingStorage{}, NewRepo(db), db, WorkerConfig{
		PoolSize:     5,
		ParseTimeout: 10 * time.Millisecond,
	})
	worker.process(context.Background(), "task-1", "skills/upload-1/skill.zip", 1024)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerDeletesOversizedObjectBeforeParsing(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE parse_tasks SET status = 'failed', error_code = \\?, error_message = \\? WHERE id = \\?").
		WithArgs("FILE_TOO_LARGE", publicParseErrorMessage("FILE_TOO_LARGE"), "task-oversized").
		WillReturnResult(sqlmock.NewResult(0, 1))

	store := &oversizedStorage{size: 2048}
	worker := NewWorker(store, NewRepo(db), db, WorkerConfig{PoolSize: 1, QueueSize: 1, ParseTimeout: time.Second})
	worker.process(context.Background(), "task-oversized", "skill-uploads/upload-1/skill.zip", 1024)

	if len(store.deleteKeys) != 1 || store.deleteKeys[0] != "skill-uploads/upload-1/skill.zip" {
		t.Fatalf("deleteKeys=%v, want oversized upload cleanup", store.deleteKeys)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerSubmitBoundsRunningAndQueuedWork(t *testing.T) {
	db, _, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	started := make(chan struct{})
	worker := NewWorker(saturatingStorage{started: started}, NewRepo(db), db, WorkerConfig{
		PoolSize:     1,
		QueueSize:    1,
		ParseTimeout: time.Second,
	})

	if err := worker.Submit("task-running", "skill-uploads/running.zip", 1024); err != nil {
		t.Fatalf("first Submit error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first job did not start")
	}
	if err := worker.Submit("task-queued", "skill-uploads/queued.zip", 1024); err != nil {
		t.Fatalf("second Submit error = %v", err)
	}
	if got := len(worker.jobs); got != 1 {
		t.Fatalf("queued jobs = %d, want 1", got)
	}
	for i := 0; i < 20; i++ {
		err := worker.Submit("task-extra", "skill-uploads/extra.zip", 1024)
		if !errors.Is(err, ErrParseQueueFull) {
			t.Fatalf("extra Submit #%d error = %v, want ErrParseQueueFull", i, err)
		}
	}
	if got := len(worker.jobs); got != 1 {
		t.Fatalf("queued jobs after rejected submits = %d, want 1", got)
	}
}

func TestWorkerSubmitMasksPanicDetails(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE parse_tasks SET status = 'failed', error_code = \\?, error_message = \\? WHERE id = \\?").
		WithArgs("INTERNAL_ERROR", publicParseErrorMessage("INTERNAL_ERROR"), "task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	worker := NewWorker(panicStorage{}, NewRepo(db), db, WorkerConfig{PoolSize: 5, ParseTimeout: 30 * time.Second})
	worker.Submit("task-1", "skills/upload-1/skill.zip", 1024)
	worker.Wait()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerProcessSyncHonorsSemaphoreAndContext(t *testing.T) {
	db, _, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	started := make(chan struct{})
	worker := NewWorker(saturatingStorage{started: started}, NewRepo(db), db, WorkerConfig{PoolSize: 1, QueueSize: 1, ParseTimeout: time.Second})
	if err := worker.Submit("task-running", "skills/upload-1/running.zip", 1024); err != nil {
		t.Fatalf("Submit running job: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("running job did not start")
	}
	if err := worker.Submit("task-queued", "skills/upload-1/queued.zip", 1024); err != nil {
		t.Fatalf("Submit queued job: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err = worker.ProcessSync(ctx, "task-1", "skills/upload-1/skill.zip", 1024)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ProcessSync error = %v, want deadline exceeded", err)
	}
}

func TestWorkerSanitizesReadmeBeforePersisting(t *testing.T) {
	zipData := createWorkerZip(t, map[string][]byte{
		"SKILL.md": []byte(strings.Join([]string{
			"---",
			"name: safe-skill",
			"description: demo description",
			"version: 1.2.3",
			"tags:",
			"  - demo",
			"---",
			"",
			"# Safe Skill",
			"",
			`<script>alert("xss")</script>`,
			`<div onclick="evil()">hello</div>`,
			"",
			"```html",
			`<script>keep()</script>`,
			"```",
		}, "\n")),
	})

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	taskRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"result_id", "result_forked_from", "result_metadata",
		"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-1", "upload-1", "skill.zip", int64(len(zipData)), "skills/upload-1/skill.zip", "parsing",
		"", "", "", nil, "", []byte("[]"), nil,
		"", "", nil,
		"", 0, "user-1", "space-1", "", now, now,
	)

	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("task-1").
		WillReturnRows(taskRows)
	mock.ExpectQuery("SELECT id FROM skills").
		WithArgs("safe-skill", "space-1", "user-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE parse_tasks SET status = 'success',").
		WithArgs(
			"safe-skill",
			stringArg("demo description"),
			"1.2.3",
			sqlmock.AnyArg(),
			stringArg("# Safe Skill\n\n&lt;div onclick=&#34;evil()&#34;&gt;hello&lt;/div&gt;\n\n```html\n<script>keep()</script>\n```"),
			sqlmock.AnyArg(),
			"",               // result_id
			"",               // result_forked_from
			sqlmock.AnyArg(), // result_metadata (nil json)
			"task-1",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	worker := NewWorker(zipStorage{data: zipData}, NewRepo(db), db, WorkerConfig{PoolSize: 5, ParseTimeout: 30 * time.Second})
	worker.process(context.Background(), "task-1", "skills/upload-1/skill.zip", int64(len(zipData)+1024))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerReuploadNameMismatchFailsBeforeDuplicateCheck(t *testing.T) {
	zipData := createWorkerZip(t, map[string][]byte{
		"SKILL.md": []byte(strings.Join([]string{
			"---",
			"name: gstack-guard",
			"description: demo description",
			"version: 1.2.3",
			"---",
			"",
			"# Wrong Skill",
		}, "\n")),
	})

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	taskRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "status",
		"error_code", "error_message",
		"result_name", "result_description", "result_version", "result_tags", "result_readme",
		"result_id", "result_forked_from", "result_metadata",
		"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
	}).AddRow(
		"task-1", "upload-1", "skill.zip", int64(len(zipData)), "skills/upload-1/skill.zip", "parsing",
		"", "", "", nil, "", []byte("[]"), nil,
		"", "", nil,
		"", 0, "user-1", "space-1", "skill-1", now, now,
	)

	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("task-1").
		WillReturnRows(taskRows)
	mock.ExpectQuery("SELECT name FROM skills WHERE id = \\? AND space_id = \\? AND owner_id = \\?").
		WithArgs("skill-1", "space-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("ui-skill-case-1784277863"))
	mock.ExpectExec("UPDATE parse_tasks SET status = 'failed', error_code = \\?, error_message = \\? WHERE id = \\?").
		WithArgs("SKILL_NAME_MISMATCH", `重新上传的 Skill 与当前 Skill 不一致：上传 Skill name 为 "gstack-guard"，当前 Skill name 为 "ui-skill-case-1784277863"`, "task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	worker := NewWorker(zipStorage{data: zipData}, NewRepo(db), db, WorkerConfig{PoolSize: 5, ParseTimeout: 30 * time.Second})
	worker.process(context.Background(), "task-1", "skills/upload-1/skill.zip", int64(len(zipData)+1024))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func createWorkerZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()

	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "worker.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type stringArg string

func (s stringArg) Match(v driver.Value) bool {
	got, ok := v.(string)
	return ok && got == string(s)
}
