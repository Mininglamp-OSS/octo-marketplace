package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestUpsertCounts_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	repo := New(db)

	mock.ExpectExec("INSERT INTO resource_metrics").
		WithArgs("skill", "sk-1", int64(5), int64(2), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = repo.UpsertCounts(context.Background(), "skill", "sk-1", 5, 2, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}

func TestUpsertCounts_DBError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	repo := New(db)

	mock.ExpectExec("INSERT INTO resource_metrics").
		WithArgs("skill", "sk-1", int64(1), int64(0), int64(0)).
		WillReturnError(context.DeadlineExceeded)

	err = repo.UpsertCounts(context.Background(), "skill", "sk-1", 1, 0, 0)
	if err == nil {
		t.Fatal("expected error on DB failure")
	}
}

func TestUpsertCountsOnce_AppliesNewFlush(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	repo := New(db)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT IGNORE INTO resource_metric_flushes").
		WithArgs("flush-1", "skill", "sk-1", int64(5), int64(2), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO resource_metrics").
		WithArgs("skill", "sk-1", int64(5), int64(2), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := repo.UpsertCountsOnce(context.Background(), "flush-1", "skill", "sk-1", 5, 2, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}

func TestUpsertCountsOnce_SkipsDuplicateFlush(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	repo := New(db)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT IGNORE INTO resource_metric_flushes").
		WithArgs("flush-1", "skill", "sk-1", int64(5), int64(2), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	if err := repo.UpsertCountsOnce(context.Background(), "flush-1", "skill", "sk-1", 5, 2, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}

func TestDeleteAppliedFlushesBefore(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	repo := New(db)
	cutoff := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec("DELETE FROM resource_metric_flushes").
		WithArgs(cutoff).
		WillReturnResult(sqlmock.NewResult(0, 3))

	if err := repo.DeleteAppliedFlushesBefore(context.Background(), cutoff); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}
