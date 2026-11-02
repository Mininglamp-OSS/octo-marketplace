package skill

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
)

func TestManagedVersionsRejectLegacySkillWritesBeforeDependencies(t *testing.T) {
	// Nil dependencies make any database/storage access fail the test.
	svc := New(nil, nil, nil, nil).WithLegacyWritesDisabled(true)
	if _, err := svc.Create(context.Background(), CreateParams{ParseTaskID: "upload"}); !errors.Is(err, ErrVersionedWriteRequired) {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Update(context.Background(), "skill", "owner", "other-space", UpdateParams{}); !errors.Is(err, ErrVersionedWriteRequired) {
		t.Fatalf("update: %v", err)
	}
}

func TestFeatureOffLegacySkillWritesKeepOriginalValidation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := New(skillrepo.New(db), nil, nil, nil).WithLegacyWritesDisabled(false)
	mock.ExpectQuery("SELECT .+ FROM parse_tasks").WithArgs("task").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if _, err := svc.Create(context.Background(), CreateParams{ParseTaskID: "task"}); !errors.Is(err, ErrInvalidParseTask) {
		t.Fatalf("create: %v", err)
	}
	mock.ExpectQuery("SELECT .+ FROM skills").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if _, err := svc.Update(context.Background(), "skill", "owner", "space", UpdateParams{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
