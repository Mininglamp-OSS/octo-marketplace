package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
	mysql "github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

func applyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MIGRATION_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MIGRATION_TEST_MYSQL_DSN unset")
	}
	cfg, e := mysql.ParseDSN(dsn)
	if e != nil {
		t.Fatal("test configuration invalid")
	}
	if !strings.HasPrefix(cfg.DBName, "octo_marketplace_rehearsal_") {
		t.Fatal("test database namespace rejected")
	}
	cfg.DBName = ""
	admin, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal("test connection failed")
	}
	name := fmt.Sprintf("octo_marketplace_rehearsal_apply_%d", time.Now().UnixNano())
	if _, e = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); e != nil {
		t.Fatal("test database creation failed")
	}
	cfg.DBName = name
	cfg.ParseTime = true
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal("test connection failed")
	}
	t.Cleanup(func() { db.Close(); admin.Exec("DROP DATABASE `" + name + "`"); admin.Close() })
	if _, e = migrate.Exec(db, "mysql", &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}, migrate.Up); e != nil {
		t.Fatal(e)
	}
	return db
}
func TestApplyMySQLResumeAndDetectTampering(t *testing.T) {
	db := applyTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	source := model.Plugin{ID: "source", Name: "source", Type: model.PluginTypeSkill, Tags: json.RawMessage(`[]`), OwnerUID: "owner", Visibility: model.PluginVisibilitySystem, ListingState: model.PluginListingStatePublished, Manifest: json.RawMessage(`{"name":"source"}`), Package: json.RawMessage(`{"body":"original"}`), ManifestHash: "sha256:manifest", PluginHash: "sha256:plugin", Status: 1, CreatedAt: now, UpdatedAt: now}
	_, e := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, source.ID, source.Name, source.Type, string(source.Tags), source.OwnerUID, source.Visibility, source.ListingState, string(source.Manifest), string(source.Package), source.ManifestHash, source.PluginHash, 1, now, now)
	if e != nil {
		t.Fatal(e)
	}

	if _, e = db.Exec(`INSERT INTO plugin_versions (version_id,plugin_id,version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,created_by,created_at) VALUES ('old-release','source','1',?,?,?,?,'[]','owner',?)`, string(source.Manifest), string(source.Package), source.ManifestHash, source.PluginHash, now); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO plugin_version_releases VALUES ('source','old-release','0.1.0','owner',?)`, now); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`UPDATE plugins SET current_version_id='old-release',published_version_id='old-release' WHERE plugin_id='source'`); e != nil {
		t.Fatal(e)
	}
	source.CurrentVersionID = stringPtrApply("old-release")
	if _, e = db.Exec(`INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,visible,sort_order,created_at,updated_at) VALUES ('old-default','default','source',0,7,?,?),('old-custom','research','source',1,42,?,?)`, now, now, now, now); e != nil {
		t.Fatal(e)
	}
	target := source
	target.ID = "target"
	target.IsEmbedded = false
	n := Node{SourceID: source.ID, Source: source, Plugin: target, VersionID: "version", VersionLabel: "1.0.0", Clone: true, Release: true}
	p := Plan{ID: "run", SnapshotID: "snapshot", Nodes: []Node{n}, Mappings: []Mapping{{SourceID: "source", TargetID: "target", VersionID: "version", Disposition: "canonical"}}}
	r, e := Apply(ctx, db, p)
	if e != nil || !r.Complete {
		t.Fatalf("apply: %v %#v", e, r)
	}
	r, e = Apply(ctx, db, p)
	if e != nil || !r.Complete {
		t.Fatalf("resume: %v", e)
	}
	var count int
	if e = db.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id='target'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("version repeated")
	}
	var sequence, declared, released string
	if e = db.QueryRow(`SELECT v.version,v.declared_version,r.version_label FROM plugin_versions v JOIN plugin_version_releases r ON r.plugin_id=v.plugin_id AND r.version_id=v.version_id WHERE v.plugin_id='target' AND v.version_id='version'`).Scan(&sequence, &declared, &released); e != nil || sequence != "1" || declared != "1.0.0" || released != "1.0.0" {
		t.Fatal("snapshot sequence and released business label contract broken")
	}

	// Old exact releases retain their standalone eligibility after directory retirement.
	var oldID string
	if e = db.QueryRow(`SELECT v.version_id FROM plugins p JOIN plugin_versions v ON v.plugin_id=p.plugin_id JOIN plugin_version_releases r ON r.plugin_id=v.plugin_id AND r.version_id=v.version_id WHERE p.plugin_id='source' AND v.version_id='old-release' AND p.is_embedded=0 AND p.status=1 AND p.deleted_at IS NULL AND p.listing_state='published'`).Scan(&oldID); e != nil || oldID != "old-release" {
		t.Fatal("historical exact release no longer eligible")
	}
	var visible bool
	var order int
	if e = db.QueryRow(`SELECT visible,sort_order FROM plugin_placements WHERE plugin_id='target' AND placement_code='default'`).Scan(&visible, &order); e != nil || visible || order != 7 {
		t.Fatal("hidden default changed")
	}
	if e = db.QueryRow(`SELECT visible,sort_order FROM plugin_placements WHERE plugin_id='target' AND placement_code='research'`).Scan(&visible, &order); e != nil || !visible || order != 42 {
		t.Fatal("custom scene changed")
	}
	if _, e = db.Exec(`UPDATE plugins SET publisher='edited' WHERE plugin_id='target'`); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(ctx, db, p); e == nil {
		t.Fatal("metadata tamper accepted")
	}
	if _, e = Apply(ctx, db, p); e == nil {
		t.Fatal("receipt tamper accepted")
	}
}
