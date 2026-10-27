package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/stockmigration"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
	migrate "github.com/rubenv/sql-migrate"
)

const operationalIndexesMigration = "20261026-01-install-migration-access-indexes.sql"

func migrationSourceThrough(t *testing.T, cutoff string) *migrate.MemoryMigrationSource {
	t.Helper()
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	all, err := source.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var selected []*migrate.Migration
	for _, m := range all {
		if m.Id <= cutoff {
			selected = append(selected, m)
		}
	}
	return &migrate.MemoryMigrationSource{Migrations: selected}
}

func TestStockAuditSchemaRefusesDowngradeAndRetainsHistory(t *testing.T) {
	// Protect both an original PR3 schema and an already-upgraded database.
	for _, cutoff := range []string{"20261024-03-stock-migration-batches.sql", operationalIndexesMigration} {
		t.Run(cutoff, func(t *testing.T) {
			database := isolatedTestDB(t)
			source := migrationSourceThrough(t, cutoff)
			if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO plugin_stock_migration_batches VALUES ('audit','rolled_back',NOW(3))`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO plugin_stock_migration_roots VALUES ('audit','root',REPEAT('a',64),JSON_ARRAY(),JSON_OBJECT('saved','before'),JSON_OBJECT('saved','after'),JSON_ARRAY(),NOW(3))`); err != nil {
				t.Fatal(err)
			}
			var before string
			const audit = `SELECT CONCAT(b.state,m.source_digest,m.mappings_json,m.before_json,m.after_json,m.node_states_json,m.applied_at) FROM plugin_stock_migration_roots m JOIN plugin_stock_migration_batches b USING(batch_id) WHERE batch_id='audit' AND root_plugin_id='root'`
			if err := database.QueryRow(audit).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if n, err := migrate.Exec(database, "mysql", source, migrate.Down); err == nil || n != 0 || !strings.Contains(err.Error(), "Stock migration audit schema downgrade refused") {
				t.Fatalf("Down must refuse before applying any migration: n=%d err=%v", n, err)
			}
			var after string
			if err := database.QueryRow(audit).Scan(&after); err != nil || after != before {
				t.Fatalf("audit changed after rejected Down: err=%v before=%s after=%s", err, before, after)
			}
			var recorded int
			if err := database.QueryRow(`SELECT COUNT(*) FROM gorp_migrations WHERE id=?`, cutoff).Scan(&recorded); err != nil || recorded != 1 {
				t.Fatalf("migration receipt lost: count=%d err=%v", recorded, err)
			}
			if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 0 {
				t.Fatalf("repeated Up after refused Down: n=%d err=%v", n, err)
			}
		})
	}
}

func TestInstallMigrationIndexesUpgradePreservesDataAndScanPlans(t *testing.T) {
	database := isolatedTestDB(t)
	prior := migrationSourceThrough(t, "20261024-04-reference-index-state.sql")
	if _, err := migrate.Exec(database, "mysql", prior, migrate.Up); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seedStockRoot(t, database, "index-upgrade", "private", "published")
	evidence, err := stockmigration.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	plan := stockmigration.Build(evidence)
	if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "index-batch", "index-upgrade", plan.Digest, true); err != nil {
		t.Fatal(err)
	}
	var originalAudit string
	const audit = `SELECT CONCAT(source_digest,mappings_json,before_json,after_json,node_states_json) FROM plugin_stock_migration_roots WHERE batch_id='index-batch'`
	if err = database.QueryRow(audit).Scan(&originalAudit); err != nil {
		t.Fatal(err)
	}
	repo := pluginrepo.New(database)
	attempt := pluginrepo.InstallAttempt{WorkspaceID: "workspace", KeyDigest: strings.Repeat("a", 64), PluginID: "index-upgrade", RootVersionID: "version", AdapterVersion: "1", InputFingerprint: strings.Repeat("b", 64), RequestFingerprint: strings.Repeat("c", 64)}
	if _, err = database.ExecContext(ctx, `INSERT INTO plugin_install_attempts(space_id,caller_uid,workspace_id,key_digest,plugin_id,root_version_id,adapter_version,input_fingerprint,request_fingerprint,created_at) VALUES (?,?,?,?,?,?,?,?,?,NOW(3))`, tenantScope().SpaceID, tenantScope().CallerUID, attempt.WorkspaceID, attempt.KeyDigest, attempt.PluginID, attempt.RootVersionID, attempt.AdapterVersion, attempt.InputFingerprint, attempt.RequestFingerprint); err != nil {
		t.Fatal(err)
	}
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 2 {
		t.Fatalf("index upgrade: n=%d err=%v", n, err)
	}
	for _, tc := range []struct{ table, index, columns string }{
		{"plugin_install_attempts", "idx_plugin_install_resource_time", "space_id,plugin_id,created_at"},
		{"plugin_stock_migration_batches", "idx_stock_migration_batch_state", "state,batch_id"},
		{"plugin_stock_migration_roots", "idx_stock_migration_root_applied", "batch_id,applied_at,root_plugin_id"},
		{"plugins", "PRIMARY", "plugin_id"},
		{"plugin_versions", "PRIMARY", "version_id"},
		{"plugin_version_releases", "uq_plugin_release_version", "version_id"},
		{"plugin_version_relation_refs", "PRIMARY", "source_version_id,relation_id"},
	} {
		if got := strings.Join(indexColumns(t, database, tc.table, tc.index), ","); got != tc.columns {
			t.Errorf("%s.%s columns=%s, want %s", tc.table, tc.index, got, tc.columns)
		}
	}
	var retainedAudit string
	if err = database.QueryRow(audit).Scan(&retainedAudit); err != nil || retainedAudit != originalAudit {
		t.Fatalf("upgrade changed audit data: %v", err)
	}
	if got, err := repo.GetInstallAttempt(ctx, tenantScope(), attempt.WorkspaceID, attempt.KeyDigest); err != nil || got == nil || got.RequestFingerprint != attempt.RequestFingerprint {
		t.Fatalf("upgrade lost installation receipt: %+v err=%v", got, err)
	}
	// These are the actual bounded evidence queries, not FORCE INDEX probes.
	for _, query := range []string{
		`SELECT plugin_id,plugin_type,is_embedded FROM plugins ORDER BY plugin_id LIMIT 101`,
		`SELECT plugin_id,version_id,relations_json FROM plugin_versions ORDER BY version_id LIMIT 101`,
		`SELECT plugin_id,version_id FROM plugin_version_releases ORDER BY version_id LIMIT 101`,
	} {
		assertIndexedEvidenceOrder(t, database, query)
	}
	// Simulate an interrupted multi-DDL upgrade: two indexes survived, one did
	// not, and sql-migrate never recorded the migration. Replay must converge.
	if _, err := database.Exec(`ALTER TABLE plugin_stock_migration_batches DROP INDEX idx_stock_migration_batch_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM gorp_migrations WHERE id=?`, operationalIndexesMigration); err != nil {
		t.Fatal(err)
	}
	if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 1 {
		t.Fatalf("interrupted index upgrade replay: n=%d err=%v", n, err)
	}
	if got := strings.Join(indexColumns(t, database, "plugin_stock_migration_batches", "idx_stock_migration_batch_state"), ","); got != "state,batch_id" {
		t.Fatalf("replay did not restore batch index: %s", got)
	}
	if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 0 {
		t.Fatalf("repeated index upgrade: n=%d err=%v", n, err)
	}
}

func assertIndexedEvidenceOrder(t *testing.T, database *sql.DB, query string) {
	t.Helper()
	var raw []byte
	if err := database.QueryRow("EXPLAIN FORMAT=JSON " + query).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plan any
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	var indexed bool
	var inspect func(any)
	inspect = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if v["using_filesort"] == true {
				t.Errorf("evidence scan needs filesort: %s plan=%s", query, raw)
			}
			if key, ok := v["key"].(string); ok && key != "" {
				indexed = true
			}
			for _, child := range v {
				inspect(child)
			}
		case []any:
			for _, child := range v {
				inspect(child)
			}
		}
	}
	inspect(plan)
	if !indexed {
		t.Errorf("evidence scan uses no index: %s plan=%s", query, raw)
	}
	t.Logf("%s: %s", query, raw)
}
