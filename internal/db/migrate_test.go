package db

import "testing"

func TestRunMigrationsSkip(t *testing.T) {
	t.Setenv("SKIP_MIGRATION", "true")
	n, err := RunMigrations(nil)
	if err != nil {
		t.Fatalf("RunMigrations() error=%v", err)
	}
	if n != 0 {
		t.Fatalf("RunMigrations()=%d want=0", n)
	}
}
