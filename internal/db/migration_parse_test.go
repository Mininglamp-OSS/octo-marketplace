package db

import (
	"strings"
	"testing"

	migrate "github.com/rubenv/sql-migrate"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
)

// TestPerSaveVersionMigrationRelaxesUniqueVersion locks that the per-save
// snapshot migration drops the (plugin_id, version) unique key for a plain index
// (Up) and restores the unique key on rollback (Down).
func TestPerSaveVersionMigrationRelaxesUniqueVersion(t *testing.T) {
	const migration = "20260827-01-plugin-version-per-save.sql"
	content, err := migrationsql.FS.ReadFile(migration)
	if err != nil {
		t.Fatalf("ReadFile(%q) error=%v", migration, err)
	}
	text := string(content)
	up, down, found := strings.Cut(text, "-- +migrate Down")
	if !found {
		t.Fatal("migration missing Down section")
	}
	for _, want := range []string{
		"DROP INDEX `uq_plugin_versions_plugin_version`",
		"ADD INDEX `idx_plugin_versions_plugin_version` (`plugin_id`, `version`)",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("Up missing %q", want)
		}
	}
	for _, want := range []string{
		"DROP INDEX `idx_plugin_versions_plugin_version`",
		"ADD UNIQUE KEY `uq_plugin_versions_plugin_version` (`plugin_id`, `version`)",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("Down missing %q", want)
		}
	}
}

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

// PREPARE and user variables require one connection, not a multi-statement packet.
func TestCardTerminalReceiptMigrationUsesSeparateStatements(t *testing.T) {
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	all, err := source.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.Id != "20261027-01-card-terminal-receipts.sql" {
			continue
		}
		if m.DisableTransactionUp {
			t.Fatal("session variables require the migration transaction connection")
		}
		if len(m.Up) != 4 {
			t.Fatalf("got %d execution units, want four separate SQL statements", len(m.Up))
		}
		return
	}
	t.Fatal("terminal receipt migration missing")
}
