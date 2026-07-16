package db

import (
	"testing"

	migrate "github.com/rubenv/sql-migrate"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
)

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
