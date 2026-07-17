package skill

import (
	"context"
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
		WithArgs("space-1", "%auto%", 25).
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

func TestListFiltersByAllTags(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery("JSON_CONTAINS\\(s\\.tags, \\?\\).*JSON_CONTAINS\\(s\\.tags, \\?\\)").
		WithArgs("space-1", "space-1", "user-1", "space-1", `"dev"`, `"ai"`, 21).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at",
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

	mock.ExpectQuery("JSON_SEARCH\\(s\\.tags, 'one', \\?\\) IS NOT NULL").
		WithArgs("space-1", "space-1", "user-1", "space-1", "%auto%", "%auto%", "%auto%", "%auto%", 21).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "display_name", "icon_url", "description", "category_id", "tags",
			"owner_id", "owner_name", "space_id", "visibility", "version",
			"readme_content", "file_name", "file_url", "file_size", "file_sha256",
			"created_at", "updated_at",
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
