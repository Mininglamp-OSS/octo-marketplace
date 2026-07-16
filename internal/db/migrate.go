package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	migrate "github.com/rubenv/sql-migrate"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
)

const (
	migrationLockName    = "octo_marketplace_migration"
	migrationLockTimeout = 30
)

func RunMigrations(database *sql.DB) (int, error) {
	if os.Getenv("SKIP_MIGRATION") == "true" {
		log.Printf("[migrate] SKIP_MIGRATION=true, skipping")
		return 0, nil
	}

	ctx := context.Background()
	conn, err := database.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("get migration connection: %w", err)
	}
	defer conn.Close()

	var acquired int
	if err := conn.QueryRowContext(ctx,
		"SELECT GET_LOCK(?, ?)", migrationLockName, migrationLockTimeout,
	).Scan(&acquired); err != nil {
		return 0, fmt.Errorf("acquire migration lock: %w", err)
	}
	if acquired != 1 {
		return 0, fmt.Errorf("migration lock timeout after %ds", migrationLockTimeout)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", migrationLockName) }()

	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsql.FS,
		Root:       ".",
	}
	n, err := migrate.Exec(database, "mysql", source, migrate.Up)
	if err != nil {
		return 0, fmt.Errorf("run migrations: %w", err)
	}
	return n, nil
}
