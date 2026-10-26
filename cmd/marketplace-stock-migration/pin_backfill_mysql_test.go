package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/pinbackfill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migrationauth"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

// pinBackfillCLITestDB provisions an isolated migrated database and points the
// CLI's restricted DSN at it. The CLI intentionally rejects multiStatements and
// arbitrary parameters, so the fixture opens its own admin connection and hands
// the command a minimal DSN.
func pinBackfillCLITestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_MYSQL_DSN is unset in CI: the pin-backfill CLI integration would silently skip")
		}
		t.Skip("TEST_MYSQL_DSN not set; skipping pin-backfill CLI integration")
	}
	config, err := mysql.ParseDSN(dsn)
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
	databaseName := fmt.Sprintf("octo_pin_backfill_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE `" + databaseName + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		t.Fatalf("create isolated database: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE `" + databaseName + "`") })
	cliConfig := *config
	cliConfig.DBName = databaseName
	cliConfig.MultiStatements = false
	cliConfig.ParseTime = true
	cliConfig.Params = nil
	database, err := sql.Open("mysql", cliConfig.FormatDSN())
	if err != nil {
		t.Fatalf("open isolated database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Ping(); err != nil {
		t.Fatalf("ping isolated database: %v", err)
	}
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	t.Setenv("MARKETPLACE_STOCK_MIGRATION_DSN", cliConfig.FormatDSN())
	return database
}

func seedPinBackfillCLIFixture(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, plugin := range []struct {
		id, kind, visibility string
	}{
		{"skill", "skill", "system"},
		{"expert", "expert", "private"},
	} {
		if _, err := database.Exec(`INSERT INTO plugins
			(plugin_id,plugin_name,plugin_type,tags_json,owner_uid,space_id,visibility,listing_state,
			 manifest_json,plugin_json,manifest_hash,plugin_hash,current_version,created_at,updated_at)
			VALUES (?,?,?,JSON_ARRAY(),'user-1','space-a',?,'published',JSON_OBJECT(),JSON_OBJECT(),
			 REPEAT('a',71),REPEAT('b',71),'1.0.0',NOW(3),NOW(3))`,
			plugin.id, plugin.id, plugin.kind, plugin.visibility); err != nil {
			t.Fatalf("seed plugin %s: %v", plugin.id, err)
		}
	}
	relations, err := model.EncodeSnapshotRelations([]model.PluginRelation{{
		ID: "expert-skill", SourcePluginID: "expert", SourcePluginType: model.PluginTypeExpert,
		TargetPluginID: "skill", TargetPluginType: model.PluginTypeSkill,
		Type: "expert_skill", Status: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []struct {
		id, plugin string
		relations  []byte
	}{
		{"skill-v1", "skill", []byte(`[]`)},
		{"expert-v1", "expert", relations},
	} {
		if _, err := database.Exec(`INSERT INTO plugin_versions
			(version_id,plugin_id,version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,
			 created_by,created_at,snapshot_schema_version,artifacts_json)
			VALUES (?,?,'1.0.0',JSON_OBJECT(),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),?,'user-1',NOW(3),1,JSON_ARRAY())`,
			version.id, version.plugin, string(version.relations)); err != nil {
			t.Fatalf("seed version %s: %v", version.id, err)
		}
		if _, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at)
			VALUES (?,?,'1.0.0','user-1',NOW(3))`, version.plugin, version.id); err != nil {
			t.Fatalf("seed release %s: %v", version.id, err)
		}
		if _, err := database.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, version.id, version.plugin); err != nil {
			t.Fatalf("seed published pointer %s: %v", version.plugin, err)
		}
	}
	if _, err := database.Exec(`INSERT INTO plugin_version_relation_refs
		(source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id)
		VALUES ('expert','expert-v1','expert-skill','expert_skill','skill',NULL)`); err != nil {
		t.Fatalf("seed reference row: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO plugin_version_reference_index_state
		(source_plugin_id,source_version_id,relation_count,snapshot_digest,verified_at)
		SELECT v.plugin_id,v.version_id,
		 (SELECT COUNT(*) FROM plugin_version_relation_refs r WHERE r.source_version_id=v.version_id),
		 SHA2(CAST(v.relations_json AS CHAR CHARACTER SET utf8mb4),256),NOW(3)
		FROM plugin_versions v`); err != nil {
		t.Fatalf("seed index state: %v", err)
	}
}

func writePinBackfillCLITrust(t *testing.T, scope string) (ed25519.PrivateKey, string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(migrationauth.TrustConfig{
		SchemaVersion: migrationauth.SchemaVersion,
		EnvironmentID: "test-env",
		ActorID:       "pin-operator",
		Keys: []migrationauth.TrustKey{{
			KeyID: "pin-key", IssuerID: "pin-issuer",
			PublicKey: base64.RawURLEncoding.EncodeToString(public), Enabled: true,
		}},
		Issuers: []migrationauth.TrustIssuer{{
			IssuerID: "pin-issuer", AllowedSourceScopes: []string{scope}, Enabled: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "trust.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARKETPLACE_MIGRATION_TRUST_CONFIG", path)
	return private, path
}

func writePinBackfillCLIAuthorization(t *testing.T, plan pinbackfill.Plan, digest string, private ed25519.PrivateKey) string {
	t.Helper()
	now := time.Now().UTC()
	payload := migrationauth.Payload{
		SchemaVersion: migrationauth.SchemaVersion, AuthorizationID: "pin-cli-auth",
		KeyID: "pin-key", IssuerID: "pin-issuer", ActorID: "pin-operator", EnvironmentID: "test-env",
		SourceScope: plan.SourceScope, SourceIdentity: plan.SourceIdentity, PlanDigest: digest,
		BatchID: plan.BatchID, IssuedAt: now.Add(-time.Minute).Format(time.RFC3339),
		ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
	}
	envelope, err := migrationauth.Sign(payload, private)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "authorization.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPinBackfillCLIPreviewApplyAndIdempotentReplayMySQL(t *testing.T) {
	database := pinBackfillCLITestDB(t)
	seedPinBackfillCLIFixture(t, database)
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")

	var out bytes.Buffer
	if err := run([]string{"pin-backfill", "preview", "--output", planPath}, &out); err != nil {
		t.Fatalf("preview: %v\n%s", err, out.String())
	}
	plan, embeddedDigest, err := readPinBackfillPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := pinbackfill.Digest(plan)
	if err != nil || digest != embeddedDigest {
		t.Fatalf("preview digest=%s embedded=%s err=%v", digest, embeddedDigest, err)
	}
	if len(plan.Sources) != 1 || plan.Sources[0].PluginID != "expert" ||
		len(plan.Sources[0].Relations) != 1 || plan.Sources[0].Relations[0].TargetVersionID != "skill-v1" {
		t.Fatalf("preview plan=%+v", plan)
	}

	private, _ := writePinBackfillCLITrust(t, plan.SourceScope)
	authorizationPath := writePinBackfillCLIAuthorization(t, plan, digest, private)
	out.Reset()
	if err = run([]string{"pin-backfill", "apply", "--plan", planPath,
		"--plan-digest", digest, "--authorization", authorizationPath}, &out); err != nil {
		t.Fatalf("apply: %v\n%s", err, out.String())
	}
	var applied pinbackfill.Result
	if err = json.Unmarshal(out.Bytes(), &applied); err != nil {
		t.Fatalf("decode apply output: %v\n%s", err, out.String())
	}
	if applied.Status != "applied" || applied.ActorID != "pin-operator" ||
		len(applied.Sources) != 1 || applied.Sources[0].Status != "applied" {
		t.Fatalf("apply result=%+v", applied)
	}
	var snapshotPin, referencePin sql.NullString
	if err = database.QueryRow(`SELECT JSON_UNQUOTE(JSON_EXTRACT(relations_json,'$[0].target_version_id'))
		FROM plugin_versions WHERE version_id='expert-v1'`).Scan(&snapshotPin); err != nil || !snapshotPin.Valid || snapshotPin.String != "skill-v1" {
		t.Fatalf("snapshot pin=%+v err=%v", snapshotPin, err)
	}
	if err = database.QueryRow(`SELECT target_version_id FROM plugin_version_relation_refs
		WHERE source_version_id='expert-v1' AND relation_id='expert-skill'`).Scan(&referencePin); err != nil || !referencePin.Valid || referencePin.String != "skill-v1" {
		t.Fatalf("reference pin=%+v err=%v", referencePin, err)
	}
	var digestOK, countOK bool
	if err = database.QueryRow(`SELECT idx.snapshot_digest=SHA2(CAST(v.relations_json AS CHAR CHARACTER SET utf8mb4),256),
		idx.relation_count=(SELECT COUNT(*) FROM plugin_version_relation_refs r WHERE r.source_version_id=v.version_id)
		FROM plugin_versions v JOIN plugin_version_reference_index_state idx
		  ON idx.source_plugin_id=v.plugin_id AND idx.source_version_id=v.version_id
		WHERE v.version_id='expert-v1'`).Scan(&digestOK, &countOK); err != nil || !digestOK || !countOK {
		t.Fatalf("index state digest=%v count=%v err=%v", digestOK, countOK, err)
	}

	out.Reset()
	if err = run([]string{"pin-backfill", "apply", "--plan", planPath,
		"--plan-digest", digest, "--authorization", authorizationPath}, &out); err != nil {
		t.Fatalf("idempotent replay: %v\n%s", err, out.String())
	}
	var replay pinbackfill.Result
	if err = json.Unmarshal(out.Bytes(), &replay); err != nil || replay.Status != "noop" {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	var auditRows int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_pin_backfill_runs WHERE plan_digest=?`, digest).Scan(&auditRows); err != nil || auditRows != 1 {
		t.Fatalf("audit rows=%d err=%v", auditRows, err)
	}

	// A bad digest is plan_stale, before the authorization or any database write.
	out.Reset()
	err = run([]string{"pin-backfill", "apply", "--plan", planPath,
		"--plan-digest", strings.Repeat("0", 64), "--authorization", authorizationPath}, &out)
	var reason *reasonError
	if !errors.As(err, &reason) || reason.Code != exitPlanStale {
		t.Fatalf("tampered digest error=%v", err)
	}
	// A corrupted signature is authorization_invalid.
	tamperedPath := filepath.Join(dir, "tampered-authorization.json")
	raw, _ := os.ReadFile(authorizationPath)
	var artifact map[string]json.RawMessage
	_ = json.Unmarshal(raw, &artifact)
	if err = os.WriteFile(tamperedPath, []byte(`{"payload":`+string(artifact["payload"])+`,"signature":"`+strings.Repeat("A", 86)+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err = run([]string{"pin-backfill", "apply", "--plan", planPath,
		"--plan-digest", digest, "--authorization", tamperedPath}, &out)
	if !errors.As(err, &reason) || reason.Code != exitAuthorization {
		t.Fatalf("tampered authorization error=%v", err)
	}
}
