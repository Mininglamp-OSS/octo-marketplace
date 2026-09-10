package db

import (
	"testing"

	migrate "github.com/rubenv/sql-migrate"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
)

var pluginReviewTables = []string{
	"plugin_review_requests",
	"plugin_card_action_receipts",
}

// TestPluginReviewMigrationUpDownMySQL runs the full migration chain against a
// real MySQL, then rolls back only the review migration. The chain (rather than
// the single file) is required because plugin_review_requests carries a foreign
// key to plugins.
//
// The load-bearing assertion is the single-pending constraint: MySQL has no
// partial indexes, so it is expressed as a generated column projecting NULL for
// every non-pending or soft-deleted row. Getting that wrong is invisible in a
// parse test and only shows up as duplicate pending requests in production.
func TestPluginReviewMigrationUpDownMySQL(t *testing.T) {
	database := isolatedTestDB(t)
	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}

	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("migrate Up: %v", err)
	}
	for _, table := range pluginReviewTables {
		var count int
		if err := database.QueryRow(
			"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&count); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s not found after Up", table)
		}
	}

	// FK parent.
	if _, err := database.Exec(`INSERT INTO plugins
		(plugin_id, plugin_name, plugin_type, tags_json, owner_uid, space_id, visibility,
		 manifest_json, plugin_json, manifest_hash, plugin_hash, created_at, updated_at)
		VALUES ('plugin-r1', 'Review Me', 'skill', JSON_ARRAY(), 'user-1', 'space-a', 'private',
		        JSON_OBJECT('a', 1), JSON_OBJECT('b', 2), REPEAT('a', 71), REPEAT('b', 71), NOW(3), NOW(3))`,
	); err != nil {
		t.Fatalf("insert parent plugin: %v", err)
	}

	insertReview := func(id, status string) error {
		_, err := database.Exec(`INSERT INTO plugin_review_requests
			(review_id, plugin_id, space_id, status, kind, version,
			 manifest_json, plugin_json, attachment_keys_json, relations_json, manifest_hash, plugin_hash,
			 applicant_uid, applicant_name, submitted_at, created_at, updated_at)
			VALUES (?, 'plugin-r1', 'space-a', ?, 'first', '1.0.0',
			        JSON_OBJECT('a', 1), JSON_OBJECT('b', 2), NULL, JSON_ARRAY(), REPEAT('a', 71), REPEAT('b', 71),
			        'user-1', 'Alice', NOW(3), NOW(3), NOW(3))`, id, status)
		return err
	}

	if err := insertReview("review-1", "pending"); err != nil {
		t.Fatalf("insert first pending review: %v", err)
	}
	// A second pending row for the same plugin must collide.
	if err := insertReview("review-2", "pending"); !isMySQLDuplicate(err) {
		t.Fatalf("second pending review error=%v, want MySQL 1062", err)
	}
	// Terminal rows project NULL and coexist freely — several rejected requests
	// plus one live pending is the normal resubmission history.
	if err := insertReview("review-3", "rejected"); err != nil {
		t.Fatalf("insert rejected review: %v", err)
	}
	if err := insertReview("review-4", "rejected"); err != nil {
		t.Fatalf("insert second rejected review: %v", err)
	}
	if err := insertReview("review-5", "canceled"); err != nil {
		t.Fatalf("insert canceled review: %v", err)
	}
	if err := insertReview("review-8", "approved"); err != nil {
		t.Fatalf("insert approved review: %v", err)
	}
	// Once the pending row leaves pending, a fresh submission is allowed again.
	if _, err := database.Exec(`UPDATE plugin_review_requests SET status='rejected' WHERE review_id='review-1'`); err != nil {
		t.Fatalf("reject review-1: %v", err)
	}
	if err := insertReview("review-6", "pending"); err != nil {
		t.Fatalf("insert pending review after previous left pending: %v", err)
	}
	// Soft-deleting a pending row also frees the slot.
	if _, err := database.Exec(`UPDATE plugin_review_requests SET deleted_at=NOW(3) WHERE review_id='review-6'`); err != nil {
		t.Fatalf("soft-delete review-6: %v", err)
	}
	if err := insertReview("review-7", "pending"); err != nil {
		t.Fatalf("insert pending review after soft delete: %v", err)
	}
	// The relation snapshot must be an ARRAY: an object there would make the
	// approve path's unmarshal fail at decision time rather than at submit.
	if _, err := database.Exec(`INSERT INTO plugin_review_requests
		(review_id, plugin_id, space_id, status, kind, version,
		 manifest_json, plugin_json, attachment_keys_json, relations_json, manifest_hash, plugin_hash,
		 applicant_uid, applicant_name, submitted_at, created_at, updated_at)
		VALUES ('review-bad', 'plugin-r1', 'space-a', 'rejected', 'first', '1.0.0',
		        JSON_OBJECT('a', 1), JSON_OBJECT('b', 2), NULL, JSON_OBJECT('not','an array'),
		        REPEAT('a', 71), REPEAT('b', 71), 'user-1', 'Alice', NOW(3), NOW(3), NOW(3))`,
	); err == nil {
		t.Error("relations_json accepted a JSON object; the ARRAY check is missing")
	}

	// Receipt idempotency: event_id is the primary key, so a replayed event
	// collides rather than double-applying a decision.
	insertReceipt := func(eventID string) error {
		_, err := database.Exec(`INSERT INTO plugin_card_action_receipts
			(event_id, review_id, decision, stored_response, created_at)
			VALUES (?, 'review-7', 'approve', '{"disposition":"applied"}', NOW(3))`, eventID)
		return err
	}
	if err := insertReceipt("7180000000000000001"); err != nil {
		t.Fatalf("insert receipt: %v", err)
	}
	if err := insertReceipt("7180000000000000001"); !isMySQLDuplicate(err) {
		t.Fatalf("duplicate event_id error=%v, want MySQL 1062", err)
	}

	// Roll back the review feature's migrations; the rest of the chain stays
	// applied. The feature spans TWO files — 20260901-00 creates the tables and
	// 20260901-01 adds attachment_keys_json — and ExecMax counts from the newest
	// applied migration, so rolling back only one would drop the column and leave
	// both tables standing. Appending a later migration means bumping this count:
	// the listing_state step is now split into four tail files
	// (20260902-00/-01/-02/-03), so reaching 20260901-00 takes 6 steps (4 listing
	// + 2 review-feature files). The newer review-policy migration adds one more
	// tail step, so reaching 20260901-00 now takes 7.
	if n, err := migrate.ExecMax(database, "mysql", source, migrate.Down, 7); err != nil {
		t.Fatalf("migrate Down: %v", err)
	} else if n != 7 {
		t.Fatalf("migrate Down applied %d migrations, want 7", n)
	}
	for _, table := range pluginReviewTables {
		var count int
		if err := database.QueryRow(
			"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&count); err != nil {
			t.Fatalf("query table %s after Down: %v", table, err)
		}
		if count != 0 {
			t.Errorf("table %s still exists after Down", table)
		}
	}
	// The chain below the review migration must survive the rollback.
	var plugins int
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'plugins'",
	).Scan(&plugins); err != nil {
		t.Fatalf("query plugins after Down: %v", err)
	}
	if plugins != 1 {
		t.Error("rolling back the review migration dropped the plugins table")
	}
	// And Up must be re-appliable after the rollback (a Down that leaves the
	// generated column or an index behind fails here).
	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("re-apply migrate Up after Down: %v", err)
	}
}
