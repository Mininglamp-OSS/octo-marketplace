package expert

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/go-sql-driver/mysql"
)

func sqlNoRows() error { return sql.ErrNoRows }

func newMockRepo(t *testing.T) (*Repo, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	return New(db), mock, func() { db.Close() }
}

func TestMapDuplicateName(t *testing.T) {
	expertDup := &mysql.MySQLError{
		Number:  mysqlErrDupEntry,
		Message: "Duplicate entry 'u1-space-a-x' for key 'experts.uq_expert_owner_space_name_live'",
	}
	if !errors.Is(mapDuplicateName(expertDup), ErrNameTaken) {
		t.Fatal("expert name constraint must map to ErrNameTaken")
	}
	squadDup := &mysql.MySQLError{
		Number:  mysqlErrDupEntry,
		Message: "Duplicate entry 'u1-space-a-x' for key 'expert_squads.uq_squad_owner_space_name_live'",
	}
	if !errors.Is(mapDuplicateName(squadDup), ErrNameTaken) {
		t.Fatal("squad name constraint must map to ErrNameTaken")
	}
	systemExpertDup := &mysql.MySQLError{
		Number:  mysqlErrDupEntry,
		Message: "Duplicate entry 'x' for key 'experts.uq_expert_system_name_live'",
	}
	if !errors.Is(mapDuplicateName(systemExpertDup), ErrNameTaken) {
		t.Fatal("system expert name constraint must map to ErrNameTaken")
	}
	systemSquadDup := &mysql.MySQLError{
		Number:  mysqlErrDupEntry,
		Message: "Duplicate entry 'x' for key 'expert_squads.uq_squad_system_name_live'",
	}
	if !errors.Is(mapDuplicateName(systemSquadDup), ErrNameTaken) {
		t.Fatal("system squad name constraint must map to ErrNameTaken")
	}
	primaryDup := &mysql.MySQLError{
		Number:  mysqlErrDupEntry,
		Message: "Duplicate entry 'id' for key 'experts.PRIMARY'",
	}
	if errors.Is(mapDuplicateName(primaryDup), ErrNameTaken) {
		t.Fatal("unrelated unique constraint must not map to ErrNameTaken")
	}
}

func TestCreateExpertMapsDuplicate(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO experts").
		WillReturnError(&mysql.MySQLError{
			Number:  mysqlErrDupEntry,
			Message: "Duplicate entry for key 'experts.uq_expert_owner_space_name_live'",
		})
	mock.ExpectRollback()

	err := repo.CreateExpert(context.Background(), &model.Expert{
		ID: "e1", Name: "x", OwnerUID: "u1", SpaceID: "space-a",
		Visibility: model.VisibilityPublic, CreatedByType: model.CreatedByHuman,
	})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateExpertSuccessResolvesTags(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectBegin()
	// Tag "架构" resolves to an existing dictionary id 5 (no insert).
	mock.ExpectQuery("SELECT id\\s+FROM expert_tags").
		WithArgs("架构", GlobalTagSpaceID, "space-a", "space-a").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(5)))
	mock.ExpectExec("INSERT INTO experts").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.CreateExpert(context.Background(), &model.Expert{
		ID: "e1", Name: "x", OwnerUID: "u1", SpaceID: "space-a", Tags: []string{"架构"},
		Visibility: model.VisibilityPublic, CreatedByType: model.CreatedByHuman,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateExpertInsertsMissingTag(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectBegin()
	// Not found → insert → re-resolve.
	mock.ExpectQuery("SELECT id\\s+FROM expert_tags").
		WithArgs("新标签", GlobalTagSpaceID, "space-a", "space-a").
		WillReturnError(sqlNoRows())
	mock.ExpectExec("INSERT INTO expert_tags").
		WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectQuery("SELECT id\\s+FROM expert_tags").
		WithArgs("新标签", GlobalTagSpaceID, "space-a", "space-a").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectExec("INSERT INTO experts").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.CreateExpert(context.Background(), &model.Expert{
		ID: "e1", Name: "x", OwnerUID: "u1", SpaceID: "space-a", Tags: []string{"新标签"},
		Visibility: model.VisibilityPublic, CreatedByType: model.CreatedByHuman,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetExpertByIDNotFound(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectQuery("SELECT .* FROM experts WHERE id = \\? AND deleted_at IS NULL").
		WithArgs("missing").
		WillReturnError(sqlNoRows())

	_, err := repo.GetExpertByID(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetExpertByIDScans(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	now := time.Now().UTC()
	rows := sqlmock.NewRows([]string{
		"id", "short_name", "name", "summary", "category_id", "tags", "publisher",
		"owner_uid", "creator_name", "created_by_type", "created_by_bot_uid", "created_by_bot_name",
		"space_id", "visibility", "instruction", "mcp_config", "skills_json", "created_at", "updated_at", "deleted_at",
	}).AddRow(
		"e1", "后端", "后端架构师", "评审", "dev", "[]", "Octo",
		"u1", "王决", "human", nil, nil,
		"space-a", "public", "你是……", `{"mcpServers":{}}`, `[{"name":"清单","file_key":""}]`, now, now, nil,
	)
	mock.ExpectQuery("SELECT .* FROM experts WHERE id = \\?").WithArgs("e1").WillReturnRows(rows)

	m, err := repo.GetExpertByID(context.Background(), "e1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "后端架构师" || m.Visibility != model.VisibilityPublic {
		t.Fatalf("scan wrong: %+v", m)
	}
	if len(m.Skills) != 1 || m.Skills[0].Name != "清单" {
		t.Fatalf("skills scan wrong: %+v", m.Skills)
	}
	if len(m.Tags) != 0 {
		t.Fatalf("tags should be empty: %#v", m.Tags)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListExpertsCountsAndScans(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM experts WHERE").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	now := time.Now().UTC()
	rows := sqlmock.NewRows([]string{
		"id", "short_name", "name", "summary", "category_id", "tags", "publisher",
		"owner_uid", "creator_name", "created_by_type", "created_by_bot_uid", "created_by_bot_name",
		"space_id", "visibility", "instruction", "mcp_config", "skills_json", "created_at", "updated_at", "deleted_at",
	}).AddRow(
		"e1", "后端", "后端架构师", "评审", "dev", "[]", "Octo",
		"u1", "王决", "human", nil, nil,
		"space-a", "public", "", "", "[]", now, now, nil,
	)
	mock.ExpectQuery("SELECT .* FROM experts WHERE .* ORDER BY created_at DESC, id DESC LIMIT \\? OFFSET \\?").
		WillReturnRows(rows)

	items, total, err := repo.ListExperts(context.Background(), ListFilter{
		CallerUID: "u1", SpaceID: "space-a", Limit: 20, Offset: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != "e1" {
		t.Fatalf("list wrong: total=%d items=%+v", total, items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateExpertNotFound(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE experts SET").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err := repo.UpdateExpert(context.Background(), &model.Expert{
		ID: "gone", Name: "x", OwnerUID: "u1", SpaceID: "space-a", Visibility: model.VisibilityPublic,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteExpertSoftDeletes(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectExec("UPDATE experts SET deleted_at = \\?, updated_at = \\? WHERE id = \\? AND owner_uid = \\? AND space_id = \\? AND visibility <> .system. AND deleted_at IS NULL").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "e1", "u1", "space-a").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.DeleteExpert(context.Background(), "e1", "u1", "space-a", time.Now()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteExpertAlreadyGone(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectExec("UPDATE experts SET deleted_at").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := repo.DeleteExpert(context.Background(), "e1", "u1", "space-a", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateSquadMapsDuplicate(t *testing.T) {
	repo, mock, done := newMockRepo(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO expert_squads").
		WillReturnError(&mysql.MySQLError{
			Number:  mysqlErrDupEntry,
			Message: "Duplicate entry for key 'expert_squads.uq_squad_owner_space_name_live'",
		})
	mock.ExpectRollback()

	err := repo.CreateSquad(context.Background(), &model.Squad{
		ID: "s1", Name: "x", OwnerUID: "u1", SpaceID: "space-a",
		Visibility: model.VisibilityPublic, CreatedByType: model.CreatedByHuman,
	})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
