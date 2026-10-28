package db

import (
	"strings"
	"testing"

	migrate "github.com/rubenv/sql-migrate"
)

func TestFrozenPlanMigrationRecoversWithoutReceipt(t *testing.T) {
	const migrationID = "20261026-05-install-frozen-plans.sql"
	additions := []string{
		"ADD COLUMN frozen_plan_json JSON NULL",
		"ADD COLUMN hmac_key_id VARCHAR(64) NOT NULL DEFAULT ''",
		"ADD COLUMN intent_digest CHAR(64) NOT NULL DEFAULT ''",
		"ADD COLUMN attempt_state VARCHAR(16) NOT NULL DEFAULT 'UNKNOWN'",
		"ADD COLUMN had_unknown BOOLEAN NOT NULL DEFAULT TRUE",
		"ADD COLUMN lease_token VARCHAR(64) NULL",
		"ADD COLUMN lease_until DATETIME(3) NULL",
		"ADD COLUMN retry_until DATETIME(3) NULL",
		"ADD CONSTRAINT chk_install_attempt_state CHECK (attempt_state IN ('PREPARED','DISPATCHING','SUCCEEDED','FAILED','UNKNOWN','EXPIRED'))",
	}
	for _, tc := range []struct {
		name     string
		existing []int
	}{
		{"fresh", nil}, {"first_column", []int{0}}, {"first_four", []int{0, 1, 2, 3}},
		{"all_columns", []int{0, 1, 2, 3, 4, 5, 6, 7}}, {"complete_ddl", []int{0, 1, 2, 3, 4, 5, 6, 7, 8}},
		{"non_prefix", []int{3, 5, 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := isolatedTestDB(t)
			if _, err := migrate.Exec(database, "mysql", migrationSourceThrough(t, operationalIndexesMigration), migrate.Up); err != nil {
				t.Fatal(err)
			}
			// A pre-upgrade attempt may already represent an unknown Fleet result.
			if _, err := database.Exec(`INSERT INTO plugin_install_attempts(space_id,caller_uid,workspace_id,key_digest,plugin_id,root_version_id,adapter_version,input_fingerprint,request_fingerprint,created_at) VALUES ('space','owner','workspace',REPEAT('a',64),'root','v1','1',REPEAT('b',64),REPEAT('c',64),NOW(3))`); err != nil {
				t.Fatal(err)
			}
			for _, i := range tc.existing {
				if _, err := database.Exec("ALTER TABLE plugin_install_attempts " + additions[i]); err != nil {
					t.Fatal(err)
				}
			}
			source := migrationSourceThrough(t, migrationID)
			if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 1 {
				t.Fatalf("resume partial DDL: n=%d err=%v", n, err)
			}
			var state string
			var unknown bool
			var frozen, hmacNull bool
			if err := database.QueryRow(`SELECT attempt_state,had_unknown,frozen_plan_json IS NULL,hmac_key_id='' FROM plugin_install_attempts`).Scan(&state, &unknown, &frozen, &hmacNull); err != nil {
				t.Fatal(err)
			}
			if state != "UNKNOWN" || !unknown || !frozen || !hmacNull {
				t.Fatalf("historical evidence defaults changed: %s %t %t %t", state, unknown, frozen, hmacNull)
			}
			// Emulate a row written after DDL success but before recording the migration.
			// Replay must never reset its terminal state, plan, MAC identity or deadline.
			completed, savedState, hadUnknown := true, "SUCCEEDED", false
			if tc.name == "complete_ddl" || tc.name == "non_prefix" {
				completed, savedState, hadUnknown = false, "UNKNOWN", true
			}
			if _, err := database.Exec(`UPDATE plugin_install_attempts SET completed=?,agent_id='original-agent',attempt_state=?,had_unknown=?,frozen_plan_json=JSON_OBJECT('original',TRUE),hmac_key_id='retained-key',intent_digest=REPEAT('d',64),lease_token='original-lease',lease_until='2026-10-28 10:00:00.123',retry_until='2026-10-29 10:00:00.123'`, completed, savedState, hadUnknown); err != nil {
				t.Fatal(err)
			}
			const evidence = `SELECT JSON_OBJECT('space',space_id,'caller',caller_uid,'workspace',workspace_id,'key',key_digest,'root',plugin_id,'version',root_version_id,'adapter',adapter_version,'input',input_fingerprint,'request',request_fingerprint,'completed',completed,'agent',agent_id,'team',team_id,'created',created_at,'state',attempt_state,'unknown',had_unknown,'plan',frozen_plan_json,'hmac',hmac_key_id,'intent',intent_digest,'lease',lease_token,'lease_until',lease_until,'deadline',retry_until) FROM plugin_install_attempts`
			var before, after string
			if err := database.QueryRow(evidence).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`DELETE FROM gorp_migrations WHERE id=?`, migrationID); err != nil {
				t.Fatal(err)
			}
			if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 1 {
				t.Fatalf("completed-DDL/missing-receipt replay: n=%d err=%v", n, err)
			}
			if err := database.QueryRow(evidence).Scan(&after); err != nil || before != after {
				t.Fatalf("replay changed attempt: err=%v", err)
			}
			// The constraint must actually exist and enforce the state enum after recovery.
			if _, err := database.Exec(`UPDATE plugin_install_attempts SET attempt_state='INVALID'`); err == nil {
				t.Fatal("state constraint missing")
			}
			if n, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil || n != 0 {
				t.Fatalf("repeated Up: n=%d err=%v", n, err)
			}
			if n, err := migrate.ExecMax(database, "mysql", source, migrate.Down, 1); err == nil || n != 0 || !strings.Contains(err.Error(), "Frozen installation plan downgrade refused") {
				t.Fatalf("unsafe Down: n=%d err=%v", n, err)
			}
			if err := database.QueryRow(evidence).Scan(&after); err != nil || before != after {
				t.Fatalf("Down changed attempt: err=%v", err)
			}
			var recorded int
			if err := database.QueryRow(`SELECT COUNT(*) FROM gorp_migrations WHERE id=?`, migrationID).Scan(&recorded); err != nil || recorded != 1 {
				t.Fatalf("receipt count=%d err=%v", recorded, err)
			}
		})
	}
}
