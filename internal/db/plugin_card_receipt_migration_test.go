package db

import (
	"context"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
	mysql "github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

func TestCardReceiptMigrationPreservesTerminalEvidence(t *testing.T) {
	// Exercise the production driver default even when CI enables multiStatements.
	config, err := mysql.ParseDSN(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	config.MultiStatements = false
	t.Setenv("TEST_MYSQL_DSN", config.FormatDSN())
	f := newCardHTTPFixture(t, false)
	review, _ := f.pending(t, "migration")
	// Existing valid receipts must survive the upgrade from the original schema.
	if _, err := f.db.Exec(`ALTER TABLE plugin_card_action_receipts ADD CONSTRAINT fk_receipt_review FOREIGN KEY (review_id) REFERENCES plugin_review_requests(review_id)`); err != nil {
		t.Fatal(err)
	}
	repo := pluginrepo.New(f.db)
	old := &model.CardActionReceipt{EventID: "6400", ReviewID: review, Decision: "approve", StoredResponse: `{"disposition":"applied","state":"approved","requester_uid":"user-1"}`, CreatedAt: time.Now()}
	if err := repo.InsertCardActionReceipt(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	all, err := source.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var target *migrate.Migration
	for _, m := range all {
		if m.Id == "20261027-01-card-terminal-receipts.sql" {
			target = m
		}
	}
	if target == nil {
		t.Fatal("terminal-receipt migration missing")
	}
	// Re-execute the actual DDL to cover a crash after DDL but before migration receipt.
	for i := 0; i < 2; i++ {
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range target.Up {
			if _, err := tx.Exec(statement); err != nil {
				_ = tx.Rollback()
				t.Fatalf("DDL replay %d: %v", i, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	missing := &model.CardActionReceipt{EventID: "6401", ReviewID: "missing-parent", Decision: "deny", StoredResponse: `{"disposition":"not_found","state":"cancelled"}`, CreatedAt: time.Now()}
	if err := repo.InsertCardActionReceipt(context.Background(), missing); err != nil {
		t.Fatal(err)
	}
	if n, err := migrate.ExecMax(f.db, "mysql", migrate.MemoryMigrationSource{Migrations: []*migrate.Migration{target}}, migrate.Down, 1); err == nil || n != 0 {
		t.Fatalf("unsafe Down: n=%d err=%v", n, err)
	}
	for _, want := range []*model.CardActionReceipt{old, missing} {
		got, err := repo.GetCardActionReceipt(context.Background(), want.EventID)
		if err != nil || got == nil || got.StoredResponse != want.StoredResponse || got.ReviewID != want.ReviewID {
			t.Fatalf("receipt lost: %+v %v", got, err)
		}
	}
	if n, err := migrate.Exec(f.db, "mysql", source, migrate.Up); err != nil || n != 0 {
		t.Fatalf("repeated Up: %d %v", n, err)
	}
}
