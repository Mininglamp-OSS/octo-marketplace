package db

import (
	"testing"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
	migrate "github.com/rubenv/sql-migrate"
)

// PR2 can be deployed with the coverage migration before PR3 adds its earlier
// numbered installation/batch migrations. Exercise sql-migrate's catch-up path
// against MySQL instead of only testing a fresh database with every file present.
func TestReferenceToInstallationDeliveryMigrationCatchupMySQL(t *testing.T) {
	database := isolatedTestDB(t)
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	all, err := source.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}
	prior := make([]*migrate.Migration, 0, len(all))
	for _, m := range all {
		if m.Id == "20261024-02-plugin-install-attempts.sql" || m.Id == "20261024-03-stock-migration-batches.sql" {
			continue
		}
		prior = append(prior, m)
	}
	if _, err := migrate.Exec(database, "mysql", migrate.MemoryMigrationSource{Migrations: prior}, migrate.Up); err != nil {
		t.Fatal(err)
	}
	if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 2 {
		t.Fatalf("PR3 catch-up: applied=%d err=%v", n, err)
	}
	for _, name := range []string{"plugin_install_attempts", "plugin_stock_migration_batches", "plugin_stock_migration_roots", "plugin_version_reference_index_state"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing table %s: %d %v", name, count, err)
		}
	}
	var releases int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases`).Scan(&releases); err != nil || releases != 0 {
		t.Fatalf("migration fabricated release: %d %v", releases, err)
	}
	if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 0 {
		t.Fatalf("repeat upgrade: applied=%d err=%v", n, err)
	}
}
