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

var unifiedCollationTables = []string{
	"categories",
	"skills",
	"parse_tasks",
	"skill_tags",
	"skill_versions",
	"resource_metrics",
	"resource_metric_flushes",
	"mcp_servers",
	"gorp_migrations",
}

const targetCollation = "utf8mb4_0900_ai_ci"
const normalizeCollation = "utf8mb4_unicode_ci"

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

// TestRunMigrationsUpDown executes all migrations Up, asserts tables have the
// target collation, then runs Down.
func TestRunMigrationsUpDown(t *testing.T) {
	database := isolatedTestDB(t)

	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}
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

	for _, table := range unifiedCollationTables {
		var collation string
		err := database.QueryRow(
			"SELECT TABLE_COLLATION FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&collation)
		if err != nil {
			t.Fatalf("query collation for %s: %v", table, err)
		}
		if collation != targetCollation {
			t.Errorf("table %s collation=%s want=%s", table, collation, targetCollation)
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
	if databaseCollation != targetCollation {
		t.Errorf("database collation=%s want=%s", databaseCollation, targetCollation)
	}
}

// TestCollationMigrationPreflightPreventsPartialConversion verifies that
// 20260722-00 (NO PAD → PAD SPACE) preflight catches tail-space collisions
// before any persistent table is altered.
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

// TestCollationMigrationPreflightsSkillVersionCollisions verifies that
// 20260722-00 preflight catches skill version collisions.
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

// TestCollationMigrationIgnoresSoftDeletedSkillNameCollision verifies that
// 20260722-00 does not treat a collision between a live and a soft-deleted
// skill name as an error (deleted rows are excluded by the preflight query).
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

// TestMigrationsUpgradeLegacyDatabaseCollation 模拟存量部署升级路径：
// 先 provision 到 20260719-09 之前（categories/skills 由旧 migration 建好），
// 将两表 ALTER 为 utf8mb4_unicode_ci 模拟存量库状态，再从 20260719-09 开始跑剩余迁移；
// 验证 20260719-09 的临时表 COLLATE=unicode_ci 不会在 legacy 状态下触发 1267，
// 且最终所有表统一到 utf8mb4_0900_ai_ci。
func TestMigrationsUpgradeLegacyDatabaseCollation(t *testing.T) {
	database := isolatedTestDB(t)

	fullSource := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	migrations, err := fullSource.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}

	// 只跑 20260719-09 之前的迁移（不含 20260719-09 本身），此时 categories/skills 已建好
	const softDeleteUUID = "20260719-09-category-soft-delete-uuid.sql"
	before := make([]*migrate.Migration, 0, len(migrations))
	for _, migration := range migrations {
		if migration.Id < softDeleteUUID {
			before = append(before, migration)
		}
	}
	if _, err := migrate.Exec(database, "mysql", &migrate.MemoryMigrationSource{Migrations: before}, migrate.Up); err != nil {
		t.Fatalf("provision pre-20260719-09 migrations: %v", err)
	}
	// 模拟存量部署：categories/skills 在旧 migration 下使用 utf8mb4_unicode_ci（PAD SPACE）
	for _, table := range []string{"categories", "skills"} {
		if _, err := database.Exec(fmt.Sprintf("ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE %s", table, normalizeCollation)); err != nil {
			t.Fatalf("set legacy collation on %s: %v", table, err)
		}
	}
	// 从 20260719-09 开始继续跑剩余迁移；20260719-09 的临时表必须兼容 legacy collation
	if _, err := migrate.Exec(database, "mysql", fullSource, migrate.Up); err != nil {
		t.Fatalf("upgrade from 20260719-09 onward: %v", err)
	}
	for _, table := range unifiedCollationTables {
		var collation string
		if err := database.QueryRow(
			"SELECT TABLE_COLLATION FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&collation); err != nil {
			t.Fatalf("query collation for %s: %v", table, err)
		}
		if collation != targetCollation {
			t.Errorf("table %s collation=%s want=%s after full upgrade", table, collation, targetCollation)
		}
	}
}

// TestUnifyCollationMigrationUpgradesExistingTables verifies that 20260730-00
// converts a database where all earlier migrations are applied and tables use
// utf8mb4_unicode_ci (PAD SPACE) to utf8mb4_0900_ai_ci (NO PAD), including
// mcp_servers and gorp_migrations.
func TestUnifyCollationMigrationUpgradesExistingTables(t *testing.T) {
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
	const collationMigrationID = "20260730-00-unify-collation.sql"
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

	for _, table := range unifiedCollationTables {
		if table == "gorp_migrations" {
			continue
		}
		query := fmt.Sprintf(
			"ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE %s",
			table, normalizeCollation,
		)
		if _, err := database.Exec(query); err != nil {
			t.Fatalf("set legacy collation on %s: %v", table, err)
		}
	}

	n, err := migrate.Exec(database, "mysql", fullSource, migrate.Up)
	if err != nil {
		t.Fatalf("apply unify-collation migration: %v", err)
	}
	if n != 1 {
		t.Fatalf("applied %d migrations, want 1", n)
	}

	for _, table := range unifiedCollationTables {
		var collation string
		err := database.QueryRow(
			"SELECT TABLE_COLLATION FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&collation)
		if err != nil {
			t.Fatalf("query collation for %s: %v", table, err)
		}
		if collation != targetCollation {
			t.Errorf("table %s collation=%s want=%s", table, collation, targetCollation)
		}
	}

	var databaseCollation string
	if err := database.QueryRow(
		"SELECT DEFAULT_COLLATION_NAME FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME = DATABASE()",
	).Scan(&databaseCollation); err != nil {
		t.Fatalf("query database collation: %v", err)
	}
	if databaseCollation != targetCollation {
		t.Errorf("database collation=%s want=%s", databaseCollation, targetCollation)
	}
}

// TestRunMigrationsFunc verifies that RunMigrations successfully applies
// all migrations via the production code path.
func TestRunMigrationsFunc(t *testing.T) {
	database := isolatedTestDB(t)

	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}
	_, _ = migrate.Exec(database, "mysql", source, migrate.Down)

	n, err := RunMigrations(database)
	if err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	if n < 2 {
		t.Fatalf("RunMigrations applied %d, want >= 2", n)
	}

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

	_, _ = migrate.Exec(database, "mysql", source, migrate.Down)
}
