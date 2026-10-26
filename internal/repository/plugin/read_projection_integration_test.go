//go:build integration

// DB-backed coverage for the A21 read projection. Gate with the integration tag
// AND MARKETPLACE_TEST_MYSQL_DSN, matching the existing MySQL test convention:
//
//	MARKETPLACE_TEST_MYSQL_DSN='root:@tcp(127.0.0.1:3306)/octo_marketplace?parseTime=true&multiStatements=true' \
//	  go test -tags integration ./internal/repository/plugin -run TestReadProjection -v
package plugin

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	marketdb "github.com/Mininglamp-OSS/octo-marketplace/internal/db"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func openProjectionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MARKETPLACE_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MARKETPLACE_TEST_MYSQL_DSN not set; skipping DB-backed read projection test")
	}
	database, err := marketdb.Open(dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := marketdb.RunMigrations(database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return database
}

func insertProjectionPlugin(t *testing.T, database *sql.DB, id, name string, typ model.PluginType, visibility model.PluginVisibility, packageJSON, currentVersion, currentID string) {
	t.Helper()
	var current any
	if currentID != "" {
		current = currentID
	}
	var version any
	if currentVersion != "" {
		version = currentVersion
	}
	_, err := database.ExecContext(context.Background(), `INSERT INTO plugins
 (plugin_id,plugin_name,plugin_type,is_embedded,tags_json,publisher,owner_uid,space_id,visibility,creator_name,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version_id,current_version,status,created_at,updated_at)
 VALUES (?,?,?,0,'[]','','owner-a',NULL,?,'', '{}',?,NULL,'sha256:manifest','sha256:plugin',?,?,1,?,?)`,
		id, name, typ, visibility, packageJSON, current, version, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		t.Fatalf("insert plugin %s: %v", id, err)
	}
}

func insertProjectionVersion(t *testing.T, database *sql.DB, pluginID, versionID, revision, label string) {
	t.Helper()
	_, err := database.ExecContext(context.Background(), `INSERT INTO plugin_versions
 (version_id,plugin_id,version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,created_by,created_at,declared_version,snapshot_schema_version,artifacts_json)
 VALUES (?,?,?,'{}','{}','sha256:manifest','sha256:plugin','[]','owner-a',?,?,1,'[]')`,
		versionID, pluginID, revision, time.Now().UTC(), label)
	if err != nil {
		t.Fatalf("insert version %s: %v", versionID, err)
	}
}

func insertProjectionRelease(t *testing.T, database *sql.DB, pluginID, versionID, label string) {
	t.Helper()
	_, err := database.ExecContext(context.Background(), `INSERT INTO plugin_version_releases
 (plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,?,'owner-a',?)`,
		pluginID, versionID, label, time.Now().UTC())
	if err != nil {
		t.Fatalf("insert release %s: %v", versionID, err)
	}
}

func TestReadProjectionMetadataMySQL(t *testing.T) {
	database := openProjectionTestDB(t)
	ctx := context.Background()

	const (
		skillID      = "a21-projection-skill"
		skillCurrent = "a21-v2"
		skillPub     = "a21-v1"
		connectorID  = "a21-projection-connector"
	)
	cleanup := func() {
		for _, id := range []string{skillID, connectorID} {
			_, _ = database.ExecContext(ctx, `DELETE FROM plugin_connector_effective WHERE plugin_id=?`, id)
			_, _ = database.ExecContext(ctx, `UPDATE plugins SET published_version_id=NULL WHERE plugin_id=?`, id)
			_, _ = database.ExecContext(ctx, `DELETE FROM plugin_version_releases WHERE plugin_id=?`, id)
			_, _ = database.ExecContext(ctx, `DELETE FROM plugin_versions WHERE plugin_id=?`, id)
			_, _ = database.ExecContext(ctx, `DELETE FROM plugins WHERE plugin_id=?`, id)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	insertProjectionPlugin(t, database, skillID, "A21 Skill", model.PluginTypeSkill, model.PluginVisibilitySystem, `{}`, "2.0.0", skillCurrent)
	insertProjectionVersion(t, database, skillID, skillPub, "1", "1.0.0")
	insertProjectionVersion(t, database, skillID, skillCurrent, "2", "2.0.0")
	insertProjectionRelease(t, database, skillID, skillPub, "1.0.0")
	if _, err := database.ExecContext(ctx, `UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, skillPub, skillID); err != nil {
		t.Fatal(err)
	}

	connectorPackage := `{"attachments":[{"path":"mcp.json","content_type":"raw","mime_type":"application/json","raw_content":"{\"mcpServers\":{}}"}]}`
	effectivePackage := `{"attachments":[{"path":"mcp.json","content_type":"raw","mime_type":"application/json","raw_content":"{\"mcpServers\":{\"changed\":{}}}"}]}`
	insertProjectionPlugin(t, database, connectorID, "A21 Connector", model.PluginTypeConnector, model.PluginVisibilitySystem, connectorPackage, "", "")
	if _, err := database.ExecContext(ctx, `INSERT INTO plugin_connector_effective
 (plugin_id,effective_revision,effective_digest,plugin_name,tags_json,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version,visibility,updated_by,updated_at)
 VALUES (?,5,?, 'A21 Connector','[]',0,'{}',?,NULL,'sha256:manifest','sha256:plugin',NULL,'system','owner-a',?)`,
		connectorID, "sha256:effective", effectivePackage, time.Now().UTC()); err != nil {
		t.Fatalf("insert effective: %v", err)
	}

	repo := New(database)
	metas, err := repo.ListPluginReadMetas(ctx, Scope{Admin: true}, []string{skillID, connectorID})
	if err != nil {
		t.Fatalf("ListPluginReadMetas: %v", err)
	}
	skill := metas[skillID]
	if skill.CurrentVersionID == nil || *skill.CurrentVersionID != skillCurrent {
		t.Fatalf("skill current=%v want %s", skill.CurrentVersionID, skillCurrent)
	}
	if skill.CurrentVersionLabel == nil || *skill.CurrentVersionLabel != "2.0.0" {
		t.Fatalf("skill current label=%v", skill.CurrentVersionLabel)
	}
	if skill.CurrentRevisionNo == nil || *skill.CurrentRevisionNo != 2 {
		t.Fatalf("skill revision=%v want 2", skill.CurrentRevisionNo)
	}
	if skill.PublishedVersionID == nil || *skill.PublishedVersionID != skillPub {
		t.Fatalf("skill published=%v want %s", skill.PublishedVersionID, skillPub)
	}
	if skill.PublishedVersionLabel == nil || *skill.PublishedVersionLabel != "1.0.0" {
		t.Fatalf("skill published label=%v", skill.PublishedVersionLabel)
	}
	if skill.PublishedRevisionNo == nil || *skill.PublishedRevisionNo != 1 {
		t.Fatalf("skill published revision=%v want 1", skill.PublishedRevisionNo)
	}
	connector := metas[connectorID]
	if connector.EffectiveRevision == nil || *connector.EffectiveRevision != 5 {
		t.Fatalf("connector effective revision=%v want 5", connector.EffectiveRevision)
	}
	if connector.EffectiveDigest == nil || *connector.EffectiveDigest != "sha256:effective" {
		t.Fatalf("connector effective digest=%v", connector.EffectiveDigest)
	}

	digests, err := repo.ListConnectorContentDigests(ctx, Scope{Admin: true}, []string{connectorID})
	if err != nil {
		t.Fatalf("ListConnectorContentDigests: %v", err)
	}
	got := digests[connectorID]
	if got.EditingDigest == "" {
		t.Fatalf("connector editing digest missing: %#v", got)
	}
	if got.EffectiveDigest == nil {
		t.Fatalf("connector effective digest missing: %#v", got)
	}
	if *got.EffectiveDigest == got.EditingDigest {
		t.Fatalf("different complete payloads must not digest equal: %#v", got)
	}
	if _, err := database.ExecContext(ctx, `UPDATE plugin_connector_effective SET plugin_json=? WHERE plugin_id=?`, connectorPackage, connectorID); err != nil {
		t.Fatal(err)
	}
	digests, err = repo.ListConnectorContentDigests(ctx, Scope{Admin: true}, []string{connectorID})
	if err != nil {
		t.Fatalf("ListConnectorContentDigests after update: %v", err)
	}
	got = digests[connectorID]
	if got.EffectiveDigest == nil || *got.EffectiveDigest != got.EditingDigest {
		t.Fatalf("identical complete payloads must digest equal: %#v", got)
	}
}
