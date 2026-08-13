package expert

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCategoryIDByName(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectQuery("SELECT id FROM expert_categories WHERE name = \\? AND deleted_at IS NULL").
		WithArgs("研发工具").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("dev-tools"))
	id, err := repo.CategoryIDByName(context.Background(), " 研发工具 ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "dev-tools" {
		t.Fatalf("id = %q, want dev-tools", id)
	}

	// Unknown name returns empty string, not an error.
	mock.ExpectQuery("SELECT id FROM expert_categories WHERE name = \\? AND deleted_at IS NULL").
		WithArgs("不存在").
		WillReturnError(sqlNoRows())
	id, err = repo.CategoryIDByName(context.Background(), "不存在")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Fatalf("id = %q, want empty for unknown name", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCategoryNamesByIDs(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectQuery("SELECT id, name FROM expert_categories\\s+WHERE id IN \\(\\?,\\?\\) AND deleted_at IS NULL").
		WithArgs("dev-tools", "content-creation").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
			AddRow("dev-tools", "研发工具").
			AddRow("content-creation", "内容创作"))

	// Duplicate + empty ids are deduped/dropped before the query.
	names, err := repo.CategoryNamesByIDs(context.Background(), []string{"dev-tools", "", "content-creation", "dev-tools"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if names["dev-tools"] != "研发工具" || names["content-creation"] != "内容创作" {
		t.Fatalf("names = %#v", names)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCategoryNamesByIDsEmptyShortCircuits(t *testing.T) {
	repo, _, done := newMockRepo(t)
	defer done()

	// No ids → no query issued.
	names, err := repo.CategoryNamesByIDs(context.Background(), []string{"", ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("names = %#v, want empty", names)
	}
}

func TestListCategoriesWithCountAgent(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectQuery("SELECT ec.id, ec.name, COUNT\\(t.id\\).*FROM expert_categories ec\\s+LEFT JOIN experts t").
		WithArgs("space-a", "u1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "cnt"}).
			AddRow("marketing-planning", "营销策划", 3).
			AddRow("dev-tools", "研发工具", 0))

	out, err := repo.ListCategoriesWithCount(context.Background(), EntityExpert, "space-a", "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 || out[0].ID != "marketing-planning" || out[0].Count != 3 {
		t.Fatalf("out = %+v", out)
	}
	if out[1].Count != 0 {
		t.Fatalf("zero-count category must be returned: %+v", out[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListCategoriesWithCountSquadSelectsTable(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectQuery("FROM expert_categories ec\\s+LEFT JOIN expert_squads t").
		WithArgs("space-a", "u1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "cnt"}).
			AddRow("dev-tools", "研发工具", 1))

	out, err := repo.ListCategoriesWithCount(context.Background(), EntitySquad, "space-a", "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "dev-tools" {
		t.Fatalf("out = %+v", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListCategoriesWithCountQueryError(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectQuery("FROM expert_categories ec").
		WithArgs("space-a", "u1").
		WillReturnError(errors.New("boom"))

	if _, err := repo.ListCategoriesWithCount(context.Background(), EntityExpert, "space-a", "u1"); err == nil {
		t.Fatal("expected query error to propagate")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
