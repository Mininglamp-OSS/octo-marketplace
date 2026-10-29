package migration

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// This digest was produced by the original nonlocking reader for one plugin
// row and empty owned inventories. Locking must not invalidate saved receipts.
func TestReceiptLockingPreservesExistingDigest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i, table := range []string{"plugins", "plugin_relations", "plugin_versions", "plugin_version_releases", "plugin_version_relation_refs", "plugin_version_reference_index_state", "plugin_audit_logs", "plugin_review_requests", "plugin_placements", "plugin_category_placements", "marketplace_conversion_mapping"} {
		rows := sqlmock.NewRows([]string{"plugin_id"})
		if i == 0 {
			rows.AddRow("target")
		}
		mock.ExpectQuery(`^SELECT .* FROM ` + regexp.QuoteMeta(table) + ` .* FOR UPDATE$`).WillReturnRows(rows)
	}
	got, err := stateHash(context.Background(), tx, Plan{ID: "run"}, Node{Plugin: model.Plugin{ID: "target", Type: model.PluginTypeSkill}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "9072d7aa6d7aa531aa0f411ce62bd1fa64e62e5bbb2e5b01109854aceb554bb2" {
		t.Fatalf("saved receipt digest changed: %s", got)
	}
	mock.ExpectRollback()
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
