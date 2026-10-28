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
	var binding DatabaseIdentity
	if e := db.QueryRowContext(ctx, "SELECT DATABASE(),@@server_uuid").Scan(&binding.Name, &binding.ServerUUID); e != nil {
		t.Fatal(e)
	}
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
	n := Node{SourceID: source.ID, Source: source, SourcePublishedVersionID: stringPtrApply("old-release"), SourceRelease: &model.PluginVersionRelease{PluginID: "source", VersionID: "old-release", VersionLabel: "0.1.0", ReleasedBy: "owner", ReleasedAt: now}, Plugin: target, VersionID: "version", VersionLabel: "1.0.0", Clone: true, Release: true}
	p := Plan{ID: "run", SnapshotID: "snapshot", Nodes: []Node{n}, Mappings: []Mapping{{SourceID: "source", TargetID: "target", VersionID: "version", Disposition: "canonical"}}}
	r, e := Apply(ctx, db, p, binding)
	if e != nil || !r.Complete {
		t.Fatalf("apply: %v %#v", e, r)
	}
	r, e = Apply(ctx, db, p, binding)
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
	if _, e = Verify(ctx, db, p, binding); e == nil {
		t.Fatal("metadata tamper accepted")
	}
	if _, e = Apply(ctx, db, p, binding); e == nil {
		t.Fatal("receipt tamper accepted")
	}
}

// Keep fixtures close to the real planner: source publication evidence must be
// captured by Build, never inferred from the target's listing state at apply.
func seedPublicationInput(t *testing.T, db *sql.DB, kind model.PluginType, embedded, published bool) Input {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	r := resource("source", "source", "", "body")
	r.Plugin.CreatedAt, r.Plugin.UpdatedAt = now, now
	r.Plugin.ListingState = model.PluginListingStateDelisted
	r.Plugin.IsEmbedded = embedded
	if kind == model.PluginTypeExpert {
		r.Plugin.Type = kind
		r.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"source","plugin_type":"expert","name":"source","description":"test","labels":[],"examples":[]}`)
		r.Files = []File{{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("body")}}
		r.Plugin.Package = testPackage(r.Files)
	}
	p := r.Plugin
	if _, err := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,is_embedded,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at) VALUES (?,?,?,'[]',?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.Type, p.OwnerUID, p.Visibility, p.ListingState, p.IsEmbedded, string(p.Manifest), string(p.Package), p.ManifestHash, p.PluginHash, p.Status, now, now); err != nil {
		t.Fatal(err)
	}
	in := Input{SnapshotID: "publication-snapshot", Resources: []Resource{r}}
	if _, err := db.Exec(`INSERT INTO plugin_versions (version_id,plugin_id,version,declared_version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,created_by,created_at) VALUES ('old','source','1','1.0.0',?,?,?,?,'[]','owner',?)`, string(p.Manifest), string(p.Package), p.ManifestHash, p.PluginHash, now); err != nil {
		t.Fatal(err)
	}
	in.Versions = []model.PluginVersion{{ID: "old", PluginID: "source", Version: "1", DeclaredVersion: stringPtrApply("1.0.0"), Manifest: p.Manifest, Package: p.Package, Relations: json.RawMessage(`[]`)}}
	if published {
		if _, err := db.Exec(`INSERT INTO plugin_version_releases VALUES ('source','old','1.0.0','owner',?)`, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE plugins SET published_version_id='old' WHERE plugin_id='source'`); err != nil {
			t.Fatal(err)
		}
		in.Resources[0].PublishedVersionID = stringPtrApply("old")
		in.Releases = []model.PluginVersionRelease{{PluginID: "source", VersionID: "old", VersionLabel: "1.0.0", ReleasedBy: "owner", ReleasedAt: now}}
	}
	return in
}

func TestApplyMySQLPreservesDelistedPublication(t *testing.T) {
	for _, tc := range []struct {
		name                string
		kind                model.PluginType
		embedded, published bool
	}{
		{"skill clone", model.PluginTypeSkill, false, true},
		{"retained expert", model.PluginTypeExpert, false, true},
		{"embedded expert clone", model.PluginTypeExpert, true, true},
		{"unreleased stays unreleased", model.PluginTypeSkill, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			in := seedPublicationInput(t, db, tc.kind, tc.embedded, tc.published)
			p, err := Build(in, Policy{})
			if err != nil {
				t.Fatalf("build: %v %#v", err, p.Issues)
			}
			var binding DatabaseIdentity
			if err = db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			if report, err := Apply(ctx, db, p, binding); err != nil || !report.Complete {
				t.Fatalf("apply: %v %#v", err, report)
			}
			n := p.Nodes[0]
			var listing string
			var pub sql.NullString
			if err = db.QueryRow(`SELECT listing_state,published_version_id FROM plugins WHERE plugin_id=?`, n.Plugin.ID).Scan(&listing, &pub); err != nil {
				t.Fatal(err)
			}
			if listing != "delisted" || pub.Valid != tc.published || (pub.Valid && pub.String != n.VersionID) {
				t.Fatalf("publication changed: listing=%s pointer=%#v", listing, pub)
			}
			var history int
			if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id='source' AND version_id='old'`).Scan(&history); err != nil || history != 1 {
				t.Fatal("historical snapshot changed")
			}
			if tc.published {
				if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id='source' AND version_id='old' AND version_label='1.0.0' AND released_by='owner'`).Scan(&history); err != nil || history != 1 {
					t.Fatal("historical release changed")
				}
			}
		})
	}
}

func TestApplyMySQLRejectsStalePublicationBeforeResourceWrites(t *testing.T) {
	for _, change := range []string{"nil to published", "pointer removed", "release label", "release actor", "release time", "release removed"} {
		t.Run(change, func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			in := seedPublicationInput(t, db, model.PluginTypeSkill, false, change != "nil to published")
			p, err := Build(in, Policy{})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "nil to published":
				if _, err = db.Exec(`INSERT INTO plugin_version_releases VALUES ('source','old','1.0.0','owner',?)`, in.Resources[0].Plugin.UpdatedAt); err == nil {
					_, err = db.Exec(`UPDATE plugins SET published_version_id='old' WHERE plugin_id='source'`)
				}
			case "pointer removed":
				_, err = db.Exec(`UPDATE plugins SET published_version_id=NULL WHERE plugin_id='source'`)
			case "release label":
				_, err = db.Exec(`UPDATE plugin_version_releases SET version_label='2.0.0' WHERE plugin_id='source'`)
			case "release actor":
				_, err = db.Exec(`UPDATE plugin_version_releases SET released_by='other' WHERE plugin_id='source'`)
			case "release time":
				_, err = db.Exec(`UPDATE plugin_version_releases SET released_at=DATE_ADD(released_at,INTERVAL 1 SECOND) WHERE plugin_id='source'`)
			case "release removed":
				_, err = db.Exec(`DELETE FROM plugin_version_releases WHERE plugin_id='source'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			var binding DatabaseIdentity
			if err = db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			if _, err = Apply(ctx, db, p, binding); err == nil {
				t.Fatal("stale publication accepted")
			}
			var count int
			if err = db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE plugin_id=?`, p.Nodes[0].Plugin.ID).Scan(&count); err != nil || count != 0 {
				t.Fatal("target written despite stale publication")
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_audit_logs WHERE action='migration'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("audit written despite stale publication")
			}
		})
	}
}

func TestApplyMySQLNullRelationMetadata(t *testing.T) {
	db := applyTestDB(t)
	ctx := context.Background()
	in := seedPublicationInput(t, db, model.PluginTypeExpert, false, false)
	// The planner's JSON copy turns SQL-null relation metadata into JSON null.
	connector := resource("connector", "connector", "", "unused")
	connector.Plugin.Type = model.PluginTypeConnector
	connector.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"connector","plugin_type":"connector","name":"connector","description":"test","labels":[],"examples":[]}`)
	connector.Files = []File{{Path: "mcp.json", MIME: "application/json", Data: []byte(`{"mcpServers":{"a":{"command":"echo"}}}`)}}
	connector.Plugin.Package = testPackage(connector.Files)
	connector.Plugin.CreatedAt, connector.Plugin.UpdatedAt = in.Resources[0].Plugin.CreatedAt, in.Resources[0].Plugin.UpdatedAt
	cp := connector.Plugin
	if _, err := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at) VALUES (?,?,?,'[]',?,?,?,?,?,?,?,?,?,?)`, cp.ID, cp.Name, cp.Type, cp.OwnerUID, cp.Visibility, cp.ListingState, string(cp.Manifest), string(cp.Package), cp.ManifestHash, cp.PluginHash, cp.Status, cp.CreatedAt, cp.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	in.Resources = append(in.Resources, connector)
	in.Resources[0].Relations = []model.PluginRelation{{ID: "edge", SourcePluginID: "source", SourcePluginType: model.PluginTypeExpert, TargetPluginID: "connector", TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1, CreatedAt: cp.CreatedAt, UpdatedAt: cp.UpdatedAt}}
	p, err := Build(in, Policy{})
	if err != nil {
		t.Fatalf("build: %v %#v", err, p.Issues)
	}
	var binding DatabaseIdentity
	if err = db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
		t.Fatal(err)
	}
	if report, err := Apply(ctx, db, p, binding); err != nil || !report.Complete {
		t.Fatalf("apply: %v %#v", err, report)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_relations WHERE source_plugin_id='source' AND deleted_at IS NULL AND relation_json IS NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("SQL NULL relation was not persisted: count=%d error=%v", count, err)
	}
}

func TestApplyMySQLRecoversAfterPartialCommit(t *testing.T) {
	db := applyTestDB(t)
	ctx := context.Background()
	in := seedPublicationInput(t, db, model.PluginTypeSkill, false, false)
	first := in.Resources[0]
	second := resource("later", "later", "", "later body")
	second.Plugin.ListingState = model.PluginListingStateDelisted
	second.Plugin.CreatedAt, second.Plugin.UpdatedAt = first.Plugin.CreatedAt, first.Plugin.UpdatedAt
	s := second.Plugin
	if _, err := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at) VALUES (?,?,?,'[]',?,?,?,?,?,?,?,?,?,?)`, s.ID, s.Name, s.Type, s.OwnerUID, s.Visibility, s.ListingState, string(s.Manifest), string(s.Package), s.ManifestHash, s.PluginHash, s.Status, s.CreatedAt, s.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	in.Resources = append(in.Resources, second)
	p, err := Build(in, Policy{})
	if err != nil {
		t.Fatalf("build: %v %#v", err, p.Issues)
	}
	if len(p.Nodes) != 2 {
		t.Fatalf("expected two nodes, got %d", len(p.Nodes))
	}
	early, late := p.Nodes[0], p.Nodes[1]
	var binding DatabaseIdentity
	if err = db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
		t.Fatal(err)
	}
	// Drift only the later source, leaving the first transaction free to commit.
	if _, err = db.Exec(`UPDATE plugins SET plugin_name='drift' WHERE plugin_id=?`, late.SourceID); err != nil {
		t.Fatal(err)
	}
	report, err := Apply(ctx, db, p, binding)
	if err == nil || report.Applied != 1 || report.Complete {
		t.Fatalf("partial run: %v %#v", err, report)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM marketplace_conversion_receipt WHERE run_id=? AND target_id=?`, p.ID, early.Plugin.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("first receipt missing: count=%d error=%v", count, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=? AND version_id=?`, early.Plugin.ID, early.VersionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("first version missing: count=%d error=%v", count, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE plugin_id=?`, late.Plugin.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("later target was written: count=%d error=%v", count, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM marketplace_conversion_receipt WHERE run_id=? AND target_id=?`, p.ID, late.Plugin.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("later receipt was written: count=%d error=%v", count, err)
	}
	if _, err = db.Exec(`UPDATE plugins SET plugin_name=? WHERE plugin_id=?`, late.Source.Name, late.SourceID); err != nil {
		t.Fatal(err)
	}
	report, err = Apply(ctx, db, p, binding)
	if err != nil || !report.Complete || report.Verified != 2 {
		t.Fatalf("same-plan recovery: %v %#v", err, report)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, early.Plugin.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("first version duplicated: count=%d error=%v", count, err)
	}
	if _, err = db.Exec(`UPDATE plugins SET publisher='tampered' WHERE plugin_id=?`, early.Plugin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(ctx, db, p, binding); err == nil {
		t.Fatal("completed row tampering accepted on retry")
	}
}
