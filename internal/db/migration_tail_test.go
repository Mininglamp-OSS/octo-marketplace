package db

import (
	migrate "github.com/rubenv/sql-migrate"
	"testing"
)

func migrationTailCount(t *testing.T, source migrate.MigrationSource, target string) int {
	t.Helper()
	migrations, err := source.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range migrations {
		if m.Id == target {
			return len(migrations) - i
		}
	}
	t.Fatalf("migration %s not found", target)
	return 0
}
