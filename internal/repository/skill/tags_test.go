package skill

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestListTagsScopesToSpaceAndFuzzyQuery(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT space_id, name, created_by, created_at, updated_at").
		WithArgs("space-1", "space-1", GlobalTagSpaceID, "%auto%", 25).
		WillReturnRows(sqlmock.NewRows([]string{"space_id", "name", "created_by", "created_at", "updated_at"}).
			AddRow("space-1", "automation", "user-1", now, now))

	rows, err := New(db).ListTags(context.Background(), "space-1", "auto", 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "automation" {
		t.Fatalf("rows = %#v", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListTagsIncludesGlobalTags(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectQuery("ROW_NUMBER\\(\\) OVER").
		WithArgs("space-1", "space-1", GlobalTagSpaceID, 50).
		WillReturnRows(sqlmock.NewRows([]string{"space_id", "name", "created_by", "created_at", "updated_at"}).
			AddRow(GlobalTagSpaceID, "official", "admin", now, now).
			AddRow("space-1", "team", "user-1", now, now))

	rows, err := New(db).ListTags(context.Background(), "space-1", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].SpaceID != GlobalTagSpaceID || rows[0].Name != "official" {
		t.Fatalf("global tag missing: %#v", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminUpdateSkillAndConsumeTaskUpsertsGlobalTags(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE parse_tasks SET status = 'consumed'").
		WithArgs("task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE skills SET tags = \\? WHERE id = \\? AND is_deleted = 0").
		WithArgs(`["official"]`, "skill-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO skill_tags").
		WithArgs(GlobalTagSpaceID, "official", "admin-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err = New(db).AdminUpdateSkillAndConsumeTask(
		context.Background(),
		"skill-1",
		"admin-1",
		UpdateParams{
			Tags:     json.RawMessage(`["official"]`),
			TagNames: []string{"official"},
		},
		"task-1",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListFiltersByAllTags(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// With comprehensive sort (default), expect a count query first, then the data query.
	mock.ExpectQuery("SELECT COUNT").
		WithArgs("space-1", "space-1", "user-1", "space-1", `"dev"`, `"ai"`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	mock.ExpectQuery("JSON_CONTAINS\\(s\\.tags, \\?\\).*JSON_CONTAINS\\(s\\.tags, \\?\\)").
		WithArgs("space-1", "space-1", "user-1", "space-1", `"dev"`, `"ai"`, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at", "view_count", "download_count",
		}))

	_, err = New(db).List(context.Background(), ListFilter{
		SpaceID: "space-1",
		UserID:  "user-1",
		Tags:    []string{"dev", "ai"},
		Limit:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListSearchMatchesTagFuzzy(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// With comprehensive sort (default), expect a count query first, then the data query.
	mock.ExpectQuery("SELECT COUNT").
		WithArgs("space-1", "space-1", "user-1", "space-1", "%auto%", "%auto%", "%auto%", "%auto%", "%auto%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	mock.ExpectQuery("JSON_SEARCH\\(s\\.tags, 'one', \\?\\) IS NOT NULL").
		WithArgs("space-1", "space-1", "user-1", "space-1", "%auto%", "%auto%", "%auto%", "%auto%", "%auto%", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at", "view_count", "download_count",
		}))

	_, err = New(db).List(context.Background(), ListFilter{
		SpaceID: "space-1",
		UserID:  "user-1",
		Query:   "auto",
		Limit:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
