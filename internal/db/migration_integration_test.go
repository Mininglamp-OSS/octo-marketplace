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
	"mcp_servers",
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
		if collation != "utf8mb4_0900_ai_ci" {
			t.Errorf("table %s collation=%s want=utf8mb4_0900_ai_ci", table, collation)
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
	if databaseCollation != "utf8mb4_0900_ai_ci" {
		t.Errorf("database collation=%s want=utf8mb4_0900_ai_ci", databaseCollation)
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
	// allBut22：除 20260722-00 外全跑（含 20260730-00），模拟 pre-migration 状态
	allBut22 := make([]*migrate.Migration, 0, len(migrations)-1)
	for _, m := range migrations {
		if m.Id != target {
			allBut22 = append(allBut22, m)
		}
	}
	if _, err := migrate.Exec(database, "mysql", &migrate.MemoryMigrationSource{Migrations: allBut22}, migrate.Up); err != nil {
		t.Fatalf("apply previous migrations: %v", err)
	}
	for _, table := range normalizedCollationTables {
		if _, err := database.Exec(fmt.Sprintf("ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", table)); err != nil {
			t.Fatalf("set pre-migration collation on %s: %v", table, err)
		}
	}
	// 20260722-00 的 preflight 用 TRIM(TRAILING ' ') 归一尾部空格；
	// 'prod' 与 'prod ' 在 0900（NO PAD）下为两条独立记录，
	// TRIM 后在 PAD SPACE 临时表中等价，应触发唯一键冲突。
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
	// allBut22：除 20260722-00 外的所有 migrations 全跑，模拟 pre-migration 状态，
	// 再 ALTER 回 0900 以便种下尾部空格差异数据。
	const target = "20260722-00-normalize-marketplace-collations.sql"
	allBut22 := make([]*migrate.Migration, 0, len(migrations)-1)
	for _, m := range migrations {
		if m.Id != target {
			allBut22 = append(allBut22, m)
		}
	}
	if _, err := migrate.Exec(database, "mysql", &migrate.MemoryMigrationSource{Migrations: allBut22}, migrate.Up); err != nil {
		t.Fatalf("apply previous migrations: %v", err)
	}
	for _, table := range normalizedCollationTables {
		if _, err := database.Exec(fmt.Sprintf("ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", table)); err != nil {
			t.Fatalf("set pre-migration collation on %s: %v", table, err)
		}
	}
	// 20260722-00 的 preflight 用 TRIM(TRAILING ' ') 归一尾部空格；
	// '1.0.0' 与 '1.0.0 ' 在 0900（NO PAD）下是两条独立记录，
	// TRIM 后在 PAD SPACE 临时表中应触发唯一键冲突。
	if _, err := database.Exec(`INSERT INTO skill_versions (id, skill_id, version) VALUES ('version-1', 'skill-1', '1.0.0'), ('version-2', 'skill-1', '1.0.0 ')`); err != nil {
		t.Fatalf("seed version collision: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM skill_versions WHERE id IN ('version-1', 'version-2')`)
	})
	if _, err := migrate.Exec(database, "mysql", fullSource, migrate.Up); err == nil {
		t.Fatal("collation migration unexpectedly accepted skill version trailing-space collision")
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
	// allBut22：除 20260722-00 外全跑（包含后续 20260730-00），模拟 pre-migration 状态
	allBut22 := make([]*migrate.Migration, 0, len(migrations)-1)
	for _, m := range migrations {
		if m.Id != target {
			allBut22 = append(allBut22, m)
		}
	}
	if _, err := migrate.Exec(database, "mysql", &migrate.MemoryMigrationSource{Migrations: allBut22}, migrate.Up); err != nil {
		t.Fatalf("apply previous migrations: %v", err)
	}
	for _, table := range normalizedCollationTables {
		if _, err := database.Exec(fmt.Sprintf("ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", table)); err != nil {
			t.Fatalf("set pre-migration collation on %s: %v", table, err)
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
		if _, err := database.Exec(fmt.Sprintf("ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", table)); err != nil {
			t.Fatalf("set legacy collation on %s: %v", table, err)
		}
	}
	if _, err := migrate.Exec(database, "mysql", fullSource, migrate.Up); err != nil {
		t.Fatalf("upgrade legacy database: %v", err)
	}
}

// TestCollationMigrationUpgradesExistingTables 验证 20260730-00 终端迁移
// 将已存在库的表 collation 统一到 utf8mb4_0900_ai_ci。
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
	// 跑 20260730-00 之前的所有 migrations（含 20260722-00），模拟已有库状态
	const cutoff = "20260730-00-unify-collation.sql"
	previous := make([]*migrate.Migration, 0, len(migrations)-1)
	for _, migration := range migrations {
		if migration.Id < cutoff {
			previous = append(previous, migration)
		}
	}

	previousSource := &migrate.MemoryMigrationSource{Migrations: previous}
	if _, err := migrate.Exec(database, "mysql", previousSource, migrate.Up); err != nil {
		t.Fatalf("apply previous migrations: %v", err)
	}

	// 模拟旧库遗留状态：表仍使用 utf8mb4_unicode_ci
	for _, table := range normalizedCollationTables {
		query := fmt.Sprintf(
			"ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
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
		if collation != "utf8mb4_0900_ai_ci" {
			t.Errorf("table %s collation=%s want=utf8mb4_0900_ai_ci", table, collation)
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
