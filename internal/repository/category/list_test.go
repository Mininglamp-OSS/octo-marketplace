package category

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestListWithCount_SystemSkillsAreNotScopedToCurrentSpace(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery("(?s)LEFT JOIN skills s.*s\\.is_deleted = 0.*s\\.visibility = 'system'.*s\\.visibility = 'space'.*s\\.visibility = 'private'").
		WithArgs("space-1", "user-1", "space-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "icon_key", "sort_order", "skill_count"}).
			AddRow("cat-1", "Category 1", "icon", 10, 2))

	rows, err := New(db).ListWithCount(context.Background(), ListFilter{
		SpaceID: "space-1",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows count = %d, want 1", len(rows))
	}
	if rows[0].SkillCount != 2 {
		t.Fatalf("SkillCount = %d, want 2", rows[0].SkillCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListWithCount_AppliesSearchAndTagFilters(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery("(?s)s\\.name LIKE \\? OR s\\.display_name LIKE \\?.*JSON_CONTAINS\\(s\\.tags, \\?\\).*OR.*JSON_CONTAINS\\(s\\.tags, \\?\\)").
		WithArgs("space-1", "user-1", "space-1", "%demo%", "%demo%", "12", "34").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "icon_key", "sort_order", "skill_count"}).
			AddRow("cat-1", "Category 1", "icon", 10, 1))

	rows, err := New(db).ListWithCount(context.Background(), ListFilter{
		SpaceID:     "space-1",
		UserID:      "user-1",
		Query:       "demo",
		TagIDGroups: [][]int64{{12, 34}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows count = %d, want 1", len(rows))
	}
	if rows[0].SkillCount != 1 {
		t.Fatalf("SkillCount = %d, want 1", rows[0].SkillCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListWithCount_UnmatchedTagsReturnZeroCounts(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery("(?s)LEFT JOIN skills s.*1 = 0").
		WithArgs("space-1", "user-1", "space-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "icon_key", "sort_order", "skill_count"}).
			AddRow("cat-1", "Category 1", "icon", 10, 0))

	rows, err := New(db).ListWithCount(context.Background(), ListFilter{
		SpaceID:            "space-1",
		UserID:             "user-1",
		TagFilterUnmatched: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows count = %d, want 1", len(rows))
	}
	if rows[0].SkillCount != 0 {
		t.Fatalf("SkillCount = %d, want 0", rows[0].SkillCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
