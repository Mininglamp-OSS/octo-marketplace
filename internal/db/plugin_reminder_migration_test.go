package db

import (
	"context"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	migrate "github.com/rubenv/sql-migrate"
	"testing"
)

func TestConvertedExpertConnectorReminderMySQL(t *testing.T) {
	db := reviewDB(t)
	seedMigrationRoot(t, db, "reminder-team", "private", "published")
	applyCurrentMigration(t, db)
	var source, target string
	if err := db.QueryRow("SELECT source_id,target_id FROM plugin_connector_baselines").Scan(&source, &target); err != nil {
		t.Fatal(err)
	}
	r := pluginrepo.New(db).WithVersionManagement(true).WithReferences(true)
	svc := pluginsvc.New(r).WithVersionManagement(true).WithReferences(true)
	check := func(count int) {
		t.Helper()
		p, err := r.DependencyUpdates(context.Background(), tenantScope(), source)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Updates) != count || count == 1 && p.Updates[0].PluginID != target {
			t.Fatalf("updates: %+v", p.Updates)
		}
		items, _, err := svc.List(context.Background(), managedOwner(), pluginsvc.ListParams{Mine: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item.ID == source {
				if (len(item.DependencyUpdates) > 0) != (count == 1) {
					t.Fatal("owner indicator does not match preview")
				}
				return
			}
		}
		t.Fatal("converted Expert missing from owner list")
	}
	check(0)
	if _, err := db.Exec("UPDATE plugins SET plugin_hash='changed-config' WHERE plugin_id=?", target); err != nil {
		t.Fatal(err)
	}
	check(1)
}

func TestReminderMigrationReplayMySQL(t *testing.T) {
	const id = "20261103-03-plugin-connector-baselines.sql"
	db := isolatedTestDB(t)
	if _, err := migrate.Exec(db, "mysql", migrationSourceThrough(t, "20261103-02-plugin-dependency-reminder-skips.sql"), migrate.Up); err != nil {
		t.Fatal(err)
	}
	dependencyFixture(t, db)
	source := migrationSourceThrough(t, id)
	last := source.Migrations[len(source.Migrations)-1]
	// The first CREATE committed but the receipt did not.
	if _, err := db.Exec(last.Up[0]); err != nil {
		t.Fatal(err)
	}
	replay := func() {
		t.Helper()
		if n, err := migrate.Exec(db, "mysql", source, migrate.Up); err != nil || n != 1 {
			t.Fatalf("applied=%d err=%v", n, err)
		}
	}
	replay()
	var original string
	if err := db.QueryRow("SELECT revision FROM plugin_connector_baselines WHERE source_id='expert' AND target_id='connector'").Scan(&original); err != nil {
		t.Fatal(err)
	}
	// The current config has moved on since acceptance; replay cannot acknowledge it.
	if _, err := db.Exec("UPDATE plugins SET plugin_hash='modified' WHERE plugin_id='connector'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM gorp_migrations WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	replay()
	var after string
	if err := db.QueryRow("SELECT revision FROM plugin_connector_baselines WHERE source_id='expert' AND target_id='connector'").Scan(&after); err != nil || after != original {
		t.Fatalf("baseline %q, expected %q: %v", after, original, err)
	}
}
