package skill

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
)

// TestAdminGet_RejectsNonPublic verifies that AdminGet returns ErrNotFound for
// skills that are not visibility='public'.
func TestAdminGet_RejectsNonPublic(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	store := &fakeStorage{}
	svc := New(repo, catRepo, store, func() string { return "id" })

	// Return a private skill
	rows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags",
		"owner_id", "owner_name", "space_id", "visibility", "version",
		"readme_content", "file_name", "file_url", "file_size", "file_sha256",
		"created_at", "updated_at",
		"resolved_version", "version_storage",
		"view_count", "download_count",
	}).AddRow(
		"sk-1", "private-skill", "", "", "", "",
		"desc", "", []byte(`[]`),
		"u1", "admin", "sp1", "private", "1.0.0",
		"", "", "", int64(0), "",
		time.Now(), time.Now(),
		"1.0.0", "",
		int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").WithArgs("sk-1").WillReturnRows(rows)

	_, err = svc.AdminGet(context.Background(), "sk-1")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestAdminGet_AcceptsPublic verifies that AdminGet succeeds for public skills.
func TestAdminGet_AcceptsPublic(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	store := &fakeStorage{}
	svc := New(repo, catRepo, store, func() string { return "id" })

	rows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags",
		"owner_id", "owner_name", "space_id", "visibility", "version",
		"readme_content", "file_name", "file_url", "file_size", "file_sha256",
		"created_at", "updated_at",
		"resolved_version", "version_storage",
		"view_count", "download_count",
	}).AddRow(
		"sk-2", "public-skill", "Public Skill", "", "", "v1",
		"a public skill", "cat1", []byte(`["demo"]`),
		"admin-uid", "Admin", "", "public", "1.0.0",
		"", "skill.zip", "skills/sk-2/v1.0.0/skill.zip", int64(1024), "sha",
		time.Now(), time.Now(),
		"1.0.0", "",
		int64(5), int64(10),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").WithArgs("sk-2").WillReturnRows(rows)

	item, err := svc.AdminGet(context.Background(), "sk-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.ID != "sk-2" {
		t.Fatalf("expected skill_id=sk-2, got %s", item.ID)
	}
	if item.Visibility != "public" {
		t.Fatalf("expected visibility=public, got %s", item.Visibility)
	}
}

func TestAdminDeleteDeletesAllVersionArtifacts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{}
	svc := New(skillrepo.New(db), categoryrepo.New(db), store, func() string { return "id" })
	now := time.Now()

	currentStorage := `{"type":"s3","zip_object_key":"skills/admin-skill/v2/skill.zip","skill_md_object_key":"skills/admin-skill/v2/SKILL.md"}`
	oldStorage := `{"type":"s3","zip_object_key":"skills/admin-skill/v1/skill.zip","skill_md_object_key":"skills/admin-skill/v1/SKILL.md"}`
	legacyStorage := `{"type":"s3","object_key":"skills/admin-skill/v0/legacy.zip"}`

	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("admin-skill").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at",
			"resolved_version", "version_storage",
			"view_count", "download_count",
		}).AddRow(
			"admin-skill", "octo-style", "octo-style", "", "", "v2",
			"desc", "cat1", []byte(`[]`),
			"owner-1", "Owner", "", "public", "2.0.0",
			"", "skill.zip", "skills/admin-skill/v2/skill.zip", int64(2048), "sha2",
			now, now,
			"2.0.0", currentStorage,
			int64(0), int64(0),
		))
	mock.ExpectQuery("SELECT id, skill_id, version, changelog, storage, changed_by, created_at").
		WithArgs("admin-skill").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "skill_id", "version", "changelog", "storage", "changed_by", "created_at",
		}).
			AddRow("v2", "admin-skill", "2.0.0", "", currentStorage, "admin", now).
			AddRow("v1", "admin-skill", "1.0.0", "", oldStorage, "admin", now.Add(-time.Hour)).
			AddRow("v0", "admin-skill", "0.9.0", "", legacyStorage, "admin", now.Add(-2*time.Hour)))
	mock.ExpectExec("UPDATE skills").
		WithArgs("admin-skill").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := svc.AdminDelete(context.Background(), "admin-skill"); err != nil {
		t.Fatalf("AdminDelete error = %v", err)
	}

	want := map[string]bool{
		"skills/admin-skill/v2/skill.zip":  true,
		"skills/admin-skill/v2/SKILL.md":   true,
		"skills/admin-skill/v1/skill.zip":  true,
		"skills/admin-skill/v1/SKILL.md":   true,
		"skills/admin-skill/v0/legacy.zip": true,
	}
	if len(store.deleteKeys) != len(want) {
		t.Fatalf("deleteKeys=%v, want %d keys", store.deleteKeys, len(want))
	}
	for _, key := range store.deleteKeys {
		if !want[key] {
			t.Fatalf("unexpected delete key %q in %v", key, store.deleteKeys)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing delete keys: %v", want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminReuploadNameMismatchDeletesTempObject(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{}
	svc := New(skillrepo.New(db), categoryrepo.New(db), store, func() string { return "id" })
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("admin-skill").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at",
			"resolved_version", "version_storage",
			"view_count", "download_count",
		}).AddRow(
			"admin-skill", "octo-style", "octo-style", "", "", "v1",
			"desc", "cat1", []byte(`[]`),
			"owner-1", "Owner", "", "public", "1.0.0",
			"", "skill.zip", "skills/admin-skill/v1.0.0/skill.zip", int64(1024), "sha",
			now, now,
			"1.0.0", "",
			int64(0), int64(0),
		))
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("admin-task-mismatch").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
			"status", "result_name", "result_description", "result_version",
			"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
			"owner_id", "space_id", "skill_id",
		}).AddRow(
			"admin-task-mismatch", "upload-mismatch", "skill.zip", int64(2048), "skill-uploads/upload-mismatch/admin.zip", "sha",
			"success", "gstack-guard", "desc", "2.0.0",
			[]byte(`[]`), "", "", "", nil, 0,
			"admin", "", "admin-skill",
		))

	_, err = svc.AdminReupload(context.Background(), "admin-skill", AdminReuploadParams{
		ParseTaskID: "admin-task-mismatch",
		AdminUID:    "admin",
	})
	if !errors.Is(err, ErrNameMismatch) {
		t.Fatalf("AdminReupload error = %v, want ErrNameMismatch", err)
	}
	if len(store.deleteKeys) != 1 || store.deleteKeys[0] != "skill-uploads/upload-mismatch/admin.zip" {
		t.Fatalf("deleteKeys=%v, want temp upload cleanup", store.deleteKeys)
	}
	if store.putCount != 0 {
		t.Fatalf("PutObject count=%d, want 0", store.putCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminReuploadRejectsParseTaskForOtherSkill(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{}
	svc := New(skillrepo.New(db), categoryrepo.New(db), store, func() string { return "id" })
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("admin-skill-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at",
			"resolved_version", "version_storage",
			"view_count", "download_count",
		}).AddRow(
			"admin-skill-a", "octo-style", "octo-style", "", "", "v1",
			"desc", "cat1", []byte(`[]`),
			"owner-1", "Owner", "", "public", "1.0.0",
			"", "skill.zip", "skills/admin-skill-a/v1.0.0/skill.zip", int64(1024), "sha",
			now, now,
			"1.0.0", "",
			int64(0), int64(0),
		))
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-for-b").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
			"status", "result_name", "result_description", "result_version",
			"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
			"owner_id", "space_id", "skill_id",
		}).AddRow(
			"task-for-b", "upload-b", "skill.zip", int64(2048), "skill-uploads/upload-b/admin.zip", "sha",
			"success", "octo-style", "desc", "2.0.0",
			[]byte(`[]`), "", "", "", nil, 0,
			"admin", "", "admin-skill-b",
		))

	_, err = svc.AdminReupload(context.Background(), "admin-skill-a", AdminReuploadParams{
		ParseTaskID: "task-for-b",
		AdminUID:    "admin",
	})
	if !errors.Is(err, ErrInvalidParseTask) {
		t.Fatalf("AdminReupload error = %v, want ErrInvalidParseTask", err)
	}
	if len(store.deleteKeys) != 0 {
		t.Fatalf("deleteKeys=%v, want no cleanup for another skill task", store.deleteKeys)
	}
	if store.putCount != 0 {
		t.Fatalf("PutObject count=%d, want 0", store.putCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminCreateRejectsParseTaskFromOtherTenant(t *testing.T) {
	tests := []struct {
		name      string
		taskOwner string
		taskSpace string
	}{
		{name: "other owner", taskOwner: "other-admin", taskSpace: skillrepo.GlobalTagSpaceID},
		{name: "other space", taskOwner: "admin", taskSpace: "space-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			svc := New(skillrepo.New(db), categoryrepo.New(db), &fakeStorage{}, func() string { return "id" })
			mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
				WithArgs("foreign-task").
				WillReturnRows(sqlmock.NewRows([]string{
					"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
					"status", "result_name", "result_description", "result_version",
					"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
					"owner_id", "space_id", "skill_id",
				}).AddRow(
					"foreign-task", "upload-foreign", "skill.zip", int64(1024), "skill-uploads/upload-foreign/skill.zip", "sha",
					"success", "foreign-skill", "desc", "1.0.0",
					[]byte(`[]`), "", "", "", nil, 0,
					tt.taskOwner, tt.taskSpace, "",
				))

			_, err = svc.AdminCreate(context.Background(), AdminCreateParams{
				ParseTaskID: "foreign-task",
				AdminUID:    "admin",
				AdminName:   "Admin",
			})
			if !errors.Is(err, ErrInvalidParseTask) {
				t.Fatalf("AdminCreate error = %v, want ErrInvalidParseTask", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdminReuploadRejectsParseTaskFromOtherTenant(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := New(skillrepo.New(db), categoryrepo.New(db), &fakeStorage{}, func() string { return "id" })
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("admin-skill").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
			"description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at",
			"resolved_version", "version_storage",
			"view_count", "download_count",
		}).AddRow(
			"admin-skill", "octo-style", "octo-style", "", "", "v1",
			"desc", "cat1", []byte(`[]`),
			"owner-1", "Owner", "", "public", "1.0.0",
			"", "skill.zip", "skills/admin-skill/v1.0.0/skill.zip", int64(1024), "sha",
			now, now,
			"1.0.0", "",
			int64(0), int64(0),
		))
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("foreign-task").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
			"status", "result_name", "result_description", "result_version",
			"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
			"owner_id", "space_id", "skill_id",
		}).AddRow(
			"foreign-task", "upload-foreign", "skill.zip", int64(1024), "skill-uploads/upload-foreign/skill.zip", "sha",
			"success", "octo-style", "desc", "2.0.0",
			[]byte(`[]`), "", "", "", nil, 0,
			"other-admin", skillrepo.GlobalTagSpaceID, "admin-skill",
		))

	_, err = svc.AdminReupload(context.Background(), "admin-skill", AdminReuploadParams{
		ParseTaskID: "foreign-task",
		AdminUID:    "admin",
	})
	if !errors.Is(err, ErrInvalidParseTask) {
		t.Fatalf("AdminReupload error = %v, want ErrInvalidParseTask", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestAdminGetSkillMD_RejectsNonPublic verifies that AdminGetSkillMD returns
// ErrNotFound for non-public skills.
func TestAdminGetSkillMD_RejectsNonPublic(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	store := &fakeStorage{}
	svc := New(repo, catRepo, store, func() string { return "id" })

	rows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags",
		"owner_id", "owner_name", "space_id", "visibility", "version",
		"readme_content", "file_name", "file_url", "file_size", "file_sha256",
		"created_at", "updated_at",
		"resolved_version", "version_storage",
		"view_count", "download_count",
	}).AddRow(
		"sk-priv", "space-skill", "", "", "", "",
		"desc", "", []byte(`[]`),
		"u1", "owner", "sp1", "space", "1.0.0",
		"", "", "", int64(0), "",
		time.Now(), time.Now(),
		"1.0.0", `{"type":"s3","zip_object_key":"x","skill_md_object_key":"y"}`,
		int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").WithArgs("sk-priv").WillReturnRows(rows)

	_, err = svc.AdminGetSkillMD(context.Background(), "sk-priv")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestAdminUpdate_RejectsNonPublic verifies that AdminUpdate returns ErrNotFound
// for non-public skills.
func TestAdminUpdate_RejectsNonPublic(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	store := &fakeStorage{}
	svc := New(repo, catRepo, store, func() string { return "id" })

	rows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags",
		"owner_id", "owner_name", "space_id", "visibility", "version",
		"readme_content", "file_name", "file_url", "file_size", "file_sha256",
		"created_at", "updated_at",
		"resolved_version", "version_storage",
		"view_count", "download_count",
	}).AddRow(
		"sk-priv2", "priv", "", "", "", "",
		"", "", []byte(`[]`),
		"u1", "owner", "sp1", "private", "1.0.0",
		"", "", "", int64(0), "",
		time.Now(), time.Now(),
		"", "",
		int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").WithArgs("sk-priv2").WillReturnRows(rows)

	name := "new name"
	_, err = svc.AdminUpdate(context.Background(), "sk-priv2", AdminUpdateParams{Name: &name})
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestAdminList_OnlyReturnsPublic verifies AdminList filters by visibility=public.
func TestAdminList_OnlyReturnsPublic(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	store := &fakeStorage{}
	svc := New(repo, catRepo, store, func() string { return "id" })

	// Count query
	mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// List query
	rows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags",
		"owner_id", "owner_name", "space_id", "visibility", "version",
		"readme_content", "file_name", "file_url", "file_size", "file_sha256",
		"created_at", "updated_at",
		"resolved_version", "version_storage",
		"view_count", "download_count",
	}).AddRow(
		"sk-pub", "public-skill", "", "", "", "",
		"desc", "", []byte(`[]`),
		"owner-1", "Owner", "space-1", "public", "1.0.0",
		"", "", "", int64(0), "",
		time.Now(), time.Now(),
		"1.0.0", "",
		int64(10), int64(20),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").WillReturnRows(rows)

	result, err := svc.AdminList(context.Background(), AdminListParams{Limit: 20, Sort: "latest"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected total=1, got %d", result.Total)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	if result.Items[0].ID != "sk-pub" {
		t.Fatalf("expected id=sk-pub, got %s", result.Items[0].ID)
	}
}

// TestAdminUpdate_InvalidTags verifies AdminUpdate rejects invalid tags.
func TestAdminUpdate_InvalidTags(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	store := &fakeStorage{}
	svc := New(repo, catRepo, store, func() string { return "id" })

	rows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags",
		"owner_id", "owner_name", "space_id", "visibility", "version",
		"readme_content", "file_name", "file_url", "file_size", "file_sha256",
		"created_at", "updated_at",
		"resolved_version", "version_storage",
		"view_count", "download_count",
	}).AddRow(
		"sk-up", "pub-skill", "", "", "", "",
		"", "", []byte(`[]`),
		"owner-1", "Owner", "space-1", "public", "1.0.0",
		"", "", "", int64(0), "",
		time.Now(), time.Now(),
		"", "",
		int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").WithArgs("sk-up").WillReturnRows(rows)

	_, err = svc.AdminUpdate(context.Background(), "sk-up", AdminUpdateParams{
		Tags: json.RawMessage(`"not-an-array"`),
	})
	if err != ErrInvalidTags {
		t.Fatalf("expected ErrInvalidTags, got %v", err)
	}
}

func TestAdminUpdate_UpsertsGlobalTags(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := skillrepo.New(db)
	catRepo := categoryrepo.New(db)
	store := &fakeStorage{}
	svc := New(repo, catRepo, store, func() string { return "id" })

	initial := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags",
		"owner_id", "owner_name", "space_id", "visibility", "version",
		"readme_content", "file_name", "file_url", "file_size", "file_sha256",
		"created_at", "updated_at",
		"resolved_version", "version_storage",
		"view_count", "download_count",
	}).AddRow(
		"sk-global", "pub-skill", "", "", "", "",
		"", "", []byte(`[]`),
		"owner-1", "Owner", "space-1", "public", "1.0.0",
		"", "", "", int64(0), "",
		time.Now(), time.Now(),
		"", "",
		int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").WithArgs("sk-global").WillReturnRows(initial)
	mock.ExpectBegin()
	expectResolveOrCreateTagIDs(mock, skillrepo.GlobalTagSpaceID, "owner-1", []string{"official"}, []int64{1})
	mock.ExpectExec("UPDATE skills SET tags = \\? WHERE id = \\? AND owner_id = \\? AND space_id = \\? AND is_deleted = 0").
		WithArgs(`[1]`, "sk-global", "owner-1", "space-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	updated := sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags",
		"owner_id", "owner_name", "space_id", "visibility", "version",
		"readme_content", "file_name", "file_url", "file_size", "file_sha256",
		"created_at", "updated_at",
		"resolved_version", "version_storage",
		"view_count", "download_count",
	}).AddRow(
		"sk-global", "pub-skill", "", "", "", "",
		"", "", tagIDJSON(1),
		"owner-1", "Owner", "space-1", "public", "1.0.0",
		"", "", "", int64(0), "",
		time.Now(), time.Now(),
		"", "",
		int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").WithArgs("sk-global").WillReturnRows(updated)
	expectResolveTagNames(mock, []int64{1}, []string{"official"})

	item, err := svc.AdminUpdate(context.Background(), "sk-global", AdminUpdateParams{
		Tags: json.RawMessage(`["official"]`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(item.Tags) != 1 || item.Tags[0] != "official" {
		t.Fatalf("tags = %v", item.Tags)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
