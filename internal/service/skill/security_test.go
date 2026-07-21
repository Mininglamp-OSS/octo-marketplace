package skill

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
)

func TestReadVerifiedTempZipRejectsDigestMismatch(t *testing.T) {
	zipData := makeTestZip("Secure Skill", "desc", "1.0.0")
	svc := New(nil, nil, &fakeStorage{getData: zipData}, func() string { return "id" })

	_, err := svc.readVerifiedTempZip(context.Background(), &skillrepo.ParseTaskRow{
		FileURL:    "skill-uploads/upload/skill.zip",
		FileSize:   int64(len(zipData)),
		FileSHA256: "not-the-real-digest",
	})
	if err == nil || !containsString(err.Error(), "sha256 mismatch") {
		t.Fatalf("error = %v, want sha256 mismatch", err)
	}
}

func TestReadVerifiedTempZipRejectsOversizedObject(t *testing.T) {
	zipData := makeTestZip("Secure Skill", "desc", "1.0.0")
	svc := New(nil, nil, &fakeStorage{getData: append(zipData, 'x')}, func() string { return "id" })

	_, err := svc.readVerifiedTempZip(context.Background(), &skillrepo.ParseTaskRow{
		FileURL:    "skill-uploads/upload/skill.zip",
		FileSize:   int64(len(zipData)),
		FileSHA256: testSHA256Hex(zipData),
	})
	if err == nil || !containsString(err.Error(), "file exceeds size limit") {
		t.Fatalf("error = %v, want size limit", err)
	}
}

func TestGetSkillMDRejectsOversizedObject(t *testing.T) {
	_, err := readLimited(io.LimitReader(zeroReader{}, maxSkillMDReadBytes+1), maxSkillMDReadBytes)
	if err == nil || !containsString(err.Error(), "file exceeds size limit") {
		t.Fatalf("error = %v, want size limit", err)
	}
}

func TestUpdateDuplicateVersionDoesNotDeletePublishedObjects(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	zipData := makeTestZip("Duplicate Skill", "desc", "2.0.0")
	store := &fakeStorage{getData: zipData}
	svc := New(skillrepo.New(db), categoryrepo.New(db), store, func() string { return "new-version-id" })
	now := time.Now()
	oldZipKey := "skills/skill-dup/v2.0.0/skill.zip"
	oldMDKey := "skills/skill-dup/v2.0.0/SKILL.md"

	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("skill-dup").
		WillReturnRows(skillRowsForSecurityTest().
			AddRow("skill-dup", "Duplicate Skill", "Duplicate Skill", "", "", "old-version-id",
				"desc", "cat-1", []byte(`[]`), "user-1", "User One",
				"space-1", "space", "2.0.0", "old readme", "skill.zip",
				oldZipKey, int64(len(zipData)), "oldsha", now, now,
				"2.0.0", `{"type":"s3","zip_object_key":"`+oldZipKey+`","skill_md_object_key":"`+oldMDKey+`","zip_file_name":"skill.zip","zip_size":100,"zip_sha256":"oldsha"}`, int64(0), int64(0)))
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-dup").
		WillReturnRows(parseTaskRowsForSecurityTest().
			AddRow("task-dup", "upload-dup", "skill.zip", int64(len(zipData)),
				"skill-uploads/upload-dup/skill.zip", testSHA256Hex(zipData),
				"success", "Duplicate Skill", "desc", "2.0.0",
				[]byte(`[]`), "", "", "", nil, 0,
				"user-1", "space-1", "skill-dup"))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE parse_tasks SET status").
		WithArgs("task-dup", "user-1", "space-1", "skill-dup").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE skills SET").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_versions").
		WillReturnError(errors.New("duplicate version"))
	mock.ExpectRollback()

	_, err = svc.Update(context.Background(), "skill-dup", "user-1", "space-1", UpdateParams{
		ParseTaskID: "task-dup",
	})
	if err == nil {
		t.Fatal("Update should fail on duplicate version insert")
	}
	assertDoesNotContain(t, store.putKeys, oldZipKey)
	assertDoesNotContain(t, store.putKeys, oldMDKey)
	assertDoesNotContain(t, store.deleteKeys, oldZipKey)
	assertDoesNotContain(t, store.deleteKeys, oldMDKey)
	if len(store.deleteKeys) != 2 {
		t.Fatalf("deleteKeys=%v, want cleanup of only new zip and SKILL.md", store.deleteKeys)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateDBFailureSynchronouslyDeletesNewArtifacts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	zipData := makeTestZip("Create Cleanup", "desc", "1.0.0")
	store := &fakeStorage{getData: zipData}
	ids := []string{"skill-new", "version-new"}
	idGen := func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}
	svc := New(skillrepo.New(db), categoryrepo.New(db), store, idGen)

	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-create-fail").
		WillReturnRows(parseTaskRowsForSecurityTest().
			AddRow("task-create-fail", "upload-create", "skill.zip", int64(len(zipData)),
				"skill-uploads/upload-create/skill.zip", testSHA256Hex(zipData),
				"success", "Create Cleanup", "desc", "1.0.0",
				[]byte(`[]`), "", "", "", nil, 0,
				"user-1", "space-1", ""))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE parse_tasks SET status").
		WithArgs("task-create-fail", "user-1", "space-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	_, err = svc.Create(context.Background(), CreateParams{
		ParseTaskID: "task-create-fail",
		UserID:      "user-1",
		UserName:    "User One",
		SpaceID:     "space-1",
	})
	if !errors.Is(err, ErrParseTaskConsumed) {
		t.Fatalf("Create error = %v, want ErrParseTaskConsumed", err)
	}

	wantZip := "skills/skill-new/versions/version-new/skill.zip"
	wantMD := "skills/skill-new/versions/version-new/SKILL.md"
	if len(store.deleteKeys) != 2 || store.deleteKeys[0] != wantZip || store.deleteKeys[1] != wantMD {
		t.Fatalf("deleteKeys=%v, want synchronous cleanup of %q and %q", store.deleteKeys, wantZip, wantMD)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRejectsInvalidVisibilityBeforeStorageWrites(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	zipData := makeTestZip("Invalid Visibility", "desc", "1.0.0")
	store := &fakeStorage{getData: zipData}
	svc := New(skillrepo.New(db), categoryrepo.New(db), store, func() string { return "id" })

	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-invalid-visibility").
		WillReturnRows(parseTaskRowsForSecurityTest().
			AddRow("task-invalid-visibility", "upload-invalid", "skill.zip", int64(len(zipData)),
				"skill-uploads/upload-invalid/skill.zip", testSHA256Hex(zipData),
				"success", "Invalid Visibility", "desc", "1.0.0",
				[]byte(`[]`), "", "", "", nil, 0,
				"user-1", "space-1", ""))

	_, err = svc.Create(context.Background(), CreateParams{
		ParseTaskID: "task-invalid-visibility",
		Visibility:  "system",
		UserID:      "user-1",
		UserName:    "User One",
		SpaceID:     "space-1",
	})
	if !errors.Is(err, ErrInvalidVisibility) {
		t.Fatalf("Create error = %v, want ErrInvalidVisibility", err)
	}
	if store.putCount != 0 {
		t.Fatalf("PutObject count = %d, want 0", store.putCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRejectsInaccessibleSourceBeforeStorageWrites(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	zipData := makeTestZip("Forged Source", "desc", "1.0.0")
	store := &fakeStorage{getData: zipData}
	svc := New(skillrepo.New(db), categoryrepo.New(db), store, func() string { return "id" })

	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-forged-source").
		WillReturnRows(parseTaskRowsForSecurityTest().
			AddRow("task-forged-source", "upload-forged", "skill.zip", int64(len(zipData)),
				"skill-uploads/upload-forged/skill.zip", testSHA256Hex(zipData),
				"success", "Forged Source", "desc", "1.0.0",
				[]byte(`[]`), "", "", "private-source", nil, 0,
				"user-1", "space-1", ""))
	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("private-source").
		WillReturnRows(skillRowsForSecurityTest().
			AddRow("private-source", "Private Source", "", "", "", "",
				"desc", "", []byte(`[]`), "other-user", "Other",
				"space-2", "private", "1.0.0", "", "", "",
				int64(0), "", time.Now(), time.Now(),
				"1.0.0", "", int64(0), int64(0)))

	_, err = svc.Create(context.Background(), CreateParams{
		ParseTaskID: "task-forged-source",
		UserID:      "user-1",
		UserName:    "User One",
		SpaceID:     "space-1",
	})
	if !errors.Is(err, ErrInvalidSourceSkill) {
		t.Fatalf("Create error = %v, want ErrInvalidSourceSkill", err)
	}
	if store.putCount != 0 {
		t.Fatalf("PutObject count = %d, want 0", store.putCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRejectsInvalidVisibilityBeforeStorageWrites(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &fakeStorage{}
	svc := New(skillrepo.New(db), categoryrepo.New(db), store, func() string { return "id" })

	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("skill-1").
		WillReturnRows(skillRowsForSecurityTest().
			AddRow("skill-1", "Skill", "", "", "", "",
				"desc", "", []byte(`[]`), "user-1", "User One",
				"space-1", "space", "1.0.0", "", "", "",
				int64(0), "", time.Now(), time.Now(),
				"1.0.0", "", int64(0), int64(0)))

	visibility := "system"
	_, err = svc.Update(context.Background(), "skill-1", "user-1", "space-1", UpdateParams{
		Visibility: &visibility,
	})
	if !errors.Is(err, ErrInvalidVisibility) {
		t.Fatalf("Update error = %v, want ErrInvalidVisibility", err)
	}
	if store.putCount != 0 {
		t.Fatalf("PutObject count = %d, want 0", store.putCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminReuploadDuplicateVersionDoesNotDeletePublishedObjects(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	zipData := makeTestZip("Admin Duplicate", "desc", "2.0.0")
	store := &fakeStorage{getData: zipData}
	svc := New(skillrepo.New(db), categoryrepo.New(db), store, func() string { return "admin-new-version-id" })
	now := time.Now()
	oldZipKey := "skills/admin-dup/v2.0.0/skill.zip"
	oldMDKey := "skills/admin-dup/v2.0.0/SKILL.md"

	mock.ExpectQuery("SELECT .+ FROM skills").
		WithArgs("admin-dup").
		WillReturnRows(skillRowsForSecurityTest().
			AddRow("admin-dup", "Admin Duplicate", "Admin Duplicate", "", "", "old-version-id",
				"desc", "cat-1", []byte(`[]`), "admin", "Admin",
				"", "public", "2.0.0", "old readme", "skill.zip",
				oldZipKey, int64(len(zipData)), "oldsha", now, now,
				"2.0.0", `{"type":"s3","zip_object_key":"`+oldZipKey+`","skill_md_object_key":"`+oldMDKey+`","zip_file_name":"skill.zip","zip_size":100,"zip_sha256":"oldsha"}`, int64(0), int64(0)))
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("admin-task-dup").
		WillReturnRows(parseTaskRowsForSecurityTest().
			AddRow("admin-task-dup", "upload-dup", "skill.zip", int64(len(zipData)),
				"skill-uploads/upload-dup/skill.zip", testSHA256Hex(zipData),
				"success", "Admin Duplicate", "desc", "2.0.0",
				[]byte(`[]`), "", "", "", nil, 0,
				"admin", "", "admin-dup"))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE parse_tasks SET status").
		WithArgs("admin-task-dup", "admin", "", "admin-dup").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE skills SET").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_versions").
		WillReturnError(errors.New("duplicate version"))
	mock.ExpectRollback()

	_, err = svc.AdminReupload(context.Background(), "admin-dup", AdminReuploadParams{
		ParseTaskID: "admin-task-dup",
		AdminUID:    "admin",
	})
	if err == nil {
		t.Fatal("AdminReupload should fail on duplicate version insert")
	}
	assertDoesNotContain(t, store.putKeys, oldZipKey)
	assertDoesNotContain(t, store.putKeys, oldMDKey)
	assertDoesNotContain(t, store.deleteKeys, oldZipKey)
	assertDoesNotContain(t, store.deleteKeys, oldMDKey)
	if len(store.deleteKeys) != 2 {
		t.Fatalf("deleteKeys=%v, want cleanup of only new zip and SKILL.md", store.deleteKeys)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func skillRowsForSecurityTest() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "name", "display_name", "icon_url", "source_skill_id", "current_version_id",
		"description", "category_id", "tags", "owner_id", "owner_name",
		"space_id", "visibility", "version", "readme_content", "file_name", "file_url",
		"file_size", "file_sha256", "created_at", "updated_at",
		"resolved_version", "version_storage", "view_count", "download_count",
	})
}

func parseTaskRowsForSecurityTest() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
		"status", "result_name", "result_description", "result_version",
		"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
		"owner_id", "space_id", "skill_id",
	})
}

func assertDoesNotContain(t *testing.T, values []string, disallowed string) {
	t.Helper()
	for _, value := range values {
		if value == disallowed {
			t.Fatalf("%q unexpectedly present in %v", disallowed, values)
		}
	}
}
