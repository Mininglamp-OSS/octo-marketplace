package db

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
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

// ucaCollisionSeed 描述某张含文本唯一键的表如何种下一对 UCA 冲突行
// golang vs gol<U+00AD>ang —— unicode_ci 下区分，0900_ai_ci 下冲突
type ucaCollisionSeed struct {
	table  string
	seed   func(db *sql.DB, softHyphen string) // 在 legacy collation 状态下种入一对冲突行
	clean  func(db *sql.DB)                    // 清理种子行，避免污染其他 case
}

// guardedTables 列出所有在 20260730-00 中需要 preflight guard 的含用户输入文本唯一键表。
// 新增含文本唯一键的 ALTER 目标表时必须同步加入此表，TestUnifyCollationPreflightGuardsCompleteness 会校验。
var guardedTables = []ucaCollisionSeed{
	{
		table: "categories",
		seed: func(db *sql.DB, softHyphen string) {
			// categories.name_live 生成列依赖 deleted_at，初始为 NULL 即 live
			if _, err := db.Exec(
				`INSERT INTO categories (id, name, icon_key) VALUES ('uca-cat-1','golang',''), ('uca-cat-2',?,'')`,
				softHyphen,
			); err != nil {
				panic(fmt.Sprintf("seed categories UCA collision: %v", err))
			}
		},
		clean: func(db *sql.DB) {
			_, _ = db.Exec(`DELETE FROM categories WHERE id IN ('uca-cat-1','uca-cat-2')`)
		},
	},
	{
		table: "skills",
		seed: func(db *sql.DB, softHyphen string) {
			const ins = `INSERT INTO skills
				(id, name, description, category_id, tags, owner_id, owner_name, space_id,
				 visibility, readme_content, file_name, file_url)
			VALUES (?, ?, '', '', JSON_ARRAY(), 'uca-owner', '', 'uca-space', 'private', '', '', '')`
			if _, err := db.Exec(ins, "uca-skill-1", "golang"); err != nil {
				panic(fmt.Sprintf("seed skills row 1: %v", err))
			}
			if _, err := db.Exec(ins, "uca-skill-2", softHyphen); err != nil {
				panic(fmt.Sprintf("seed skills UCA collision: %v", err))
			}
		},
		clean: func(db *sql.DB) {
			_, _ = db.Exec(`DELETE FROM skills WHERE id IN ('uca-skill-1','uca-skill-2')`)
		},
	},
	{
		table: "skill_tags",
		seed: func(db *sql.DB, softHyphen string) {
			if _, err := db.Exec(
				`INSERT INTO skill_tags (space_id, name, created_by) VALUES ('uca-guard','golang','u'), ('uca-guard',?,'u')`,
				softHyphen,
			); err != nil {
				panic(fmt.Sprintf("seed skill_tags UCA collision: %v", err))
			}
		},
		clean: func(db *sql.DB) {
			_, _ = db.Exec(`DELETE FROM skill_tags WHERE space_id = 'uca-guard'`)
		},
	},
	{
		table: "skill_versions",
		seed: func(db *sql.DB, softHyphen string) {
			// softHyphen = 'gol<U+00AD>ang'，单独作为版本号时与 'golang' 等价产生唯一键冲突
			if _, err := db.Exec(
				`INSERT INTO skill_versions (id, skill_id, version) VALUES ('uca-sv-1','sv-sk','golang'), ('uca-sv-2','sv-sk',?)`,
				softHyphen,
			); err != nil {
				panic(fmt.Sprintf("seed skill_versions UCA collision: %v", err))
			}
		},
		clean: func(db *sql.DB) {
			_, _ = db.Exec(`DELETE FROM skill_versions WHERE id IN ('uca-sv-1','uca-sv-2')`)
		},
	},
	{
		table: "mcp_servers",
		seed: func(db *sql.DB, softHyphen string) {
			const ins = `INSERT INTO mcp_servers
				(id, name, owner_uid, space_id, transport, config_json, tags_json, tools_json,
				 usage_examples_json, faqs_json, notes_json, icon, slogan, category,
				 visibility, creator_name, created_at, updated_at, deleted_at)
			VALUES (?, ?, 'uca-owner', 'uca-space', 'stdio', '{}', JSON_ARRAY(), JSON_ARRAY(),
				JSON_ARRAY(), JSON_ARRAY(), JSON_ARRAY(), '', '', 'cat',
				'public', '', NOW(), NOW(), NULL)`
			if _, err := db.Exec(ins, "uca-mcp-1", "golang"); err != nil {
				panic(fmt.Sprintf("seed mcp_servers row 1: %v", err))
			}
			if _, err := db.Exec(ins, "uca-mcp-2", softHyphen); err != nil {
				panic(fmt.Sprintf("seed mcp_servers UCA collision: %v", err))
			}
		},
		clean: func(db *sql.DB) {
			_, _ = db.Exec(`DELETE FROM mcp_servers WHERE id IN ('uca-mcp-1','uca-mcp-2')`)
		},
	},
}

// TestUnifyCollationMigrationPreflightCatchesUCACollision 表驱动验证：
// 每张 guarded table 单独种下 UCA 冲突，确保 preflight abort 且无半转换。
// 每个 case 独立隔离 DB，删除任意一张表的 guard 会让对应 case 失败。
func TestUnifyCollationMigrationPreflightCatchesUCACollision(t *testing.T) {
	softHyphen := "gol­ang" // gol + U+00AD + ang
	for _, tc := range guardedTables {
		tc := tc
		t.Run(tc.table, func(t *testing.T) {
			database := isolatedTestDB(t)

			fullSource := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
			_, _ = migrate.Exec(database, "mysql", fullSource, migrate.Down)
			t.Cleanup(func() { _, _ = migrate.Exec(database, "mysql", fullSource, migrate.Down) })

			migrations, err := fullSource.FindMigrations()
			if err != nil {
				t.Fatal(err)
			}
			const unifyID = "20260730-00-unify-collation.sql"
			previous := make([]*migrate.Migration, 0, len(migrations)-1)
			for _, m := range migrations {
				if m.Id != unifyID {
					previous = append(previous, m)
				}
			}
			// 跑到 20260730-00 之前，再强制所有业务表回退到 legacy unicode_ci 模拟存量库
			if _, err := migrate.Exec(database, "mysql", &migrate.MemoryMigrationSource{Migrations: previous}, migrate.Up); err != nil {
				t.Fatalf("apply migrations before unify: %v", err)
			}
			for _, table := range unifiedCollationTables {
				if table == "gorp_migrations" {
					continue
				}
				if _, err := database.Exec(fmt.Sprintf("ALTER TABLE `%s` CONVERT TO CHARACTER SET utf8mb4 COLLATE %s", table, normalizeCollation)); err != nil {
					t.Fatalf("set legacy collation on %s: %v", table, err)
				}
			}

			tc.seed(database, softHyphen)
			t.Cleanup(func() { tc.clean(database) })

			if _, err := migrate.Exec(database, "mysql", fullSource, migrate.Up); err == nil {
				t.Fatalf("unify-collation migration unexpectedly accepted UCA collision in %s", tc.table)
			}

			// preflight 必须在任何持久 ALTER 之前 abort，所有业务表仍在 legacy collation
			for _, table := range unifiedCollationTables {
				var collation string
				if err := database.QueryRow(
					"SELECT TABLE_COLLATION FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
					table,
				).Scan(&collation); err != nil {
					t.Fatalf("query collation for %s: %v", table, err)
				}
				if collation != normalizeCollation && table != "gorp_migrations" {
					t.Errorf("table %s partially converted to %s, want %s", table, collation, normalizeCollation)
				}
			}
		})
	}
}

// TestUnifyCollationPreflightGuardsCompleteness 静态校验：
// 20260730-00-unify-collation.sql 中 CREATE TEMPORARY TABLE collation_guard_<table>
// 的数量必须与 guardedTables 长度一致，防止新增文本唯一键表时忘记加 guard。
func TestUnifyCollationPreflightGuardsCompleteness(t *testing.T) {
	content, err := migrationsql.FS.ReadFile("20260730-00-unify-collation.sql")
	if err != nil {
		t.Fatalf("read unify-collation migration: %v", err)
	}
	sqlText := string(content)

	guardedSet := make(map[string]struct{}, len(guardedTables))
	for _, g := range guardedTables {
		guardedSet[g.table] = struct{}{}
	}

	// 解析所有 CREATE TEMPORARY TABLE collation_guard_<table>
	for _, line := range strings.Split(sqlText, "\n") {
		const prefix = "CREATE TEMPORARY TABLE collation_guard_"
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[idx+len(prefix):])
		// 取空格或括号前的表名部分
		nameEnd := strings.IndexAny(rest, " (")
		if nameEnd < 0 {
			nameEnd = len(rest)
		}
		tableName := rest[:nameEnd]
		if _, ok := guardedSet[tableName]; !ok {
			t.Errorf("collation_guard_%s 存在于 SQL 但未加入 guardedTables", tableName)
		}
		delete(guardedSet, tableName)
	}
	for table := range guardedSet {
		t.Errorf("guardedTables 包含 %q 但 SQL 中缺少 collation_guard_%s 临时表", table, table)
	}
}

// TestFreshInstallMigrationsSucceedWithUnifiedCollation 验证全新安装路径：
// 从空库直接跑全量 migrations，20260719-09 的 JOIN 显式 COLLATE 必须兼容
// categories/skills 已被 20260714-01 建成 utf8mb4_0900_ai_ci 的场景，不触发 ERROR 1267；
// 最终所有 unifiedCollationTables 统一到 utf8mb4_0900_ai_ci。
func TestFreshInstallMigrationsSucceedWithUnifiedCollation(t *testing.T) {
	database := isolatedTestDB(t)

	fullSource := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	_, _ = migrate.Exec(database, "mysql", fullSource, migrate.Down)

	// 从空库直接跑全量迁移
	if _, err := migrate.Exec(database, "mysql", fullSource, migrate.Up); err != nil {
		t.Fatalf("fresh install migrate Up: %v", err)
	}

	// 断言所有统一管理表最终 collation 为 utf8mb4_0900_ai_ci
	for _, table := range unifiedCollationTables {
		var collation string
		if err := database.QueryRow(
			"SELECT TABLE_COLLATION FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&collation); err != nil {
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
