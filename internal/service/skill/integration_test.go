package skill

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
)

// --- Integration test: Create full flow (upload zip → parse → create skill → verify storage path) ---

func TestCreate_FullFlow_VerifiesStoragePath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	zipData := makeTestZip("Integration Skill", "Integration test description", "2.0.0")
	store := &fakeStorage{getData: zipData}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)

	idSeq := 0
	idGen := func() string {
		idSeq++
		return fmt.Sprintf("gen-id-%d", idSeq)
	}
	svc := New(repo, catRepo, store, idGen)

	// Mock GetParseTask
	parseRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
		"status", "result_name", "result_description", "result_version",
		"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
		"owner_id", "space_id", "skill_id",
	}).AddRow(
		"task-int-1", "upload-int-1", "skill.zip", int64(len(zipData)),
		"skill-uploads/upload-int-1/skill.zip", "sha256-test",
		"success", "Integration Skill", "Integration test description", "2.0.0",
		[]byte(`["integration","test"]`), "# Integration Skill\nBody", "", "", nil, 0,
		"user-int", "space-int", "",
	)
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-int-1").
		WillReturnRows(parseRows)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE parse_tasks SET status").
		WithArgs("task-int-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skills").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_versions").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_tags").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_tags").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ctx := context.Background()
	item, err := svc.Create(ctx, CreateParams{
		ParseTaskID: "task-int-1",
		UserID:      "user-int",
		UserName:    "Integration User",
		SpaceID:     "space-int",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify skill item
	if item.ID != "gen-id-1" {
		t.Errorf("ID = %q, want %q", item.ID, "gen-id-1")
	}
	if item.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", item.Version, "2.0.0")
	}

	// Verify storage paths are correct: skills/{id}/v{version}/skill.zip and SKILL.md
	expectedZipKey := "skills/gen-id-1/v2.0.0/skill.zip"
	expectedMdKey := "skills/gen-id-1/v2.0.0/SKILL.md"

	if len(store.putKeys) != 2 {
		t.Fatalf("PutObject call count = %d, want 2", len(store.putKeys))
	}
	if store.putKeys[0] != expectedZipKey {
		t.Errorf("zip storage key = %q, want %q", store.putKeys[0], expectedZipKey)
	}
	if store.putKeys[1] != expectedMdKey {
		t.Errorf("md storage key = %q, want %q", store.putKeys[1], expectedMdKey)
	}

	// Verify the temporary upload used skill-uploads/ prefix
	// The parse task file_url was "skill-uploads/upload-int-1/skill.zip"
	// This confirms presign uploads use the temp prefix

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// --- Integration test: Reupload flow (update with parse task → verify new version) ---

func TestUpdate_ReuploadFlow_NewVersionGenerated(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	zipData := makeTestZip("Updated Skill", "Updated desc", "3.0.0")
	store := &fakeStorage{getData: zipData}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)

	idSeq := 0
	idGen := func() string {
		idSeq++
		return fmt.Sprintf("ver-id-%d", idSeq)
	}
	svc := New(repo, catRepo, store, idGen)

	now := time.Now()

	// Mock GetByID — existing skill
	skillRows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage", "view_count", "download_count",
	}).AddRow(
		"skill-reup", "Original Skill", "Original Skill", "", "", "old-ver-id",
		"Original desc", "cat-1", []byte(`["v1"]`), "user-reup", "User Reup",
		"space-reup", "space", "1.0.0", "old readme", "skill.zip",
		"skills/skill-reup/v1.0.0/skill.zip", int64(1024), "oldsha", now, now,
		"1.0.0", `{"type":"s3","zip_object_key":"skills/skill-reup/v1.0.0/skill.zip","skill_md_object_key":"skills/skill-reup/v1.0.0/SKILL.md","zip_file_name":"skill.zip","zip_size":1024,"zip_sha256":"oldsha"}`, int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("skill-reup").
		WillReturnRows(skillRows)

	// Mock GetParseTask — reupload task
	parseRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
		"status", "result_name", "result_description", "result_version",
		"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
		"owner_id", "space_id", "skill_id",
	}).AddRow(
		"task-reup", "upload-reup", "new.zip", int64(len(zipData)),
		"skill-uploads/upload-reup/new.zip", "newsha256",
		"success", "Updated Skill", "Updated desc", "3.0.0",
		[]byte(`["v3","updated"]`), "# Updated\nNew body", "", "", nil, 0,
		"user-reup", "space-reup", "skill-reup",
	)
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-reup").
		WillReturnRows(parseRows)

	// Expect the transactional update
	// Order: consume parse task → update skill → insert version → upsert tags
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE parse_tasks SET status").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE skills SET").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_versions").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_tags").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_tags").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// Mock the re-fetch after update
	updatedRows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage", "view_count", "download_count",
	}).AddRow(
		"skill-reup", "Updated Skill", "Updated Skill", "", "", "ver-id-1",
		"Updated desc", "cat-1", []byte(`["v3","updated"]`), "user-reup", "User Reup",
		"space-reup", "space", "3.0.0", "# Updated\nNew body", "skill.zip",
		"skills/skill-reup/v3.0.0/skill.zip", int64(len(zipData)), "newsha256", now, now,
		"3.0.0", `{"type":"s3","zip_object_key":"skills/skill-reup/v3.0.0/skill.zip","skill_md_object_key":"skills/skill-reup/v3.0.0/SKILL.md","zip_file_name":"skill.zip","zip_size":2048,"zip_sha256":"newsha256"}`, int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("skill-reup").
		WillReturnRows(updatedRows)

	ctx := context.Background()
	item, err := svc.Update(ctx, "skill-reup", "user-reup", "space-reup", UpdateParams{
		ParseTaskID: "task-reup",
		Changelog:   "Major update to v3",
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Verify the new version is reflected
	if item.Version != "3.0.0" {
		t.Errorf("Version = %q, want %q", item.Version, "3.0.0")
	}

	// Verify storage was called with new version keys
	expectedZipKey := "skills/skill-reup/v3.0.0/skill.zip"
	expectedMdKey := "skills/skill-reup/v3.0.0/SKILL.md"
	if len(store.putKeys) < 2 {
		t.Fatalf("PutObject call count = %d, want >= 2", len(store.putKeys))
	}
	if store.putKeys[0] != expectedZipKey {
		t.Errorf("zip key = %q, want %q", store.putKeys[0], expectedZipKey)
	}
	if store.putKeys[1] != expectedMdKey {
		t.Errorf("md key = %q, want %q", store.putKeys[1], expectedMdKey)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// --- Integration test: Get query verifies JOIN result ---

func TestGet_JoinResult_VersionStorageResolved(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "id" })

	now := time.Now()

	// Mock GetByID — returns a skill with current_version_id pointing to a version row
	skillRows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage", "view_count", "download_count",
	}).AddRow(
		"skill-join", "Join Skill", "Join Skill", "", "", "ver-join-1",
		"desc", "", []byte(`[]`), "user-join", "User Join",
		"space-join", "space", "1.0.0", "", "skill.zip",
		"skills/skill-join/v1.0.0/skill.zip", int64(512), "sha1",
		now, now,
		// resolved_version from JOIN with skill_versions
		"2.0.0",
		// version_storage from skill_versions
		`{"type":"s3","zip_object_key":"skills/skill-join/v2.0.0/skill.zip","skill_md_object_key":"skills/skill-join/v2.0.0/SKILL.md","zip_file_name":"skill.zip","zip_size":2048,"zip_sha256":"sha2"}`, int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("skill-join").
		WillReturnRows(skillRows)

	ctx := context.Background()
	item, err := svc.Get(ctx, "skill-join", "space-join", "user-join")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	// Verify JOIN result: resolved version, file_url, file_size, file_sha256 come from VersionStorage
	if item.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q (resolved from JOIN)", item.Version, "2.0.0")
	}
	if item.FileURL != "skills/skill-join/v2.0.0/skill.zip" {
		t.Errorf("FileURL = %q, want %q", item.FileURL, "skills/skill-join/v2.0.0/skill.zip")
	}
	if item.FileSize != 2048 {
		t.Errorf("FileSize = %d, want %d", item.FileSize, 2048)
	}
	if item.FileSHA256 != "sha2" {
		t.Errorf("FileSHA256 = %q, want %q", item.FileSHA256, "sha2")
	}
	if item.FileName != "skill.zip" {
		t.Errorf("FileName = %q, want %q", item.FileName, "skill.zip")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// --- Integration test: Download verifies presign URL from storage JSON ---

func TestGetDownloadInfo_UsesVersionStorageKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{presignedURL: "https://cdn.example.com/skills/dl-skill/v1.0.0/skill.zip?sig=abc"}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "id" })

	now := time.Now()

	// Skill with current_version_id pointing to a version with full storage JSON
	skillRows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage", "view_count", "download_count",
	}).AddRow(
		"dl-skill", "Download Skill", "Download Skill", "", "", "ver-dl-1",
		"desc", "", []byte(`[]`), "user-dl", "User DL",
		"space-dl", "space", "1.0.0", "", "skill.zip",
		"skills/dl-skill/v1.0.0/skill.zip", int64(4096), "dlsha",
		now, now,
		"1.0.0",
		`{"type":"s3","zip_object_key":"skills/dl-skill/v1.0.0/skill.zip","skill_md_object_key":"skills/dl-skill/v1.0.0/SKILL.md","zip_file_name":"skill.zip","zip_size":4096,"zip_sha256":"dlsha"}`, int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("dl-skill").
		WillReturnRows(skillRows)

	ctx := context.Background()
	info, err := svc.GetDownloadInfo(ctx, "dl-skill", "space-dl", "user-dl")
	if err != nil {
		t.Fatalf("GetDownloadInfo failed: %v", err)
	}

	if info.DownloadURL != "https://cdn.example.com/skills/dl-skill/v1.0.0/skill.zip?sig=abc" {
		t.Errorf("DownloadURL = %q", info.DownloadURL)
	}
	if info.FileSHA256 != "dlsha" {
		t.Errorf("FileSHA256 = %q, want %q", info.FileSHA256, "dlsha")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// --- Integration test: /skill-md endpoint returns markdown content ---

func TestGetSkillMD_ReturnsMarkdownContent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mdContent := []byte("---\nname: Test\n---\n# Test Skill\nMarkdown body here.")
	store := &fakeStorage{getData: mdContent}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "id" })

	now := time.Now()

	// Skill with version_storage containing skill_md_object_key
	skillRows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage", "view_count", "download_count",
	}).AddRow(
		"md-skill", "MD Skill", "MD Skill", "", "", "ver-md-1",
		"desc", "", []byte(`[]`), "user-md", "User MD",
		"space-md", "space", "1.0.0", "", "skill.zip",
		"skills/md-skill/v1.0.0/skill.zip", int64(1024), "mdsha",
		now, now,
		"1.0.0",
		`{"type":"s3","zip_object_key":"skills/md-skill/v1.0.0/skill.zip","skill_md_object_key":"skills/md-skill/v1.0.0/SKILL.md","zip_file_name":"skill.zip","zip_size":1024,"zip_sha256":"mdsha"}`, int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("md-skill").
		WillReturnRows(skillRows)

	ctx := context.Background()
	data, err := svc.GetSkillMD(ctx, "md-skill", "space-md", "user-md")
	if err != nil {
		t.Fatalf("GetSkillMD failed: %v", err)
	}

	if !bytes.Equal(data, mdContent) {
		t.Errorf("GetSkillMD returned %q, want %q", data, mdContent)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// --- Integration test: /skill-md returns 404 for old version without skill_md_object_key ---

func TestGetSkillMD_LegacyVersion_ReturnsErrNoFile(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "id" })

	now := time.Now()

	// Skill with legacy storage: only object_key, no skill_md_object_key
	skillRows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage", "view_count", "download_count",
	}).AddRow(
		"legacy-md", "Legacy Skill", "Legacy Skill", "", "", "ver-legacy-1",
		"desc", "", []byte(`[]`), "user-legacy", "User Legacy",
		"space-legacy", "space", "1.0.0", "", "skill.zip",
		"skills/legacy-md/v1.0.0/skill.zip", int64(1024), "legsha",
		now, now,
		"1.0.0",
		// Legacy storage format: only object_key, no skill_md_object_key
		`{"type":"s3","object_key":"skills/legacy-md/v1.0.0/skill.zip"}`, int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("legacy-md").
		WillReturnRows(skillRows)

	ctx := context.Background()
	_, err = svc.GetSkillMD(ctx, "legacy-md", "space-legacy", "user-legacy")

	if !errors.Is(err, ErrNoFile) {
		t.Errorf("GetSkillMD for legacy version should return ErrNoFile, got: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// --- Integration test: Legacy storage compat — fallback object_key in rowToItem ---

func TestRowToItem_LegacyStorageFallback_ObjectKey(t *testing.T) {
	now := time.Now()

	// Old version storage with only {"type":"s3","object_key":"..."} — no zip_object_key
	row := &skillrepo.SkillRow{
		ID:              "old-skill",
		Name:            "Old Skill",
		Version:         "0.9.0",
		ResolvedVersion: "0.9.0",
		VersionStorage:  `{"type":"s3","object_key":"old-path/skill.zip"}`,
		FileName:        "",
		FileURL:         "",
		FileSize:        0,
		FileSHA256:      "",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	svc := &Service{}
	item := svc.rowToItem(context.Background(), row)

	// Should fallback to "object_key" from legacy storage
	if item.FileURL != "old-path/skill.zip" {
		t.Errorf("FileURL = %q, want %q (legacy fallback)", item.FileURL, "old-path/skill.zip")
	}
	if item.FileName != "skill.zip" {
		t.Errorf("FileName = %q, want %q (default)", item.FileName, "skill.zip")
	}
}

// --- Integration test: Create with PutObject failure cleans up ---

func TestCreate_PutZipFails_NoDBMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	zipData := makeTestZip("Fail Skill", "Will fail", "1.0.0")
	store := &fakeStorage{
		getData: zipData,
		putErr:  errors.New("storage write error"),
	}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "fail-id" })

	parseRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
		"status", "result_name", "result_description", "result_version",
		"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
		"owner_id", "space_id", "skill_id",
	}).AddRow(
		"task-fail", "upload-fail", "skill.zip", int64(len(zipData)),
		"skill-uploads/upload-fail/skill.zip", "sha",
		"success", "Fail Skill", "Will fail", "1.0.0",
		[]byte(`[]`), "", "", "", nil, 0,
		"user-fail", "space-fail", "",
	)
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-fail").
		WillReturnRows(parseRows)

	ctx := context.Background()
	_, createErr := svc.Create(ctx, CreateParams{
		ParseTaskID: "task-fail",
		UserID:      "user-fail",
		UserName:    "Fail User",
		SpaceID:     "space-fail",
	})

	if createErr == nil {
		t.Fatal("Create should fail when PutObject fails")
	}
	if !containsString(createErr.Error(), "upload zip") {
		t.Errorf("error should mention upload, got: %v", createErr)
	}

	// No DB transaction should have happened
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB calls: %v", err)
	}
}

// --- Integration test: Verify upload prefix is "skill-uploads/" ---

func TestUploadPrefix_IsSkillUploads(t *testing.T) {
	// The parse service uses "skill-uploads/{uploadID}/{filename}" as the temp prefix.
	// Verify the Create service reads from this prefix and puts to the permanent path.

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	zipData := makeTestZip("Prefix Skill", "desc", "1.0.0")
	store := &fakeStorage{getData: zipData}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "prefix-id" })

	// Parse task file_url uses the temporary "skill-uploads/" prefix
	tempPath := "skill-uploads/some-upload-id/my-skill.zip"

	parseRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
		"status", "result_name", "result_description", "result_version",
		"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
		"owner_id", "space_id", "skill_id",
	}).AddRow(
		"task-prefix", "some-upload-id", "my-skill.zip", int64(len(zipData)),
		tempPath, "sha",
		"success", "Prefix Skill", "desc", "1.0.0",
		[]byte(`[]`), "", "", "", nil, 0,
		"user-p", "space-p", "",
	)
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-prefix").
		WillReturnRows(parseRows)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE parse_tasks SET status").
		WithArgs("task-prefix").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skills").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_versions").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ctx := context.Background()
	_, err = svc.Create(ctx, CreateParams{
		ParseTaskID: "task-prefix",
		UserID:      "user-p",
		UserName:    "User P",
		SpaceID:     "space-p",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify the final storage path is NOT "skill-uploads/" — it's "skills/{id}/v{ver}/"
	permanentZipKey := "skills/prefix-id/v1.0.0/skill.zip"
	permanentMdKey := "skills/prefix-id/v1.0.0/SKILL.md"

	if len(store.putKeys) < 2 {
		t.Fatalf("PutObject calls = %d, want >= 2", len(store.putKeys))
	}
	if store.putKeys[0] != permanentZipKey {
		t.Errorf("permanent zip key = %q, want %q", store.putKeys[0], permanentZipKey)
	}
	if store.putKeys[1] != permanentMdKey {
		t.Errorf("permanent md key = %q, want %q", store.putKeys[1], permanentMdKey)
	}

	// Confirm temp key != permanent key (prefix differs)
	if tempPath == permanentZipKey {
		t.Error("temp upload path should differ from permanent path")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// --- Integration test: VersionStorage JSON round-trip ---

func TestVersionStorage_JSONRoundTrip(t *testing.T) {
	vs := model.VersionStorage{
		Type:             "s3",
		ZipObjectKey:     "skills/abc/v1.0.0/skill.zip",
		SkillMdObjectKey: "skills/abc/v1.0.0/SKILL.md",
		ZipFileName:      "skill.zip",
		ZipSize:          4096,
		ZipSHA256:        "sha256hash",
	}
	data, err := json.Marshal(vs)
	if err != nil {
		t.Fatal(err)
	}

	var decoded model.VersionStorage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.Type != "s3" {
		t.Errorf("Type = %q", decoded.Type)
	}
	if decoded.ZipObjectKey != "skills/abc/v1.0.0/skill.zip" {
		t.Errorf("ZipObjectKey = %q", decoded.ZipObjectKey)
	}
	if decoded.SkillMdObjectKey != "skills/abc/v1.0.0/SKILL.md" {
		t.Errorf("SkillMdObjectKey = %q", decoded.SkillMdObjectKey)
	}
	if decoded.ZipFileName != "skill.zip" {
		t.Errorf("ZipFileName = %q", decoded.ZipFileName)
	}
	if decoded.ZipSize != 4096 {
		t.Errorf("ZipSize = %d", decoded.ZipSize)
	}
	if decoded.ZipSHA256 != "sha256hash" {
		t.Errorf("ZipSHA256 = %q", decoded.ZipSHA256)
	}
}

// --- Integration test: Legacy VersionStorage JSON decode compatibility ---

func TestVersionStorage_LegacyFormatDecode(t *testing.T) {
	// Old format: {"type":"s3","object_key":"..."}
	legacy := `{"type":"s3","object_key":"skills/old/v1.0.0/skill.zip"}`

	var vs model.VersionStorage
	err := json.Unmarshal([]byte(legacy), &vs)
	if err != nil {
		t.Fatal(err)
	}

	// New fields should be empty
	if vs.ZipObjectKey != "" {
		t.Errorf("ZipObjectKey should be empty for legacy format, got %q", vs.ZipObjectKey)
	}
	if vs.SkillMdObjectKey != "" {
		t.Errorf("SkillMdObjectKey should be empty for legacy format, got %q", vs.SkillMdObjectKey)
	}

	// The legacy "object_key" field is not in the struct — handled by rowToItem fallback logic
	var legacyStruct struct {
		ObjectKey string `json:"object_key"`
	}
	if err := json.Unmarshal([]byte(legacy), &legacyStruct); err != nil {
		t.Fatal(err)
	}
	if legacyStruct.ObjectKey != "skills/old/v1.0.0/skill.zip" {
		t.Errorf("legacy ObjectKey = %q", legacyStruct.ObjectKey)
	}
}

// --- Integration test: GetSkillMD with empty version_storage returns ErrNoFile ---

func TestGetSkillMD_EmptyVersionStorage_ReturnsErrNoFile(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "id" })

	now := time.Now()

	// Skill without version_storage (empty string)
	skillRows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage", "view_count", "download_count",
	}).AddRow(
		"empty-vs", "Empty VS", "Empty VS", "", "", "",
		"desc", "", []byte(`[]`), "user-ev", "User EV",
		"space-ev", "space", "1.0.0", "", "skill.zip",
		"skills/empty-vs/v1.0.0/skill.zip", int64(1024), "sha",
		now, now,
		"1.0.0", "", int64(0), int64(0), // empty version_storage
	)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("empty-vs").
		WillReturnRows(skillRows)

	ctx := context.Background()
	_, err = svc.GetSkillMD(ctx, "empty-vs", "space-ev", "user-ev")

	if !errors.Is(err, ErrNoFile) {
		t.Errorf("GetSkillMD with empty version_storage should return ErrNoFile, got: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// --- Integration test: PutObject is called with correct content types ---

type contentCapture struct {
	fakeStorage
	contentTypes []string
}

func (c *contentCapture) PutObject(_ context.Context, key string, _ bytes.Reader, _ int64, ct string) error {
	c.putCount++
	c.putKeys = append(c.putKeys, key)
	c.contentTypes = append(c.contentTypes, ct)
	return nil
}

// --- Integration test: Create with large zip verifies zip rewrite + SKILL.md extraction ---

func TestCreate_ZipRewrite_ExtractsSkillMD(t *testing.T) {
	// Build a zip with known SKILL.md content
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	// SKILL.md with frontmatter and body
	fw, _ := w.Create("SKILL.md")
	fw.Write([]byte("---\nname: Extract Test\ndescription: Test extraction\nversion: 1.2.3\ntags:\n  - extract\n---\n# Extract Test\n\nThis is the body that should be extracted."))

	// Another file
	fw2, _ := w.Create("main.py")
	fw2.Write([]byte("print('hello')"))

	w.Close()

	zipData := buf.Bytes()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{getData: zipData}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "extract-id" })

	parseRows := sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
		"status", "result_name", "result_description", "result_version",
		"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
		"owner_id", "space_id", "skill_id",
	}).AddRow(
		"task-ext", "upload-ext", "skill.zip", int64(len(zipData)),
		"skill-uploads/upload-ext/skill.zip", "sha",
		"success", "Extract Test", "Test extraction", "1.2.3",
		[]byte(`["extract"]`), "# Extract Test\nBody", "", "", nil, 0,
		"user-ext", "space-ext", "",
	)
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-ext").
		WillReturnRows(parseRows)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE parse_tasks SET status").
		WithArgs("task-ext").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skills").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_versions").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_tags").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ctx := context.Background()
	item, err := svc.Create(ctx, CreateParams{
		ParseTaskID: "task-ext",
		UserID:      "user-ext",
		UserName:    "Ext User",
		SpaceID:     "space-ext",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify SKILL.md was uploaded separately
	if len(store.putKeys) < 2 {
		t.Fatalf("expected at least 2 PutObject calls, got %d", len(store.putKeys))
	}
	if store.putKeys[1] != "skills/extract-id/v1.2.3/SKILL.md" {
		t.Errorf("SKILL.md key = %q, want %q", store.putKeys[1], "skills/extract-id/v1.2.3/SKILL.md")
	}

	// ReadmeContent should be extracted and set
	if item.ReadmeContent == "" {
		t.Error("ReadmeContent should be non-empty after SKILL.md extraction")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}
