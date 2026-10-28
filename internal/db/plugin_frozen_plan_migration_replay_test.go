package db

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	migrate "github.com/rubenv/sql-migrate"
)

const frozenPlansMigration = "20261026-05-install-frozen-plans.sql"

// MySQL commits DDL before sql-migrate records its receipt. Replaying a complete
// or interrupted upgrade must fill only missing schema and preserve every row's
// frozen payload, completed result, uncertainty, dispatch lease and deadline.
func TestFrozenPlanMigrationReplayPreservesEvidence(t *testing.T) {
	columns := []struct{ name, ddl string }{
		{"frozen_plan_json", "JSON NULL"},
		{"hmac_key_id", "VARCHAR(64) NOT NULL DEFAULT ''"},
		{"intent_digest", "CHAR(64) NOT NULL DEFAULT ''"},
		{"attempt_state", "VARCHAR(16) NOT NULL DEFAULT 'UNKNOWN'"},
		{"had_unknown", "BOOLEAN NOT NULL DEFAULT TRUE"},
		{"lease_token", "VARCHAR(64) NULL"},
		{"lease_until", "DATETIME(3) NULL"},
		{"retry_until", "DATETIME(3) NULL"},
	}
	for _, mode := range []string{"lost_receipt", "partial_columns", "partial_with_constraint"} {
		t.Run(mode, func(t *testing.T) {
			database := isolatedTestDB(t)
			prior := migrationSourceThrough(t, operationalIndexesMigration)
			if _, err := migrate.Exec(database, "mysql", prior, migrate.Up); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"legacy", "success", "unknown", "dispatch"} {
				if _, err := database.Exec(`INSERT INTO plugin_install_attempts(space_id,caller_uid,workspace_id,key_digest,plugin_id,root_version_id,adapter_version,input_fingerprint,request_fingerprint,agent_id,completed,created_at) VALUES ('space-a','user-1',?,REPEAT('a',64),'root','released-v1','adapter-v1',REPEAT('b',64),REPEAT('c',64),?,?, '2026-10-26 01:02:03.456')`, id, map[string]string{"legacy": "legacy-agent", "success": "created-agent"}[id], id == "legacy" || id == "success"); err != nil {
					t.Fatal(err)
				}
			}
			present := map[string]bool{}
			source := migrationSourceThrough(t, frozenPlansMigration)
			if mode == "lost_receipt" {
				if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 1 {
					t.Fatalf("initial upgrade: n=%d err=%v", n, err)
				}
				for _, column := range columns {
					present[column.name] = true
				}
				if _, err := database.Exec(`DELETE FROM gorp_migrations WHERE id=?`, frozenPlansMigration); err != nil {
					t.Fatal(err)
				}
			} else {
				for _, column := range columns {
					// Cover an interrupted prefix and a non-prefix schema with a
					// surviving CHECK while later/earlier columns are still absent.
					keep := column.name == "frozen_plan_json" || column.name == "attempt_state" || column.name == "had_unknown"
					if mode == "partial_columns" {
						keep = keep || column.name == "hmac_key_id" || column.name == "intent_digest"
					} else {
						keep = keep || column.name == "lease_token"
					}
					if keep {
						if _, err := database.Exec("ALTER TABLE plugin_install_attempts ADD COLUMN " + column.name + " " + column.ddl); err != nil {
							t.Fatal(err)
						}
						present[column.name] = true
					}
				}
				if mode == "partial_with_constraint" {
					if _, err := database.Exec(`ALTER TABLE plugin_install_attempts ADD CONSTRAINT chk_install_attempt_state CHECK (attempt_state IN ('PREPARED','DISPATCHING','SUCCEEDED','FAILED','UNKNOWN','EXPIRED'))`); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, id := range []string{"success", "unknown", "dispatch"} {
				states := map[string]string{"success": "SUCCEEDED", "unknown": "UNKNOWN", "dispatch": "DISPATCHING"}
				sets := []string{"frozen_plan_json=JSON_OBJECT('definition',JSON_OBJECT('name','frozen-root'),'evidence','preserve')", "attempt_state='" + states[id] + "'", fmt.Sprintf("had_unknown=%t", id != "success")}
				for _, column := range []struct{ name, value string }{
					{"hmac_key_id", "'original-key'"}, {"intent_digest", "REPEAT('d',64)"}, {"lease_token", "'original-lease'"},
					{"lease_until", "'2026-10-26 01:05:03.456'"}, {"retry_until", "'2026-10-27 01:01:03.456'"},
				} {
					if present[column.name] {
						sets = append(sets, column.name+"="+column.value)
					}
				}
				if _, err := database.Exec("UPDATE plugin_install_attempts SET "+strings.Join(sets, ",")+" WHERE workspace_id=?", id); err != nil {
					t.Fatal(err)
				}
			}
			fields := []string{"space_id", "caller_uid", "workspace_id", "key_digest", "plugin_id", "root_version_id", "adapter_version", "input_fingerprint", "request_fingerprint", "agent_id", "team_id", "completed", "created_at"}
			for _, column := range columns {
				if present[column.name] {
					fields = append(fields, column.name)
				}
			}
			before := frozenMigrationEvidence(t, database, fields)
			if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 1 {
				t.Fatalf("interrupted migration replay: n=%d err=%v", n, err)
			}
			if after := frozenMigrationEvidence(t, database, fields); !reflect.DeepEqual(before, after) {
				t.Fatal("migration replay changed existing installation evidence")
			}
			for _, column := range columns {
				var count int
				if err := database.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='plugin_install_attempts' AND column_name=?`, column.name).Scan(&count); err != nil || count != 1 {
					t.Fatalf("column %s missing after replay: count=%d err=%v", column.name, count, err)
				}
			}
			var checks, receipts, conservativeLegacy int
			if err := database.QueryRow(`SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_schema=DATABASE() AND table_name='plugin_install_attempts' AND constraint_name='chk_install_attempt_state' AND constraint_type='CHECK'`).Scan(&checks); err != nil || checks != 1 {
				t.Fatalf("state constraint missing: count=%d err=%v", checks, err)
			}
			if err := database.QueryRow(`SELECT COUNT(*) FROM gorp_migrations WHERE id=?`, frozenPlansMigration).Scan(&receipts); err != nil || receipts != 1 {
				t.Fatalf("migration receipt missing: count=%d err=%v", receipts, err)
			}
			if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_install_attempts WHERE workspace_id='legacy' AND completed=TRUE AND agent_id='legacy-agent' AND attempt_state='UNKNOWN' AND had_unknown=TRUE AND frozen_plan_json IS NULL AND hmac_key_id='' AND intent_digest='' AND lease_token IS NULL AND lease_until IS NULL AND retry_until IS NULL`).Scan(&conservativeLegacy); err != nil || conservativeLegacy != 1 {
				t.Fatalf("legacy uncertainty/result changed: count=%d err=%v", conservativeLegacy, err)
			}
			if _, err := database.Exec(`UPDATE plugin_install_attempts SET attempt_state='INVALID' WHERE workspace_id='legacy'`); err == nil {
				t.Fatal("replayed state constraint does not enforce allowed states")
			}
			if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 0 {
				t.Fatalf("repeated Up: n=%d err=%v", n, err)
			}
			if n, err := migrate.ExecMax(database, "mysql", source, migrate.Down, 1); err == nil || n != 0 || !strings.Contains(err.Error(), "Frozen installation plan downgrade refused") {
				t.Fatalf("unsafe Down: n=%d err=%v", n, err)
			}
			if after := frozenMigrationEvidence(t, database, fields); !reflect.DeepEqual(before, after) {
				t.Fatal("repeated Up/refused Down changed installation evidence")
			}
		})
	}
}

func frozenMigrationEvidence(t *testing.T, database *sql.DB, fields []string) []string {
	t.Helper()
	pairs := make([]string, 0, len(fields))
	for _, field := range fields {
		pairs = append(pairs, "'"+field+"',"+field)
	}
	rows, err := database.Query("SELECT JSON_OBJECT(" + strings.Join(pairs, ",") + ") FROM plugin_install_attempts ORDER BY workspace_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out = append(out, raw)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
