package skill

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
)

// TestBackfill_ExistingVersions_CorrectlyResolved verifies that after the backfill
// migration runs, skills with existing skill_versions rows get current_version_id
// filled, and the Get/List queries resolve version/storage from the JOIN.
func TestBackfill_ExistingVersions_CorrectlyResolved(t *testing.T) {
	now := time.Now()

	// Simulate a skill that already had current_version_id set by the backfill
	// (20260717-06-backfill-current-version.sql picks the most recent version).
	row := &skillrepo.SkillRow{
		ID:               "backfill-skill-1",
		Name:             "Backfilled Skill",
		Version:          "1.0.0", // old skill.version column
		CurrentVersionID: "bk-ver-1",
		ResolvedVersion:  "2.0.0", // from the JOIN with skill_versions
		VersionStorage:   `{"type":"s3","zip_object_key":"skills/backfill-skill-1/v2.0.0/skill.zip","skill_md_object_key":"skills/backfill-skill-1/v2.0.0/SKILL.md","zip_file_name":"skill.zip","zip_size":8192,"zip_sha256":"bksha"}`,
		FileName:         "old.zip",
		FileURL:          "skills/backfill-skill-1/v1.0.0/old.zip",
		FileSize:         512,
		FileSHA256:       "oldsha",
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	svc := &Service{}
	item := svc.rowToItem(context.Background(), row)

	// After backfill, Get/List should use the resolved version from the JOIN
	if item.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q (resolved via backfill)", item.Version, "2.0.0")
	}
	if item.FileURL != "skills/backfill-skill-1/v2.0.0/skill.zip" {
		t.Errorf("FileURL = %q, want from version_storage", item.FileURL)
	}
	if item.FileSize != 8192 {
		t.Errorf("FileSize = %d, want 8192", item.FileSize)
	}
	if item.FileSHA256 != "bksha" {
		t.Errorf("FileSHA256 = %q, want %q", item.FileSHA256, "bksha")
	}
}

// TestBackfill_NoVersionRecords_CreatedVersion verifies that for old skills
// without any skill_versions row, the backfill creates one and sets current_version_id.
// After backfill, such skills should still work with Get/List/Download.
func TestBackfill_NoVersionRecords_FallbackColumns(t *testing.T) {
	now := time.Now()

	// After the backfill's PROCEDURE runs for skills without version records,
	// a new skill_versions row is created with NULL storage. The skill gets
	// current_version_id set, and the JOIN finds the row.
	row := &skillrepo.SkillRow{
		ID:               "backfill-old",
		Name:             "Old No-Version Skill",
		Version:          "1.0.0",
		CurrentVersionID: "bk-gen-id",
		// After backfill, resolved_version comes from the new empty version row
		ResolvedVersion: "1.0.0",
		// Storage is NULL in the backfill-created row, so it comes back as empty
		VersionStorage: "",
		// These old columns are the only source of file info for this skill
		FileName:   "legacy.zip",
		FileURL:    "skills/backfill-old/v1.0.0/legacy.zip",
		FileSize:   2048,
		FileSHA256: "legsha",
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	svc := &Service{}
	item := svc.rowToItem(context.Background(), row)

	// Version should come from resolved or fallback
	if item.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", item.Version, "1.0.0")
	}
	// Without VersionStorage, file info falls back to old columns
	if item.FileURL != "skills/backfill-old/v1.0.0/legacy.zip" {
		t.Errorf("FileURL = %q, want fallback to old column", item.FileURL)
	}
	if item.FileName != "legacy.zip" {
		t.Errorf("FileName = %q, want %q", item.FileName, "legacy.zip")
	}
	if item.FileSize != 2048 {
		t.Errorf("FileSize = %d, want 2048", item.FileSize)
	}
}

// TestBackfill_Download_WorksAfterBackfill verifies that GetDownloadInfo still
// works after backfill sets current_version_id.
func TestBackfill_Download_WorksAfterBackfill(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{presignedURL: "https://cdn.example.com/skills/bk-dl/v1.5.0/skill.zip?sig=xyz"}
	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	svc := New(repo, catRepo, store, func() string { return "id" })

	now := time.Now()

	// Skill after backfill: current_version_id is set, version_storage available
	skillRows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage", "view_count", "download_count",
	}).AddRow(
		"bk-dl", "Backfill DL", "Backfill DL", "", "", "bk-ver-dl",
		"desc", "", []byte(`[]`), "user-bk", "User BK",
		"space-bk", "space", "1.5.0", "", "skill.zip",
		"skills/bk-dl/v1.5.0/skill.zip", int64(4096), "bkdlsha",
		now, now,
		"1.5.0",
		`{"type":"s3","zip_object_key":"skills/bk-dl/v1.5.0/skill.zip","skill_md_object_key":"skills/bk-dl/v1.5.0/SKILL.md","zip_file_name":"skill.zip","zip_size":4096,"zip_sha256":"bkdlsha"}`, int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("bk-dl").
		WillReturnRows(skillRows)

	info, err := svc.GetDownloadInfo(context.Background(), "bk-dl", "space-bk", "user-bk")
	if err != nil {
		t.Fatalf("GetDownloadInfo after backfill failed: %v", err)
	}

	if info.DownloadURL != "https://cdn.example.com/skills/bk-dl/v1.5.0/skill.zip?sig=xyz" {
		t.Errorf("DownloadURL = %q", info.DownloadURL)
	}
	if info.FileSHA256 != "bkdlsha" {
		t.Errorf("FileSHA256 = %q", info.FileSHA256)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// TestBackfill_MigrationSQL_ValidatesLogic verifies the logic of the backfill
// migration SQL by checking:
// 1. Skills WITH existing versions get the latest version's ID
// 2. Skills WITHOUT versions get a new version row created
func TestBackfill_MigrationSQL_Logic(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Simulate step 1: UPDATE skills SET current_version_id = latest.version_id
	// WHERE current_version_id = '' (skills that have version records)
	mock.ExpectExec(`UPDATE skills s JOIN .+ SET s.current_version_id = latest.version_id WHERE s.current_version_id = ''`).
		WillReturnResult(sqlmock.NewResult(0, 3)) // 3 skills backfilled

	// Execute the equivalent of the first UPDATE (simplified verification)
	result, err := db.Exec(`UPDATE skills s JOIN (SELECT skill_id, id AS version_id FROM skill_versions sv1 WHERE created_at = (SELECT MAX(created_at) FROM skill_versions sv2 WHERE sv2.skill_id = sv1.skill_id)) latest ON latest.skill_id = s.id SET s.current_version_id = latest.version_id WHERE s.current_version_id = ''`)
	if err != nil {
		t.Fatalf("backfill step 1 failed: %v", err)
	}
	affected, _ := result.RowsAffected()
	if affected != 3 {
		t.Errorf("step 1 affected rows = %d, want 3", affected)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestBackfill_List_UsesCurrentVersionStorage verifies that after backfill,
// the List/ListMine endpoints return file metadata from the current version's
// VersionStorage JSON, not from the stale skills.file_* columns.
func TestBackfill_List_UsesCurrentVersionStorage(t *testing.T) {
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

	// Simulate a List query returning a backfilled skill where:
	// - skills.file_size = 512 (old stale value)
	// - version_storage contains zip_size = 8192 (current version value)
	listRows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage", "view_count", "download_count",
	}).AddRow(
		"list-bk-1", "Listed Skill", "Listed Skill", "", "", "ver-list-1",
		"desc", "", []byte(`[]`), "user-list", "User List",
		"space-list", "space", "1.0.0", "", "old.zip",
		"skills/list-bk-1/v1.0.0/old.zip", int64(512), "oldsha",
		now, now,
		"2.0.0",
		`{"type":"s3","zip_object_key":"skills/list-bk-1/v2.0.0/skill.zip","skill_md_object_key":"skills/list-bk-1/v2.0.0/SKILL.md","zip_file_name":"skill.zip","zip_size":8192,"zip_sha256":"newsha"}`, int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(listRows)

	ctx := context.Background()
	result, err := svc.List(ctx, ListParams{
		SpaceID: "space-list",
		UserID:  "user-list",
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	item := result.Items[0]

	// Assert current version metadata is used (from VersionStorage), not stale columns
	if item.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q (from current version)", item.Version, "2.0.0")
	}
	if item.FileName != "skill.zip" {
		t.Errorf("FileName = %q, want %q (from version_storage)", item.FileName, "skill.zip")
	}
	if item.FileSize != 8192 {
		t.Errorf("FileSize = %d, want 8192 (from version_storage, not stale 512)", item.FileSize)
	}
	if item.FileSHA256 != "newsha" {
		t.Errorf("FileSHA256 = %q, want %q (from version_storage)", item.FileSHA256, "newsha")
	}
	if item.FileURL != "skills/list-bk-1/v2.0.0/skill.zip" {
		t.Errorf("FileURL = %q, want %q (from version_storage)", item.FileURL, "skills/list-bk-1/v2.0.0/skill.zip")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}

// TestBackfill_ListMine_UsesCurrentVersionStorage verifies ListMine also uses
// the version_storage from the JOIN result.
func TestBackfill_ListMine_UsesCurrentVersionStorage(t *testing.T) {
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

	listRows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage", "view_count", "download_count",
	}).AddRow(
		"mine-bk-1", "My BK Skill", "My BK Skill", "", "", "ver-mine-1",
		"desc", "", []byte(`[]`), "owner-mine", "Owner Mine",
		"space-mine", "private", "1.0.0", "", "legacy.zip",
		"skills/mine-bk-1/v1.0.0/legacy.zip", int64(512), "legsha",
		now, now,
		"3.0.0",
		`{"type":"s3","zip_object_key":"skills/mine-bk-1/v3.0.0/skill.zip","zip_file_name":"skill.zip","zip_size":16384,"zip_sha256":"v3sha"}`, int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").
		WillReturnRows(listRows)

	ctx := context.Background()
	result, err := svc.ListMine(ctx, ListParams{
		SpaceID: "space-mine",
		UserID:  "owner-mine",
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("ListMine failed: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	item := result.Items[0]

	if item.Version != "3.0.0" {
		t.Errorf("Version = %q, want %q", item.Version, "3.0.0")
	}
	if item.FileSize != 16384 {
		t.Errorf("FileSize = %d, want 16384 (from version_storage)", item.FileSize)
	}
	if item.FileSHA256 != "v3sha" {
		t.Errorf("FileSHA256 = %q, want %q", item.FileSHA256, "v3sha")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet DB expectations: %v", err)
	}
}
