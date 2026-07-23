package db

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
)

var normalizedCollationTables = []string{
	"categories",
	"skills",
	"parse_tasks",
	"skill_tags",
	"skill_versions",
	"resource_metrics",
	"resource_metric_flushes",
}

// testDSN returns the MySQL DSN for integration tests.
// Skips the test if TEST_MYSQL_DSN is not set.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set; skipping integration test")
	}
	return dsn
}

// TestRunMigrationsUpDown executes all migrations Up, asserts the three
// marketplace tables exist, then runs Down and asserts they are dropped.
func TestRunMigrationsUpDown(t *testing.T) {
	dsn := testDSN(t)

	database, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer database.Close()

	if err := database.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}

	// --- Up ---
	n, err := migrate.Exec(database, "mysql", source, migrate.Up)
	if err != nil {
		t.Fatalf("migrate Up: %v", err)
	}
	if n < 2 {
		t.Fatalf("migrate Up applied %d migrations, want >= 2", n)
	}

	// Assert tables exist by querying INFORMATION_SCHEMA.
	expectedTables := []string{"categories", "skills", "parse_tasks"}
	for _, table := range expectedTables {
		var count int
		err := database.QueryRow(
			"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&count)
		if err != nil {
			t.Fatalf("query INFORMATION_SCHEMA for %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s not found after migrate Up", table)
		}
	}

	for _, table := range normalizedCollationTables {
		var collation string
		err := database.QueryRow(
			"SELECT TABLE_COLLATION FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&collation)
		if err != nil {
			t.Fatalf("query collation for %s: %v", table, err)
		}
		if collation != "utf8mb4_unicode_ci" {
			t.Errorf("table %s collation=%s want=utf8mb4_unicode_ci", table, collation)
		}
	}

	// --- Down ---
	n, err = migrate.Exec(database, "mysql", source, migrate.Down)
	if err != nil {
		t.Fatalf("migrate Down: %v", err)
	}
	if n < 2 {
		t.Fatalf("migrate Down applied %d migrations, want >= 2", n)
	}

	// Assert tables are gone.
	for _, table := range expectedTables {
		var count int
		err := database.QueryRow(
			"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&count)
		if err != nil {
			t.Fatalf("query INFORMATION_SCHEMA for %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("table %s still exists after migrate Down", table)
		}
	}
}

// TestCollationMigrationUpgradesExistingTables verifies that the forward
// migration repairs a database where every earlier migration is already
// recorded and the tables still use MySQL 8's default collation.
func TestCollationMigrationUpgradesExistingTables(t *testing.T) {
	dsn := testDSN(t)

	database, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer database.Close()

	if err := database.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	fullSource := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}
	_, _ = migrate.Exec(database, "mysql", fullSource, migrate.Down)
	t.Cleanup(func() {
		_, _ = migrate.Exec(database, "mysql", fullSource, migrate.Down)
	})

	migrations, err := fullSource.FindMigrations()
	if err != nil {
		t.Fatalf("FindMigrations: %v", err)
	}
	const collationMigrationID = "20260722-00-normalize-marketplace-collations.sql"
	previous := make([]*migrate.Migration, 0, len(migrations)-1)
	for _, migration := range migrations {
		if migration.Id != collationMigrationID {
			previous = append(previous, migration)
		}
	}
	if len(previous) != len(migrations)-1 {
		t.Fatalf("expected exactly one %s migration", collationMigrationID)
	}

	previousSource := &migrate.MemoryMigrationSource{Migrations: previous}
	if _, err := migrate.Exec(database, "mysql", previousSource, migrate.Up); err != nil {
		t.Fatalf("apply previous migrations: %v", err)
	}

	for _, table := range normalizedCollationTables {
		query := fmt.Sprintf(
			"ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci",
			table,
		)
		if _, err := database.Exec(query); err != nil {
			t.Fatalf("set legacy collation on %s: %v", table, err)
		}
	}

	n, err := migrate.Exec(database, "mysql", fullSource, migrate.Up)
	if err != nil {
		t.Fatalf("apply collation migration: %v", err)
	}
	if n != 1 {
		t.Fatalf("applied %d migrations, want 1", n)
	}

	for _, table := range normalizedCollationTables {
		var collation string
		err := database.QueryRow(
			"SELECT TABLE_COLLATION FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&collation)
		if err != nil {
			t.Fatalf("query collation for %s: %v", table, err)
		}
		if collation != "utf8mb4_unicode_ci" {
			t.Errorf("table %s collation=%s want=utf8mb4_unicode_ci", table, collation)
		}
	}
}

// TestRunMigrationsFunc verifies that RunMigrations successfully applies
// all migrations via the production code path.
func TestRunMigrationsFunc(t *testing.T) {
	dsn := testDSN(t)

	database, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer database.Close()

	if err := database.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	// Clean state: run all Down first.
	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}
	_, _ = migrate.Exec(database, "mysql", source, migrate.Down)

	// Run via production function.
	n, err := RunMigrations(database)
	if err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	if n < 2 {
		t.Fatalf("RunMigrations applied %d, want >= 2", n)
	}

	// Verify tables exist.
	for _, table := range []string{"categories", "skills", "parse_tasks"} {
		var count int
		err := database.QueryRow(
			"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&count)
		if err != nil {
			t.Fatalf("query INFORMATION_SCHEMA for %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s not found after RunMigrations", table)
		}
	}

	// Cleanup: run Down so test is idempotent.
	_, _ = migrate.Exec(database, "mysql", source, migrate.Down)
}
