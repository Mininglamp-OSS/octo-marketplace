package skill

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDeleteSoftDeletesLiveSkill(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE skills").
		WithArgs("skill-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	affected, err := New(db).Delete(context.Background(), "skill-1")
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteAlreadyDeletedSkillAffectsZeroRows(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("WHERE id = \\? AND is_deleted = 0").
		WithArgs("skill-1").
		WillReturnResult(sqlmock.NewResult(0, 0))

	affected, err := New(db).Delete(context.Background(), "skill-1")
	if err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("affected = %d, want 0", affected)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
