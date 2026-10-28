package db

import (
	"testing"

	migrate "github.com/rubenv/sql-migrate"
)

func TestPluginNameMigrationDownRemovesDerivedObjects(t *testing.T) {
	database := isolatedTestDB(t)
	source := migrationSourceThrough(t, "20261027-00-plugin-name-uniqueness.sql")
	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema=DATABASE() AND trigger_name LIKE 'plugins_name_reservations_%'`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("trigger installation: count=%d err=%v", count, err)
	}
	if n, err := migrate.ExecMax(database, "mysql", source, migrate.Down, 1); err != nil || n != 1 {
		t.Fatalf("down: n=%d err=%v", n, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='plugin_name_reservations'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reservation table remains: count=%d err=%v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='plugins' AND column_name IN ('name_key','published_name_key','name_scope','active_name_key')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("derived columns remain: count=%d err=%v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema=DATABASE() AND trigger_name LIKE 'plugins_name_reservations_%'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("triggers remain: count=%d err=%v", count, err)
	}
}
