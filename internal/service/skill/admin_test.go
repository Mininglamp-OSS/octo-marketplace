package skill

import (
	"context"
	"encoding/json"
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
		"admin", "Admin", "", "public", "1.0.0",
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
		"admin", "Admin", "", "public", "1.0.0",
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
		"admin", "Admin", "", "public", "1.0.0",
		"", "", "", int64(0), "",
		time.Now(), time.Now(),
		"", "",
		int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").WithArgs("sk-global").WillReturnRows(initial)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE skills SET tags = \\? WHERE id = \\?").
		WithArgs(`["official"]`, "sk-global").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_tags").
		WithArgs(skillrepo.GlobalTagSpaceID, "official", "admin").
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
		"", "", []byte(`["official"]`),
		"admin", "Admin", "", "public", "1.0.0",
		"", "", "", int64(0), "",
		time.Now(), time.Now(),
		"", "",
		int64(0), int64(0),
	)
	mock.ExpectQuery("SELECT .+ FROM skills").WithArgs("sk-global").WillReturnRows(updated)

	item, err := svc.AdminUpdate(context.Background(), "sk-global", AdminUpdateParams{
		Tags: json.RawMessage(`["official"]`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(item.Tags) != `["official"]` {
		t.Fatalf("tags = %s", item.Tags)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
