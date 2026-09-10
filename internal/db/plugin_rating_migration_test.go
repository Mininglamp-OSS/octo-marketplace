package db

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	migrate "github.com/rubenv/sql-migrate"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
)

func TestPluginRatingMigrationContract(t *testing.T) {
	raw, err := migrationsql.FS.ReadFile("20260904-00-plugin-rating.sql")
	if err != nil {
		t.Fatal(err)
	}
	sqlText := string(raw)
	for _, want := range []string{
		"-- +migrate Up", "information_schema.COLUMNS", "PREPARE add_plugin_rating", "'DO 0'",
		"ADD COLUMN `rating` TINYINT UNSIGNED NULL, ALGORITHM=INSTANT",
		"-- +migrate Down", "DROP COLUMN `rating`",
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("migration missing %q", want)
		}
	}
	upper := strings.ToUpper(sqlText)
	for _, forbidden := range []string{" AFTER `", " FIRST", "ADD CONSTRAINT", " CHECK (", "DROP CHECK"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("instant rating migration must not contain %q", strings.TrimSpace(forbidden))
		}
	}
}

func TestPluginRatingMigrationMySQL(t *testing.T) {
	database := isolatedTestDB(t)
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("migrate Up: %v", err)
	}

	var dataType, columnType, nullable string
	var defaultValue sql.NullString
	if err := database.QueryRow(`SELECT DATA_TYPE, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'plugins' AND COLUMN_NAME = 'rating'`).
		Scan(&dataType, &columnType, &nullable, &defaultValue); err != nil {
		t.Fatalf("read rating column shape: %v", err)
	}
	if dataType != "tinyint" || columnType != "tinyint unsigned" || nullable != "YES" || defaultValue.Valid {
		t.Errorf("rating column shape = data_type %q, column_type %q, nullable %q, default %#v; want tinyint unsigned NULL without default",
			dataType, columnType, nullable, defaultValue)
	}

	for rating := 1; rating <= 5; rating++ {
		id := fmt.Sprintf("rating-round-trip-%d", rating)
		if _, err := database.Exec(`INSERT INTO plugins
			(plugin_id, plugin_name, plugin_type, tags_json, owner_uid, visibility,
			 manifest_json, plugin_json, manifest_hash, plugin_hash, rating, created_at, updated_at)
			VALUES (?, ?, 'skill', JSON_ARRAY(), 'user-1', 'private',
			        JSON_OBJECT(), JSON_OBJECT(), REPEAT('a', 71), REPEAT('b', 71), ?, NOW(3), NOW(3))`,
			id, id, rating); err != nil {
			t.Fatalf("insert rating %d: %v", rating, err)
		}
		var got int
		if err := database.QueryRow("SELECT rating FROM plugins WHERE plugin_id = ?", id).Scan(&got); err != nil {
			t.Fatalf("read rating %d: %v", rating, err)
		}
		if got != rating {
			t.Errorf("rating round trip = %d, want %d", got, rating)
		}
	}

	// Simulate DDL committed but sql-migrate bookkeeping missing: deleting only
	// this row makes the guarded Up replay while the column already exists.
	if _, err := database.Exec("DELETE FROM gorp_migrations WHERE id = ?", "20260904-00-plugin-rating.sql"); err != nil {
		t.Fatalf("delete migration bookkeeping: %v", err)
	}
	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("replay guarded migration: %v", err)
	}
	if got := columnCount(t, database, "plugins", "rating"); got != 1 {
		t.Fatalf("rating column count after replay = %d, want 1", got)
	}
}
