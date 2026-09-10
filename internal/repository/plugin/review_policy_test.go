package plugin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetReviewPolicyDefaultsEnabledOnlyWhenRowIsAbsent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := New(db)
	scope := Scope{SpaceID: "space-a"}

	mock.ExpectQuery("SELECT is_auto_approve_enabled,updated_at FROM plugin_review_policies").
		WithArgs("space-a").WillReturnError(sql.ErrNoRows)
	policy, err := repo.GetReviewPolicy(context.Background(), scope)
	if err != nil || !policy.IsAutoApproveEnabled {
		t.Fatalf("policy=%#v err=%v, want enabled default", policy, err)
	}

	dbErr := errors.New("db unavailable")
	mock.ExpectQuery("SELECT is_auto_approve_enabled,updated_at FROM plugin_review_policies").
		WithArgs("space-a").WillReturnError(dbErr)
	if _, err := repo.GetReviewPolicy(context.Background(), scope); !errors.Is(err, dbErr) {
		t.Fatalf("err=%v, want wrapped database error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertReviewPolicyScopesWriteToSpace(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC)
	repo := New(db)
	repo.now = func() time.Time { return now }
	mock.ExpectExec("INSERT INTO plugin_review_policies").
		WithArgs("space-a", false, "owner-1", "Owner", now, now).
		WillReturnResult(sqlmock.NewResult(1, 1))

	policy, err := repo.UpsertReviewPolicy(context.Background(), Scope{SpaceID: "space-a"}, false, "owner-1", "Owner")
	if err != nil || policy.IsAutoApproveEnabled || policy.UpdatedAt == nil {
		t.Fatalf("policy=%#v err=%v", policy, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
