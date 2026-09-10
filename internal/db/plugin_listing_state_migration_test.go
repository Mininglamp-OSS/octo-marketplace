package db

import (
	"database/sql"
	"strings"
	"testing"

	migrate "github.com/rubenv/sql-migrate"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
)

// TestPluginListingStateMigrationUpDownMySQL pins the three things the
// listing_state migration has to get right, none of which a fake store can
// express: the grandfathering the add-column default produces, the enum's value
// set, and a clean Down.
//
// The flow deliberately goes Up -> Down 3 -> seed -> Up 3 rather than seeding
// before the first Up: migrate.Exec applies the whole chain at once, so the only
// way to observe the backfill acting on pre-existing rows is to roll the tail
// migrations back, insert rows the old schema would have produced, and re-apply
// them. That also proves the Down leaves a re-appliable schema behind.
//
// listing_state ships as FOUR files (add-column / backfill / reindex /
// review-submitted-index, see 20260902-00's header for why). One newer review-
// policy migration and plugin-rating migration now follow them, so Down must
// take 6 steps to reach the listing files; Up then reapplies those same 6 tail
// migrations.
func TestPluginListingStateMigrationUpDownMySQL(t *testing.T) {
	database := isolatedTestDB(t)
	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}

	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("migrate Up: %v", err)
	}

	// Roll back rating + policy plus the four listing_state migrations.
	if n, err := migrate.ExecMax(database, "mysql", source, migrate.Down, 6); err != nil {
		t.Fatalf("migrate Down: %v", err)
	} else if n != 6 {
		t.Fatalf("migrate Down applied %d migrations, want 6", n)
	}
	if got := columnCount(t, database, "plugins", "listing_state"); got != 0 {
		t.Fatal("listing_state column still exists after Down")
	}
	// The Down must also restore the scope index to its pre-migration shape, or a
	// re-apply hits a duplicate-key error on ADD INDEX.
	if cols := indexColumns(t, database, "plugins", "idx_plugins_scope_category_created"); strings.Join(cols, ",") != "visibility,space_id,category_id,created_at" {
		t.Fatalf("scope index after Down = %v, want the pre-migration shape", cols)
	}
	// -03 adds the review-submitted index under a fresh name so it is
	// idempotent against envs that ran an earlier head (see 20260902-03's
	// header for the ERROR 1061 hazard). After Down the index must be gone.
	if cols := indexColumns(t, database, "plugin_review_requests", "idx_review_plugin_submitted_at"); len(cols) != 0 {
		t.Fatalf("idx_review_plugin_submitted_at still exists after Down: %v", cols)
	}

	// Two rows the pre-listing_state schema could produce: one live, one soft
	// deleted. Neither carries listing_state, because the column does not exist.
	for _, row := range []struct {
		id      string
		deleted string
	}{
		{id: "plugin-live", deleted: "NULL"},
		{id: "plugin-gone", deleted: "NOW(3)"},
	} {
		if _, err := database.Exec(`INSERT INTO plugins
			(plugin_id, plugin_name, plugin_type, tags_json, owner_uid, space_id, visibility,
			 manifest_json, plugin_json, manifest_hash, plugin_hash, created_at, updated_at, deleted_at)
			VALUES (?, 'Grandfathered', 'skill', JSON_ARRAY(), 'user-1', 'space-a', 'space',
			        JSON_OBJECT(), JSON_OBJECT(), REPEAT('a', 71), REPEAT('b', 71), NOW(3), NOW(3), `+row.deleted+`)`,
			row.id,
		); err != nil {
			t.Fatalf("insert %s: %v", row.id, err)
		}
	}

	if n, err := migrate.ExecMax(database, "mysql", source, migrate.Up, 6); err != nil {
		t.Fatalf("re-apply migrate Up after Down: %v", err)
	} else if n != 6 {
		t.Fatalf("re-apply applied %d migrations, want 6", n)
	}

	// Grandfathering: a live row keeps the reach it had, a soft-deleted row stays
	// fail-closed so an accidental undelete does not republish it.
	for id, want := range map[string]string{"plugin-live": "published", "plugin-gone": "draft"} {
		var got string
		if err := database.QueryRow("SELECT listing_state FROM plugins WHERE plugin_id = ?", id).Scan(&got); err != nil {
			t.Fatalf("read listing_state for %s: %v", id, err)
		}
		if got != want {
			t.Errorf("listing_state for %s = %q, want %q", id, got, want)
		}
	}

	// A fresh insert that does not name the column must land 'draft': every Go
	// write path stamps it explicitly, so the DEFAULT is only reachable from
	// hand-written SQL, where invisible is the safe outcome.
	if _, err := database.Exec(`INSERT INTO plugins
		(plugin_id, plugin_name, plugin_type, tags_json, owner_uid, space_id, visibility,
		 manifest_json, plugin_json, manifest_hash, plugin_hash, created_at, updated_at)
		VALUES ('plugin-default', 'Defaulted', 'skill', JSON_ARRAY(), 'user-1', 'space-a', 'space',
		        JSON_OBJECT(), JSON_OBJECT(), REPEAT('a', 71), REPEAT('b', 71), NOW(3), NOW(3))`,
	); err != nil {
		t.Fatalf("insert without listing_state: %v", err)
	}
	var defaulted string
	if err := database.QueryRow("SELECT listing_state FROM plugins WHERE plugin_id = 'plugin-default'").Scan(&defaulted); err != nil {
		t.Fatalf("read defaulted listing_state: %v", err)
	}
	if defaulted != "draft" {
		t.Errorf("column DEFAULT = %q, want draft (fail-closed)", defaulted)
	}

	// The item-26 guard, as a test rather than only a comment: listing_state is a
	// LISTING axis and must never absorb review vocabulary. If someone adds
	// 'pending' or 'rejected' here, a listed v1 can no longer coexist with an
	// in-review v2 and the whole review entity collapses into a status column.
	var columnType string
	if err := database.QueryRow(
		"SELECT COLUMN_TYPE FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'plugins' AND COLUMN_NAME = 'listing_state'",
	).Scan(&columnType); err != nil {
		t.Fatalf("read listing_state column type: %v", err)
	}
	if columnType != "enum('draft','published','delisted')" {
		t.Errorf("listing_state type = %q, want exactly enum('draft','published','delisted')", columnType)
	}

	// The catalog read predicate filters visibility, space_id and listing_state
	// together, so the scope index has to cover it.
	if cols := indexColumns(t, database, "plugins", "idx_plugins_scope_category_created"); strings.Join(cols, ",") != "visibility,space_id,listing_state,category_id,created_at" {
		t.Errorf("scope index = %v, want listing_state as the third column", cols)
	}
	if cols := indexColumns(t, database, "plugin_review_requests", "idx_review_plugin_submitted_at"); strings.Join(cols, ",") != "plugin_id,submitted_at" {
		t.Errorf("idx_review_plugin_submitted_at = %v, want (plugin_id, submitted_at)", cols)
	}
}

func columnCount(t *testing.T, database *sql.DB, table, column string) int {
	t.Helper()
	var count int
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?",
		table, column,
	).Scan(&count); err != nil {
		t.Fatalf("query column %s.%s: %v", table, column, err)
	}
	return count
}

// indexColumns returns an index's columns in key order, or nil when the index
// does not exist.
func indexColumns(t *testing.T, database *sql.DB, table, index string) []string {
	t.Helper()
	rows, err := database.Query(
		"SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ? ORDER BY SEQ_IN_INDEX",
		table, index,
	)
	if err != nil {
		t.Fatalf("query index %s.%s: %v", table, index, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index column: %v", err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index columns: %v", err)
	}
	return cols
}
