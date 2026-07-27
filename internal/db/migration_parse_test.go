package db

import (
	"strings"
	"testing"

	migrate "github.com/rubenv/sql-migrate"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
)

func TestCurrentVersionBackfillUsesExplicitCollation(t *testing.T) {
	const migration = "20260717-06-backfill-current-version.sql"

	content, err := migrationsql.FS.ReadFile(migration)
	if err != nil {
		t.Fatalf("ReadFile(%q) error=%v", migration, err)
	}

	const collation = "COLLATE utf8mb4_unicode_ci"
	if got, want := strings.Count(string(content), collation), 8; got != want {
		t.Fatalf("%s contains %d explicit collations, want %d", migration, got, want)
	}
}

func TestMarketplaceCreateTablesUseExplicitUnicodeCollation(t *testing.T) {
	migrations := []string{
		"20260714-01-skill-marketplace.sql",
		"20260715-03-skill-versions.sql",
		"20260717-02-skill-tags.sql",
		"20260717-07-resource-metrics.sql",
		"20260721-00-resource-metric-flushes.sql",
	}

	for _, migration := range migrations {
		t.Run(migration, func(t *testing.T) {
			content, err := migrationsql.FS.ReadFile(migration)
			if err != nil {
				t.Fatalf("ReadFile(%q) error=%v", migration, err)
			}

			text := string(content)
			createTables := strings.Count(text, "CREATE TABLE")
			if createTables == 0 {
				t.Fatalf("%s has no CREATE TABLE statements", migration)
			}
			if got := strings.Count(text, "COLLATE=utf8mb4_unicode_ci"); got < createTables {
				t.Fatalf("%s has %d CREATE TABLE statements but only %d explicit table collations", migration, createTables, got)
			}
		})
	}
}

// TestMigrationsParseUpDown verifies that all embedded migration files can be
// read and that each file has both Up and Down sections.
func TestMigrationsParseUpDown(t *testing.T) {
	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}

	migrations, err := source.FindMigrations()
	if err != nil {
		t.Fatalf("FindMigrations() error=%v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations found")
	}

	for _, m := range migrations {
		t.Run(m.Id, func(t *testing.T) {
			if len(m.Up) == 0 {
				t.Errorf("migration %s: empty Up section", m.Id)
			}
			if len(m.Down) == 0 {
				t.Errorf("migration %s: empty Down section", m.Id)
			}
		})
	}
}

// TestMigrationOrderAndCount verifies that the expected migration files exist
// in the correct order.
func TestMigrationOrderAndCount(t *testing.T) {
	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}

	migrations, err := source.FindMigrations()
	if err != nil {
		t.Fatalf("FindMigrations() error=%v", err)
	}

	// We expect at least the baseline + skill-marketplace migration.
	if got := len(migrations); got < 2 {
		t.Fatalf("want at least 2 migrations, got %d", got)
	}

	expectedIDs := []string{
		"20260714-00-baseline.sql",
		"20260714-01-skill-marketplace.sql",
	}
	for i, wantID := range expectedIDs {
		if i >= len(migrations) {
			t.Fatalf("missing migration at index %d: want %s", i, wantID)
		}
		if migrations[i].Id != wantID {
			t.Errorf("migration[%d].Id=%s want=%s", i, migrations[i].Id, wantID)
		}
	}
}
