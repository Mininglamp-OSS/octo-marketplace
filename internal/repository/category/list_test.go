package category

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestListWithCount_PublicSkillsAreNotScopedToCurrentSpace(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery("LEFT JOIN skills s").
		WithArgs("space-1", "user-1", "space-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "icon_key", "sort_order", "skill_count"}).
			AddRow("cat-1", "Category 1", "icon", 10, 2))

	rows, err := New(db).ListWithCount(context.Background(), "space-1", "user-1")
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
