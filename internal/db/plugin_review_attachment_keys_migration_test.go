package db

import (
	"testing"

	migrate "github.com/rubenv/sql-migrate"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
)

// TestPluginReviewAttachmentKeysMigrationUpDownMySQL verifies the
// attachment_keys_json column is added to plugin_review_requests, is nullable,
// accepts a JSON object, and rolls back cleanly.
func TestPluginReviewAttachmentKeysMigrationUpDownMySQL(t *testing.T) {
	database := isolatedTestDB(t)
	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}

	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("migrate Up: %v", err)
	}

	// FK parent.
	if _, err := database.Exec(`INSERT INTO plugins
		(plugin_id, plugin_name, plugin_type, tags_json, owner_uid, space_id, visibility,
		 manifest_json, plugin_json, manifest_hash, plugin_hash, created_at, updated_at)
		VALUES ('plugin-ak', 'AK Me', 'skill', JSON_ARRAY(), 'user-1', 'space-a', 'private',
		        JSON_OBJECT(), JSON_OBJECT(), REPEAT('a', 71), REPEAT('b', 71), NOW(3), NOW(3))`,
	); err != nil {
		t.Fatalf("insert parent plugin: %v", err)
	}

	// Insert a review with NULL attachment_keys_json (declared-JSON submission,
	// no spilled files) — must succeed.
	if _, err := database.Exec(`INSERT INTO plugin_review_requests
		(review_id, plugin_id, space_id, status, kind, version,
		 manifest_json, plugin_json, attachment_keys_json, relations_json, manifest_hash, plugin_hash,
		 applicant_uid, applicant_name, submitted_at, created_at, updated_at)
		VALUES ('review-nokeys', 'plugin-ak', 'space-a', 'pending', 'first', '1.0.0',
		        JSON_OBJECT('a',1), JSON_OBJECT('b',2), NULL, JSON_ARRAY(),
		        REPEAT('a',71), REPEAT('b',71), 'user-1','Alice',NOW(3),NOW(3),NOW(3))`); err != nil {
		t.Fatalf("insert review with NULL keys: %v", err)
	}

	// Insert a review with a populated attachment_keys_json (zip-submitted
	// skill with spilled files) — must succeed.
	if _, err := database.Exec(`INSERT INTO plugin_review_requests
		(review_id, plugin_id, space_id, status, kind, version,
		 manifest_json, plugin_json, attachment_keys_json, relations_json, manifest_hash, plugin_hash,
		 applicant_uid, applicant_name, submitted_at, created_at, updated_at)
		VALUES ('review-keys', 'plugin-ak', 'space-a', 'rejected', 'upgrade', '2.0.0',
		        JSON_OBJECT('a',1), JSON_OBJECT('b',2),
		        JSON_OBJECT('images/logo.png','plugins/space-a/attachments/skill-ak-abc123.png'),
		        JSON_ARRAY(),
		        REPEAT('a',71), REPEAT('b',71), 'user-1','Alice',NOW(3),NOW(3),NOW(3))`); err != nil {
		t.Fatalf("insert review with keys: %v", err)
	}

	// Roll back until THIS migration is undone. ExecMax counts from the newest
	// applied migration. Four listing_state files and the newer review-policy
	// migration follow 20260901-01, so reaching it takes 6 steps. Appending a later
	// migration means bumping this count.
	if n, err := migrate.ExecMax(database, "mysql", source, migrate.Down, 6); err != nil {
		t.Fatalf("migrate Down: %v", err)
	} else if n != 6 {
		t.Fatalf("migrate Down applied %d migrations, want 6", n)
	}

	// The column must be gone.
	var colCount int
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'plugin_review_requests' AND COLUMN_NAME = 'attachment_keys_json'",
	).Scan(&colCount); err != nil {
		t.Fatalf("query column: %v", err)
	}
	if colCount != 0 {
		t.Error("attachment_keys_json column still exists after Down")
	}

	// Re-Up must succeed.
	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("re-apply migrate Up: %v", err)
	}
}
