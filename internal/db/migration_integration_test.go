package db

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
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
//
// Skipping is correct on a laptop with no MySQL, and WRONG in CI: this package is
// where the migration chain, the generated-column unique index, the approve swap
// and every cross-Space scope predicate are actually executed, so a CI run that
// silently skips it reports `ok` for the only job that proves the schema works.
// The DSN is set by .github/workflows/ci.yml; if that ever stops being true the
// suite must fail loudly rather than pass by not running. GitHub Actions (and
// essentially every other runner) sets CI=true, which is the signal used here.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_MYSQL_DSN is unset in CI: the MySQL-backed suite would silently skip and " +
				"report ok without executing a single migration or race. Check the workflow's env block.")
		}
		t.Skip("TEST_MYSQL_DSN not set; skipping integration test (set CI=1 to make this a failure)")
	}
	return dsn
}

func isolatedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	config, err := mysql.ParseDSN(testDSN(t))
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
	}
	adminConfig := *config
	adminConfig.DBName = ""
	admin, err := sql.Open("mysql", adminConfig.FormatDSN())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	databaseName := fmt.Sprintf("octo_marketplace_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE `" + databaseName + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		t.Fatalf("create isolated database: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE `" + databaseName + "`") })

	config.DBName = databaseName
	database, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatalf("open isolated database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Ping(); err != nil {
		t.Fatalf("ping isolated database: %v", err)
	}
	return database
}

// TestRunMigrationsUpDown executes all migrations Up, asserts the three
// marketplace tables exist, then runs Down and asserts they are dropped.
func TestRunMigrationsUpDown(t *testing.T) {
	database := isolatedTestDB(t)

	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}
	// Integration tests share the configured database and run in shuffled
	// order in CI. Always normalize the starting state explicitly.
	if _, err := migrate.Exec(database, "mysql", source, migrate.Down); err != nil {
		t.Fatalf("reset migrations: %v", err)
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
	var databaseCollation string
	if err := database.QueryRow(
		"SELECT DEFAULT_COLLATION_NAME FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME = DATABASE()",
	).Scan(&databaseCollation); err != nil {
		t.Fatalf("query database collation: %v", err)
	}
	if databaseCollation != "utf8mb4_unicode_ci" {
		t.Errorf("database collation=%s want=utf8mb4_unicode_ci", databaseCollation)
	}
}

func TestCollationMigrationPreflightPreventsPartialConversion(t *testing.T) {
	database := isolatedTestDB(t)

	fullSource := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	_, _ = migrate.Exec(database, "mysql", fullSource, migrate.Down)
	t.Cleanup(func() { _, _ = migrate.Exec(database, "mysql", fullSource, migrate.Down) })
	migrations, err := fullSource.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}
	const target = "20260722-00-normalize-marketplace-collations.sql"
	previous := make([]*migrate.Migration, 0, len(migrations)-1)
	for _, migration := range migrations {
		if migration.Id != target {
			previous = append(previous, migration)
		}
	}
	if _, err := migrate.Exec(database, "mysql", &migrate.MemoryMigrationSource{Migrations: previous}, migrate.Up); err != nil {
		t.Fatalf("apply previous migrations: %v", err)
	}
	for _, table := range normalizedCollationTables {
		if _, err := database.Exec(fmt.Sprintf("ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", table)); err != nil {
			t.Fatalf("set legacy collation on %s: %v", table, err)
		}
	}
	if _, err := database.Exec(`INSERT INTO skill_tags (space_id, name, created_by) VALUES ('space-guard', 'prod', 'u'), ('space-guard', 'prod ', 'u')`); err != nil {
		t.Fatalf("seed collision: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM skill_tags WHERE space_id = 'space-guard'`)
	})
	if _, err := migrate.Exec(database, "mysql", fullSource, migrate.Up); err == nil {
		t.Fatal("collation migration unexpectedly accepted trailing-space collision")
	}
	for _, table := range normalizedCollationTables {
		var collation string
		if err := database.QueryRow("SELECT TABLE_COLLATION FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&collation); err != nil {
			t.Fatal(err)
		}
		if collation != "utf8mb4_0900_ai_ci" {
			t.Errorf("table %s partially converted to %s", table, collation)
		}
	}
}

func TestCollationMigrationPreflightsSkillVersionCollisions(t *testing.T) {
	database := isolatedTestDB(t)

	fullSource := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	_, _ = migrate.Exec(database, "mysql", fullSource, migrate.Down)
	t.Cleanup(func() { _, _ = migrate.Exec(database, "mysql", fullSource, migrate.Down) })
	migrations, err := fullSource.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}
	const target = "20260722-00-normalize-marketplace-collations.sql"
	previous := make([]*migrate.Migration, 0, len(migrations)-1)
	for _, migration := range migrations {
		if migration.Id != target {
			previous = append(previous, migration)
		}
	}
	if _, err := migrate.Exec(database, "mysql", &migrate.MemoryMigrationSource{Migrations: previous}, migrate.Up); err != nil {
		t.Fatalf("apply previous migrations: %v", err)
	}
	for _, table := range normalizedCollationTables {
		if _, err := database.Exec(fmt.Sprintf("ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", table)); err != nil {
			t.Fatalf("set legacy collation on %s: %v", table, err)
		}
	}
	if _, err := database.Exec(`INSERT INTO skill_versions (id, skill_id, version) VALUES ('version-1', 'skill-1', '1.0.0'), ('version-2', 'skill-1', '1.0.0 ')`); err != nil {
		t.Fatalf("seed version collision: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM skill_versions WHERE id IN ('version-1', 'version-2')`)
	})
	if _, err := migrate.Exec(database, "mysql", fullSource, migrate.Up); err == nil {
		t.Fatal("collation migration unexpectedly accepted skill version collision")
	}
	for _, table := range normalizedCollationTables {
		var collation string
		if err := database.QueryRow("SELECT TABLE_COLLATION FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&collation); err != nil {
			t.Fatal(err)
		}
		if collation != "utf8mb4_0900_ai_ci" {
			t.Errorf("table %s partially converted to %s", table, collation)
		}
	}
}

func TestCollationMigrationIgnoresSoftDeletedSkillNameCollision(t *testing.T) {
	database := isolatedTestDB(t)

	fullSource := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	_, _ = migrate.Exec(database, "mysql", fullSource, migrate.Down)
	t.Cleanup(func() { _, _ = migrate.Exec(database, "mysql", fullSource, migrate.Down) })
	migrations, err := fullSource.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}
	const target = "20260722-00-normalize-marketplace-collations.sql"
	previous := make([]*migrate.Migration, 0, len(migrations)-1)
	for _, migration := range migrations {
		if migration.Id != target {
			previous = append(previous, migration)
		}
	}
	if _, err := migrate.Exec(database, "mysql", &migrate.MemoryMigrationSource{Migrations: previous}, migrate.Up); err != nil {
		t.Fatalf("apply previous migrations: %v", err)
	}
	for _, table := range normalizedCollationTables {
		if _, err := database.Exec(fmt.Sprintf("ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", table)); err != nil {
			t.Fatalf("set legacy collation on %s: %v", table, err)
		}
	}
	const insert = `INSERT INTO skills
		(id, name, description, category_id, tags, owner_id, owner_name, space_id,
		 visibility, readme_content, file_name, file_url, is_deleted)
		VALUES (?, ?, '', '', JSON_ARRAY(), 'owner', '', 'space', 'private', '', '', '', ?)`
	if _, err := database.Exec(insert, "live", "example", 0); err != nil {
		t.Fatalf("seed live skill: %v", err)
	}
	if _, err := database.Exec(insert, "deleted", "example ", 1); err != nil {
		t.Fatalf("seed deleted skill: %v", err)
	}
	if _, err := migrate.Exec(database, "mysql", fullSource, migrate.Up); err != nil {
		t.Fatalf("collation migration rejected collision with soft-deleted skill: %v", err)
	}
}

func TestMigrationsUpgradeLegacyDatabaseCollation(t *testing.T) {
	database := isolatedTestDB(t)

	fullSource := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	migrations, err := fullSource.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}

	const legacyCutoff = "20260719-09-category-soft-delete-uuid.sql"
	legacy := make([]*migrate.Migration, 0, len(migrations))
	for _, migration := range migrations {
		if migration.Id <= legacyCutoff {
			legacy = append(legacy, migration)
		}
	}
	if _, err := migrate.Exec(database, "mysql", &migrate.MemoryMigrationSource{Migrations: legacy}, migrate.Up); err != nil {
		t.Fatalf("provision legacy database: %v", err)
	}
	for _, table := range []string{"categories", "skills"} {
		if _, err := database.Exec(fmt.Sprintf("ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", table)); err != nil {
			t.Fatalf("set legacy collation on %s: %v", table, err)
		}
	}
	if _, err := migrate.Exec(database, "mysql", fullSource, migrate.Up); err != nil {
		t.Fatalf("upgrade legacy database: %v", err)
	}
}

// TestCollationMigrationUpgradesExistingTables verifies that the forward
// migration repairs a database where every earlier migration is already
// recorded and the tables still use MySQL 8's default collation.
func TestCollationMigrationUpgradesExistingTables(t *testing.T) {
	database := isolatedTestDB(t)

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
	database := isolatedTestDB(t)

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

// TestMigrationsConvergeOnReplayAfterALostRecord reproduces the one failure mode
// the single-DDL-per-file split could not fix on its own, and proves the
// information_schema guards close it.
//
// MySQL implicitly commits every DDL, so sql-migrate's transaction protects
// nothing: it executes the statements, and only then inserts the gorp_migrations
// row. A process death, an OOM kill, or a rolling-deploy restart in that window
// leaves the schema change COMMITTED and the record MISSING, and the next boot
// replays the file. A bare `ADD COLUMN` then dies on ERROR 1060 and a bare
// `ADD INDEX` on ERROR 1061 — a service that cannot start until a human edits
// gorp_migrations by hand, which is the worst shape a migration failure can take.
//
// Deleting the records after a successful Up is exactly that state, and it is why
// this test asserts on a REPLAY rather than on the SQL text: an assertion that the
// files "look idempotent" would have passed against the version that shipped
// ERROR 1061. The replay must apply cleanly AND leave the schema unchanged — a
// guard that skipped the wrong statement, or one written as DROP-then-ADD (which
// would 1091 on the clean first run), fails one of the two halves.
func TestMigrationsConvergeOnReplayAfterALostRecord(t *testing.T) {
	database := isolatedTestDB(t)
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	if _, err := migrate.Exec(database, "mysql", source, migrate.Down); err != nil {
		t.Fatalf("reset migrations: %v", err)
	}
	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("migrate Up: %v", err)
	}

	// The review/listing files this branch adds. Every one of them either creates a
	// column or an index (self-replay hazard) or is DML plus a metadata-only default
	// flip (idempotent by construction) — all four are replayed so the claim covers
	// the whole set rather than the two that were obviously broken.
	replayed := []string{
		"20260901-00-plugin-review-requests.sql",
		"20260901-01-plugin-review-attachment-keys.sql",
		"20260902-00-plugin-listing-state.sql",
		"20260902-01-plugin-listing-state-backfill.sql",
		"20260902-02-plugin-listing-state-reindex.sql",
		"20260902-03-plugin-review-submitted-index.sql",
	}
	before := describeReviewSchema(t, database)
	for _, id := range replayed {
		res, err := database.Exec("DELETE FROM gorp_migrations WHERE id = ?", id)
		if err != nil {
			t.Fatalf("drop migration record %s: %v", id, err)
		}
		if affected, _ := res.RowsAffected(); affected != 1 {
			t.Fatalf("migration record %s not found; the file was renamed and this test is no longer replaying it", id)
		}
	}

	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("replay after a lost migration record failed: %v\n"+
			"a boot in this state cannot recover without hand-editing gorp_migrations", err)
	}
	if after := describeReviewSchema(t, database); after != before {
		t.Fatalf("replay changed the schema.\nbefore: %s\nafter:  %s", before, after)
	}
}

// describeReviewSchema fingerprints the columns and indexes the branch's
// migrations create, so a replay can be asserted to be a true no-op rather than
// merely non-erroring — a guard that silently skipped the real statement would
// pass an error-free replay but change (or fail to produce) this fingerprint.
func describeReviewSchema(t *testing.T, database *sql.DB) string {
	t.Helper()
	var out string
	rows, err := database.Query(`
		SELECT table_name, column_name, column_type, column_default, is_nullable
		  FROM information_schema.COLUMNS
		 WHERE table_schema = DATABASE()
		   AND (table_name = 'plugin_review_requests'
		     OR (table_name = 'plugins' AND column_name = 'listing_state'))
		 ORDER BY table_name, column_name`)
	if err != nil {
		t.Fatalf("describe columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, column, columnType, nullable string
		var columnDefault sql.NullString
		if err := rows.Scan(&table, &column, &columnType, &columnDefault, &nullable); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		out += fmt.Sprintf("col %s.%s %s default=%q null=%s\n", table, column, columnType, columnDefault.String, nullable)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("describe columns: %v", err)
	}

	idx, err := database.Query(`
		SELECT table_name, index_name, seq_in_index, column_name
		  FROM information_schema.STATISTICS
		 WHERE table_schema = DATABASE()
		   AND table_name IN ('plugins', 'plugin_review_requests')
		 ORDER BY table_name, index_name, seq_in_index`)
	if err != nil {
		t.Fatalf("describe indexes: %v", err)
	}
	defer idx.Close()
	for idx.Next() {
		var table, name, column string
		var seq int
		if err := idx.Scan(&table, &name, &seq, &column); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		out += fmt.Sprintf("idx %s.%s[%d]=%s\n", table, name, seq, column)
	}
	if err := idx.Err(); err != nil {
		t.Fatalf("describe indexes: %v", err)
	}
	return out
}

// TestSubmittedIndexMigrationRemovesTheStaleDuplicate covers the OTHER collision
// 20260902-03 handles, which the replay test above cannot reach: an environment
// that ran an intermediate head of this branch already carries an index named
// `idx_review_plugin_submitted` over the same columns. The fresh name keeps the
// Up from failing on it, but leaving it in place costs an index write on every
// review insert forever and makes a full Down leave a branch-introduced index
// behind. The file drops it, conditionally — so this test builds the upgraded
// shape by hand and asserts the replay converges to exactly one index.
func TestSubmittedIndexMigrationRemovesTheStaleDuplicate(t *testing.T) {
	database := isolatedTestDB(t)
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	if _, err := migrate.Exec(database, "mysql", source, migrate.Down); err != nil {
		t.Fatalf("reset migrations: %v", err)
	}
	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("migrate Up: %v", err)
	}
	// Recreate the intermediate head's state: the old index name alongside the new.
	if _, err := database.Exec(
		"ALTER TABLE `plugin_review_requests` ADD INDEX `idx_review_plugin_submitted` (`plugin_id`, `submitted_at`)"); err != nil {
		t.Fatalf("simulate an environment that ran an earlier branch head: %v", err)
	}
	if _, err := database.Exec("DELETE FROM gorp_migrations WHERE id = ?",
		"20260902-03-plugin-review-submitted-index.sql"); err != nil {
		t.Fatalf("drop migration record: %v", err)
	}
	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("re-running 20260902-03 against an upgraded environment failed: %v", err)
	}

	for name, want := range map[string]int{
		"idx_review_plugin_submitted_at": 1,
		"idx_review_plugin_submitted":    0,
	} {
		var count int
		if err := database.QueryRow(`
			SELECT COUNT(DISTINCT index_name) FROM information_schema.STATISTICS
			 WHERE table_schema = DATABASE() AND table_name = 'plugin_review_requests'
			   AND index_name = ?`, name).Scan(&count); err != nil {
			t.Fatalf("count index %s: %v", name, err)
		}
		if count != want {
			t.Errorf("index %s present=%d, want %d", name, count, want)
		}
	}
}
