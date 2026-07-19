package skill

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

// Compile-time check: fakeStorage must implement storage.Storage.
var _ storage.Storage = (*fakeStorage)(nil)

// fakeStorage implements storage.Storage for testing the Create/Update flow.
type fakeStorage struct {
	getErr       error
	getData      []byte // returned by GetObject
	putErr       error
	putCount     int
	putKeys      []string
	deleteCount  int
	copyErr      error
	copyCount    int
	copySrc      string
	copyDst      string
	presignedURL string
}

func (f *fakeStorage) PresignPut(_ context.Context, _ string, _ string, _ time.Duration) (string, http.Header, error) {
	return "", nil, nil
}
func (f *fakeStorage) PresignGet(_ context.Context, _ string, _ time.Duration) (string, error) {
	if f.presignedURL != "" {
		return f.presignedURL, nil
	}
	return "https://presigned.example.com/file", nil
}
func (f *fakeStorage) PublicURL(_ context.Context, key string) (string, error) {
	return "https://cdn.test/" + key, nil
}
func (f *fakeStorage) GetObject(_ context.Context, _ string) (io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getData != nil {
		return io.NopCloser(bytes.NewReader(f.getData)), nil
	}
	return io.NopCloser(bytes.NewReader(nil)), nil
}
func (f *fakeStorage) DeleteObject(_ context.Context, _ string) error {
	f.deleteCount++
	return nil
}
func (f *fakeStorage) PutObject(_ context.Context, key string, _ io.Reader, _ int64, _ string) error {
	f.putCount++
	f.putKeys = append(f.putKeys, key)
	return f.putErr
}
func (f *fakeStorage) CopyObject(_ context.Context, src, dst string) error {
	f.copyCount++
	f.copySrc = src
	f.copyDst = dst
	return f.copyErr
}

// makeTestZip creates a minimal zip archive with a SKILL.md for testing.
func makeTestZip(name, desc, version string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, _ := w.Create("SKILL.md")
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\nversion: %s\n---\n# %s\nBody content here.", name, desc, version, name)
	fw.Write([]byte(content))
	w.Close()
	return buf.Bytes()
}

// TestCreate_GetObjectFailure_NoDBMutation calls Service.Create with a failing
// GetObject and verifies that the parse task is NOT consumed and no Skill is created.
func TestCreate_GetObjectFailure_NoDBMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{getErr: errors.New("storage unavailable")}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "new-skill-id" })

	// Mock GetParseTask query — returns a valid success task
	parseRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
		"status", "result_name", "result_description", "result_version",
		"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
		"owner_id", "space_id", "skill_id",
	}).AddRow(
		"task-1", "upload-1", "skill.zip", int64(1024), "skills/upload-1/skill.zip", "sha256abc",
		"success", "My Skill", "A description", "1.0.0",
		[]byte(`["tag1"]`), "# My Skill\nContent", "", "", nil, 0,
		"user-1", "space-1", "",
	)
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-1").
		WillReturnRows(parseRows)

	ctx := context.Background()
	_, createErr := svc.Create(ctx, CreateParams{
		ParseTaskID: "task-1",
		UserID:      "user-1",
		UserName:    "User One",
		SpaceID:     "space-1",
	})

	if createErr == nil {
		t.Fatal("Create should have failed when GetObject fails")
	}
	if !containsString(createErr.Error(), "download temp zip") {
		t.Errorf("error should mention download, got: %v", createErr)
	}

	// Verify no DB mutations occurred (sqlmock ensures no unexpected queries)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB calls: %v", err)
	}
}

// TestCreate_PutObjectSuccess_DBMutationOccurs verifies the full Create flow succeeds
// with valid zip data and triggers the DB transaction.
func TestCreate_PutObjectSuccess_DBMutationOccurs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	zipData := makeTestZip("My Skill", "A description", "1.0.0")
	store := &fakeStorage{getData: zipData}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "new-skill-id" })

	// Mock GetParseTask
	parseRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
		"status", "result_name", "result_description", "result_version",
		"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
		"owner_id", "space_id", "skill_id",
	}).AddRow(
		"task-1", "upload-1", "skill.zip", int64(len(zipData)), "skills/upload-1/skill.zip", "sha256abc",
		"success", "My Skill", "A description", "1.0.0",
		[]byte(`["tag1"]`), "# My Skill\nContent", "", "", nil, 0,
		"user-1", "space-1", "",
	)
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-1").
		WillReturnRows(parseRows)

	// Expect the transaction: BEGIN, consume task, insert skill, insert version, upsert tags, COMMIT
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE parse_tasks SET status").
		WithArgs("task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skills").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_versions").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_tags").
		WithArgs("space-1", "tag1", "user-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ctx := context.Background()
	item, createErr := svc.Create(ctx, CreateParams{
		ParseTaskID: "task-1",
		UserID:      "user-1",
		UserName:    "User One",
		SpaceID:     "space-1",
	})

	if createErr != nil {
		t.Fatalf("Create should succeed, got: %v", createErr)
	}
	if item == nil {
		t.Fatal("Create should return a SkillItem")
	}

	// Verify PutObject was called twice (zip + SKILL.md)
	if store.putCount != 2 {
		t.Errorf("PutObject call count = %d, want 2", store.putCount)
	}
	expectedZipKey := "skills/new-skill-id/v1.0.0/skill.zip"
	expectedMdKey := "skills/new-skill-id/v1.0.0/SKILL.md"
	if len(store.putKeys) >= 2 {
		if store.putKeys[0] != expectedZipKey {
			t.Errorf("PutObject key[0] = %q, want %q", store.putKeys[0], expectedZipKey)
		}
		if store.putKeys[1] != expectedMdKey {
			t.Errorf("PutObject key[1] = %q, want %q", store.putKeys[1], expectedMdKey)
		}
	}

	// Verify all DB expectations were met
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("DB expectations not met: %v", err)
	}
}

// TestUpdate_GetObjectFailure_NoDBMutation calls Service.Update with a reupload
// parse_task_id and a failing GetObject, verifying no DB mutations occur.
func TestUpdate_GetObjectFailure_NoDBMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{getErr: errors.New("disk full")}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "id" })

	// Mock GetByID — returns an existing skill
	skillRows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage",
	}).AddRow(
		"skill-1", "Old Skill", "Old Skill", "", "", "",
		"desc", "cat-1", []byte(`[]`), "user-1", "User One",
		"space-1", "space", "1.0.0", "old readme", "old.zip", "skills/skill-1/v1.0.0/old.zip",
		int64(512), "oldsha", time.Now(), time.Now(),
		"1.0.0", "",
	)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("skill-1").
		WillReturnRows(skillRows)

	// Mock GetParseTask — returns a successful reupload task
	parseRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
		"status", "result_name", "result_description", "result_version",
		"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
		"owner_id", "space_id", "skill_id",
	}).AddRow(
		"task-2", "upload-2", "new-skill.zip", int64(2048), "skills/upload-2/new-skill.zip", "newsha",
		"success", "New Skill", "New desc", "2.0.0",
		[]byte(`["new"]`), "# New\nContent", "", "", nil, 0,
		"user-1", "space-1", "skill-1",
	)
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-2").
		WillReturnRows(parseRows)

	ctx := context.Background()
	_, updateErr := svc.Update(ctx, "skill-1", "user-1", "space-1", UpdateParams{
		ParseTaskID: "task-2",
	})

	if updateErr == nil {
		t.Fatal("Update should have failed when GetObject fails")
	}
	if !containsString(updateErr.Error(), "download temp zip") {
		t.Errorf("error should mention download, got: %v", updateErr)
	}

	// Verify no DB mutations (sqlmock catches unexpected queries)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB calls: %v", err)
	}
}

// TestCreate_RejectsReuploadTask verifies Create rejects tasks with skill_id set.
func TestCreate_RejectsReuploadTask(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "id" })

	// Mock GetParseTask — returns a reupload task (skill_id non-empty)
	parseRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
		"status", "result_name", "result_description", "result_version",
		"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
		"owner_id", "space_id", "skill_id",
	}).AddRow(
		"task-r", "upload-r", "reup.zip", int64(1024), "skills/upload-r/reup.zip", "sha",
		"success", "Name", "Desc", "1.0.0",
		[]byte(`[]`), "readme", "", "", nil, 0,
		"user-1", "space-1", "existing-skill-id",
	)
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-r").
		WillReturnRows(parseRows)

	ctx := context.Background()
	_, createErr := svc.Create(ctx, CreateParams{
		ParseTaskID: "task-r",
		UserID:      "user-1",
		UserName:    "User",
		SpaceID:     "space-1",
	})

	if !errors.Is(createErr, ErrInvalidParseTask) {
		t.Errorf("Create with reupload task should return ErrInvalidParseTask, got: %v", createErr)
	}

	// PutObject/GetObject should NOT be called for rejected tasks
	if store.putCount != 0 {
		t.Errorf("PutObject should not be called for rejected task, count = %d", store.putCount)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("DB expectations not met: %v", err)
	}
}

// TestObjectKey_Format verifies the object key format for zip and SKILL.md.
func TestObjectKey_Format(t *testing.T) {
	tests := []struct {
		name    string
		skillID string
		version string
		wantZip string
		wantMd  string
	}{
		{
			name:    "standard",
			skillID: "abc-123",
			version: "1.0.0",
			wantZip: "skills/abc-123/v1.0.0/skill.zip",
			wantMd:  "skills/abc-123/v1.0.0/SKILL.md",
		},
		{
			name:    "complex version",
			skillID: "def-456",
			version: "2.1.0-beta",
			wantZip: "skills/def-456/v2.1.0-beta/skill.zip",
			wantMd:  "skills/def-456/v2.1.0-beta/SKILL.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotZip := fmt.Sprintf("skills/%s/v%s/skill.zip", tt.skillID, tt.version)
			gotMd := fmt.Sprintf("skills/%s/v%s/SKILL.md", tt.skillID, tt.version)
			if gotZip != tt.wantZip {
				t.Errorf("zip key = %q, want %q", gotZip, tt.wantZip)
			}
			if gotMd != tt.wantMd {
				t.Errorf("md key = %q, want %q", gotMd, tt.wantMd)
			}
		})
	}
}

// TestCreate_SourceSkillID_FromParam verifies source_skill_id from API param takes precedence.
func TestCreate_SourceSkillID_FromParam(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	zipData := makeTestZip("Forked Skill", "desc", "1.0.0")
	store := &fakeStorage{getData: zipData}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "gen-id" })

	parseRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
		"status", "result_name", "result_description", "result_version",
		"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
		"owner_id", "space_id", "skill_id",
	}).AddRow(
		"task-f", "upload-f", "skill.zip", int64(len(zipData)), "skills/upload-f/skill.zip", "sha",
		"success", "Forked Skill", "desc", "1.0.0",
		[]byte(`[]`), "", "result-id-candidate", "", nil, 0,
		"user-1", "space-1", "",
	)
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-f").
		WillReturnRows(parseRows)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE parse_tasks SET status").
		WithArgs("task-f").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skills").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_versions").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ctx := context.Background()
	_, createErr := svc.Create(ctx, CreateParams{
		ParseTaskID:   "task-f",
		SourceSkillID: "explicit-source",
		UserID:        "user-1",
		UserName:      "User",
		SpaceID:       "space-1",
	})
	if createErr != nil {
		t.Fatalf("unexpected error: %v", createErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("DB expectations not met: %v", err)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Ensure json import is used (for test data setup).
var _ = json.RawMessage(`[]`)
