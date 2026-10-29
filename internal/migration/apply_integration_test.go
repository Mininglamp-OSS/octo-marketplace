package migration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/artifacts"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	mysql "github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

func applyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MIGRATION_TEST_MYSQL_DSN")
	if dsn == "" {
		if os.Getenv("MIGRATION_TEST_MYSQL_REQUIRED") == "1" {
			t.Fatal("MIGRATION_TEST_MYSQL_DSN required")
		}
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
	n := Node{Evidence: map[string]SourceEvidence{source.ID: testSourceEvidence(t, db, source.ID)}, SourceRowHash: testSourceRowHash(t, db, source.ID), SourceID: source.ID, Source: source, SourcePublishedVersionID: stringPtrApply("old-release"), SourceRelease: &model.PluginVersionRelease{PluginID: "source", VersionID: "old-release", VersionLabel: "0.1.0", ReleasedBy: "owner", ReleasedAt: now}, Plugin: target, VersionID: "version", VersionLabel: "1.0.0", Clone: true, Release: true}
	categories, err := captureCategorySnapshot(ctx, db, false)
	if err != nil {
		t.Fatal(err)
	}
	p := Plan{CategorySnapshotCaptured: true, CategorySnapshot: categories, ID: "run", SnapshotID: "snapshot", Nodes: []Node{n}, Mappings: []Mapping{{SourceID: "source", TargetID: "target", VersionID: "version", Disposition: "canonical"}}}
	r, e := Apply(ctx, db, p, binding)
	if e != nil || !r.Complete {
		t.Fatalf("apply: %v %#v", e, r)
	}
	if e = CheckPlanSources(ctx, db, p, binding); e != nil {
		t.Fatal("resume preflight rejected completed receipt", e)
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
	// A new review request on the migrated target invalidates its completed receipt.
	if _, e = db.Exec(`INSERT INTO plugin_review_requests (review_id,plugin_id,space_id,status,kind,version,manifest_json,plugin_json,relations_json,manifest_hash,plugin_hash,applicant_uid,applicant_name,submitted_at,created_at,updated_at) VALUES ('target-review','target','space','rejected','create','1.0.0','{}','{}','[]','sha256:m','sha256:p','owner','Owner',?,?,?)`, now, now, now); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(ctx, db, p, binding); e == nil {
		t.Fatal("post-receipt target review drift accepted")
	}
	if _, e = db.Exec(`DELETE FROM plugin_review_requests WHERE review_id='target-review'`); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(ctx, db, p, binding); e != nil {
		t.Fatal("restored target state rejected", e)
	}
	// Receipts also bind retained original history, not just the new target.
	if _, e = db.Exec(`UPDATE plugin_versions SET changelog='changed history' WHERE version_id='old-release'`); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(ctx, db, p, binding); e == nil {
		t.Fatal("retained source history tampering accepted")
	}
	if _, e = db.Exec(`UPDATE plugin_versions SET changelog=NULL WHERE version_id='old-release'`); e != nil {
		t.Fatal(e)
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
	in.Resources[0].SourceRowHash = testSourceRowHash(t, db, in.Resources[0].Plugin.ID)
	in.Resources[0].Evidence = testSourceEvidence(t, db, in.Resources[0].Plugin.ID)
	in.CategorySnapshot, _ = captureCategorySnapshot(context.Background(), db, false)
	in.CategorySnapshotCaptured = true
	return in
}

func TestApplyMySQLCurrentAndReleasedAttachmentSidecarsMatchPlan(t *testing.T) {
	for _, kind := range []model.PluginType{model.PluginTypeSkill, model.PluginTypeExpert} {
		t.Run(string(kind), func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			in := seedPublicationInput(t, db, kind, false, true)
			source := in.Resources[0]
			data := []byte{0, 255, 1}
			entry := "SKILL.md"
			if kind == model.PluginTypeExpert {
				entry = "AGENTS.md"
			}
			pkg, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-package-2.0.json", "attachments": []any{
				map[string]any{"path": entry, "content_type": "raw", "mime_type": "text/markdown", "raw_content": "body"},
				map[string]any{"path": "asset.bin", "content_type": "storage", "mime_type": "application/octet-stream", "content_size": len(data), "content_hash": "sha256:" + digest(data)},
			}})
			oldKey := "platform/plugins/source/attachments/legacy-asset"
			sidecar, _ := json.Marshal(map[string]string{"asset.bin": oldKey})
			if _, err := db.Exec(`UPDATE plugins SET plugin_json=?,attachment_keys_json=? WHERE plugin_id='source'`, string(pkg), string(sidecar)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE plugin_versions SET plugin_json=?,attachment_keys_json=? WHERE version_id='old'`, string(pkg), string(sidecar)); err != nil {
				t.Fatal(err)
			}
			readSource := func(_ context.Context, key string, _ int64) ([]byte, error) {
				if key != oldKey {
					t.Fatalf("unexpected source object %q", key)
				}
				return data, nil
			}
			in, issues, err := InputFromDatabase(ctx, db, readSource)
			if err != nil || len(issues) != 0 {
				t.Fatalf("input %v %+v", err, issues)
			}
			p, err := Build(in, Policy{})
			if err != nil {
				t.Fatalf("build %v %+v", err, p.Issues)
			}
			n := p.Nodes[0]
			if (kind == model.PluginTypeSkill) != n.Clone {
				t.Fatal("expected clone and in-place paths")
			}
			store := storage.NewLocal(t.TempDir(), "http://local")
			if err := PublishObjects(ctx, p, store); err != nil {
				t.Fatal(err)
			}
			var binding DatabaseIdentity
			if err := db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			if r, err := Apply(ctx, db, p, binding, readSource); err != nil || !r.Complete {
				t.Fatalf("apply %v %+v", err, r)
			}
			var currentKeys, versionKeys, currentPackage []byte
			if err := db.QueryRow(`SELECT p.attachment_keys_json,v.attachment_keys_json,p.plugin_json FROM plugins p JOIN plugin_versions v ON v.version_id=p.current_version_id WHERE p.plugin_id=?`, n.Plugin.ID).Scan(&currentKeys, &versionKeys, &currentPackage); err != nil {
				t.Fatal(err)
			}
			if !sameJSON(currentKeys, n.Plugin.AttachmentKeys) || !sameJSON(versionKeys, n.Plugin.AttachmentKeys) || sameJSON(currentKeys, sidecar) {
				t.Fatal("current/version sidecars do not match rewritten plan")
			}
			live := n.Plugin
			live.Package, live.AttachmentKeys = currentPackage, currentKeys
			files, err := artifacts.Resolve(ctx, &live, func(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
				r, err := store.GetObject(ctx, key)
				if err != nil {
					return nil, err
				}
				defer r.Close()
				return io.ReadAll(io.LimitReader(r, maxBytes+1))
			}, artifacts.DefaultLimits())
			if err != nil || len(files) != 2 {
				t.Fatalf("current artifact read %v %+v", err, files)
			}
			if err := db.QueryRow(`SELECT attachment_keys_json FROM plugin_versions WHERE version_id='old'`).Scan(&versionKeys); err != nil || !sameJSON(versionKeys, sidecar) {
				t.Fatal("source historical sidecar changed", err)
			}
			if n.Clone && n.Plugin.ID == source.Plugin.ID {
				t.Fatal("clone identity not changed")
			}
			if _, err := db.Exec(`UPDATE plugins SET attachment_keys_json=JSON_OBJECT('asset.bin','platform/plugins/foreign/attachments/file') WHERE plugin_id=?`, n.Plugin.ID); err != nil {
				t.Fatal(err)
			}
			if r, err := Verify(ctx, db, p, binding); err == nil || r.Complete {
				t.Fatal("current sidecar tampering passed final verification")
			}
		})
	}
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
				// A referenced release cannot be deleted while its FK pointer exists.
				if _, err = db.Exec(`UPDATE plugins SET published_version_id=NULL WHERE plugin_id='source'`); err == nil {
					_, err = db.Exec(`DELETE FROM plugin_version_releases WHERE plugin_id='source'`)
				}
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
	var connectorPackage map[string]any
	if err := json.Unmarshal(connector.Plugin.Package, &connectorPackage); err != nil {
		t.Fatal(err)
	}
	connectorPackage["connector"] = map[string]string{"type": "mcp", "source": "fixture"}
	connector.Plugin.Package, _ = json.Marshal(connectorPackage)
	connector.Plugin.CreatedAt, connector.Plugin.UpdatedAt = in.Resources[0].Plugin.CreatedAt, in.Resources[0].Plugin.UpdatedAt
	cp := connector.Plugin
	if _, err := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at) VALUES (?,?,?,'[]',?,?,?,?,?,?,?,?,?,?)`, cp.ID, cp.Name, cp.Type, cp.OwnerUID, cp.Visibility, cp.ListingState, string(cp.Manifest), string(cp.Package), cp.ManifestHash, cp.PluginHash, cp.Status, cp.CreatedAt, cp.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	connector.SourceRowHash = testSourceRowHash(t, db, connector.Plugin.ID)
	connector.Evidence = testSourceEvidence(t, db, connector.Plugin.ID)
	in.Resources = append(in.Resources, connector)
	in.Resources[0].Relations = []model.PluginRelation{{ID: "edge", SourcePluginID: "source", SourcePluginType: model.PluginTypeExpert, TargetPluginID: "connector", TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1, CreatedAt: cp.CreatedAt, UpdatedAt: cp.UpdatedAt}}
	if _, err := db.Exec(`INSERT INTO plugin_relations (relation_id,source_plugin_id,target_plugin_id,relation_type,relation_json,created_by,created_at,updated_at) VALUES ('edge','source','connector','expert_connector',NULL,'owner',?,?)`, cp.CreatedAt, cp.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	in.Resources[0].Evidence = testSourceEvidence(t, db, "source")
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
	second.SourceRowHash = testSourceRowHash(t, db, second.Plugin.ID)
	second.Evidence = testSourceEvidence(t, db, second.Plugin.ID)
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
	if err == nil || report.Applied != 1 || report.Complete || report.CommitState != "partial" || len(report.CommittedIDs) != 1 || report.FailedResourceID != late.Plugin.ID {
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
	// A persistent conflict must remain stopped on another same-checkpoint attempt.
	repeated, retryErr := Apply(ctx, db, p, binding)
	if retryErr == nil || repeated.CommitState != "partial" || len(repeated.CommittedIDs) != 1 || repeated.CommittedIDs[0] != early.Plugin.ID {
		t.Fatalf("persistent failure lost recovery state: %v %#v", retryErr, repeated)
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

func testSourceRowHash(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var hash string
	err := readDatabaseRows(context.Background(), db, `SELECT * FROM plugins WHERE plugin_id=?`, func(row map[string][]byte) error {
		var err error
		hash, err = sourceRowDigest(row)
		return err
	}, id)
	if err != nil || hash == "" {
		t.Fatalf("source row evidence: %v", err)
	}
	return hash
}

func TestApplyMySQLRejectsSourceMetadataDrift(t *testing.T) {
	for _, change := range []string{
		`plugin_type='expert'`, `category_id='changed'`, `tags_json=JSON_ARRAY('changed')`,
		`publisher='changed'`, `creator_name='changed'`, `created_by_type='bot'`,
		`created_by_bot_uid='changed'`, `created_by_bot_name='changed'`, `icon='changed'`,
		`tool_count=42`, `rating=4`, `attachment_keys_json=JSON_OBJECT('file','changed')`,
		`current_version='2.0.0'`, `created_at=DATE_ADD(created_at, INTERVAL 1 SECOND)`,
	} {
		t.Run(change, func(t *testing.T) {
			db := applyTestDB(t)
			in := seedPublicationInput(t, db, model.PluginTypeSkill, false, false)
			p, err := Build(in, Policy{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE plugins SET ` + change + ` WHERE plugin_id='source'`); err != nil {
				t.Fatal(err)
			}
			var binding DatabaseIdentity
			if err := db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			report, err := Apply(context.Background(), db, p, binding)
			if err == nil || report.Applied != 0 {
				t.Fatalf("changed source applied: %v %#v", err, report)
			}
			for _, query := range []string{
				`SELECT COUNT(*) FROM plugins WHERE plugin_id <> 'source'`,
				`SELECT COUNT(*) FROM plugin_versions WHERE version_id <> 'old'`,
				`SELECT COUNT(*) FROM plugin_audit_logs`,
				`SELECT COUNT(*) FROM marketplace_conversion_receipt`,
			} {
				var count int
				if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
					t.Fatalf("source drift left writes: %s count=%d error=%v", query, count, err)
				}
			}
		})
	}
}

func testSourceEvidence(t *testing.T, db *sql.DB, id string) SourceEvidence {
	t.Helper()
	tables, err := captureSourceTables(context.Background(), db, id, false)
	if err != nil {
		t.Fatal(err)
	}
	return SourceEvidence{RowHash: testSourceRowHash(t, db, id), Tables: tables}
}

func TestApplyMySQLRejectsRelatedSourceDriftAndRecovers(t *testing.T) {
	for _, kind := range []string{"relation", "version", "source placement", "mapped placement"} {
		t.Run(kind, func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			in := seedPublicationInput(t, db, model.PluginTypeSkill, false, false)
			now := in.Resources[0].Plugin.UpdatedAt
			placementOwner := "source"
			if kind == "mapped placement" {
				duplicate := in.Resources[0]
				duplicate.Plugin.ID = "duplicate"
				duplicate.Plugin.UpdatedAt = now.Add(time.Second)
				if _, err := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at) SELECT 'duplicate',plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,DATE_ADD(updated_at,INTERVAL 1 SECOND) FROM plugins WHERE plugin_id='source'`); err != nil {
					t.Fatal(err)
				}
				in.Resources = append(in.Resources, duplicate)
				placementOwner = "duplicate"
			}
			if kind == "source placement" || kind == "mapped placement" {
				if _, err := db.Exec(`INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,visible,sort_order,created_at,updated_at) VALUES ('placement','default',?,1,7,?,?)`, placementOwner, now, now); err != nil {
					t.Fatal(err)
				}
			}
			for i := range in.Resources {
				r := &in.Resources[i]
				r.SourceRowHash = testSourceRowHash(t, db, r.Plugin.ID)
				r.Evidence = testSourceEvidence(t, db, r.Plugin.ID)
			}
			p, err := Build(in, Policy{})
			if err != nil {
				t.Fatalf("build: %v %#v", err, p.Issues)
			}
			if len(p.Nodes) != 1 {
				t.Fatalf("dedup failed: %#v", p.Mappings)
			}
			var undo string
			switch kind {
			case "relation":
				_, err = db.Exec(`INSERT INTO plugin_relations (relation_id,source_plugin_id,target_plugin_id,relation_type,created_by,created_at,updated_at) VALUES ('new-edge','source','other','expert_skill','owner',?,?)`, now, now)
				undo = `DELETE FROM plugin_relations WHERE relation_id='new-edge'`
			case "version":
				_, err = db.Exec(`UPDATE plugin_versions SET changelog='changed' WHERE version_id='old'`)
				undo = `UPDATE plugin_versions SET changelog=NULL WHERE version_id='old'`
			default:
				_, err = db.Exec(`UPDATE plugin_placements SET sort_order=99 WHERE placement_id='placement'`)
				undo = `UPDATE plugin_placements SET sort_order=7 WHERE placement_id='placement'`
			}
			if err != nil {
				t.Fatal(err)
			}
			var binding DatabaseIdentity
			if err = db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			report, err := Apply(ctx, db, p, binding)
			if err == nil || report.Applied != 0 || report.CommitState != "none" {
				t.Fatalf("related source changed but applied: %v %#v", err, report)
			}
			for _, query := range []string{`SELECT COUNT(*) FROM plugin_audit_logs`, `SELECT COUNT(*) FROM marketplace_conversion_receipt`, `SELECT COUNT(*) FROM plugin_versions WHERE version_id<>'old'`} {
				var count int
				if err = db.QueryRow(query).Scan(&count); err != nil || count != 0 {
					t.Fatalf("unexpected migration writes: %s count=%d err=%v", query, count, err)
				}
			}
			// Restore the exact fixture source; production recovery must use the documented
			// source reconciliation/backup procedure, never silently replace the checkpoint.
			if _, err = db.Exec(undo); err != nil {
				t.Fatal(err)
			}
			report, err = Apply(ctx, db, p, binding)
			if err != nil || !report.Complete || report.CommitState != "complete" {
				t.Fatalf("same-plan recovery failed: %v %#v", err, report)
			}
		})
	}
}

func TestApplyMySQLPersistsMixedActiveInactiveAndDeletedMappings(t *testing.T) {
	db := applyTestDB(t)
	ctx := context.Background()
	seedPublicationInput(t, db, model.PluginTypeSkill, false, false)
	for _, id := range []string{"inactive", "deleted"} {
		if _, err := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at,deleted_at)
 SELECT ?,plugin_name,plugin_type,tags_json,owner_uid,visibility,'draft',manifest_json,plugin_json,manifest_hash,plugin_hash,0,created_at,updated_at,IF(?='deleted',UTC_TIMESTAMP(3),NULL) FROM plugins WHERE plugin_id='source'`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	in, issues, err := InputFromDatabase(ctx, db, nil)
	if err != nil || len(issues) > 0 {
		t.Fatalf("input %v %+v", err, issues)
	}
	p, err := Build(in, Policy{})
	if err != nil {
		t.Fatalf("build %v %+v", err, p.Issues)
	}
	var binding DatabaseIdentity
	if err := db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if r, err := Apply(ctx, db, p, binding); err != nil || !r.Complete {
			t.Fatalf("mixed run/resume: %v %+v", err, r)
		}
		if r, err := Verify(ctx, db, p, binding); err != nil || !r.Complete {
			t.Fatalf("mixed verification: %v %+v", err, r)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM marketplace_conversion_mapping WHERE run_id=?`, p.ID).Scan(&count); err != nil || count != len(p.Mappings) {
		t.Fatalf("mapping count %d: %v", count, err)
	}
	var target, disposition string
	if err := db.QueryRow(`SELECT target_id,disposition FROM marketplace_conversion_mapping WHERE run_id=? AND source_id='deleted'`, p.ID).Scan(&target, &disposition); err != nil || target != "" || disposition != "deleted_history" {
		t.Fatalf("deleted mapping lost: %q %q %v", target, disposition, err)
	}
}

func TestPendingReviewBlocksBeforeConversionWritesMySQL(t *testing.T) {
	db := applyTestDB(t)
	ctx := context.Background()
	seedPublicationInput(t, db, model.PluginTypeSkill, false, false)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := db.Exec(`INSERT INTO plugin_review_requests (review_id,plugin_id,space_id,status,kind,version,manifest_json,plugin_json,relations_json,manifest_hash,plugin_hash,applicant_uid,applicant_name,submitted_at,created_at,updated_at) VALUES ('pending-review','source','space-a','pending','create','1.0.0','{}','{}','[]','sha256:m','sha256:p','owner','Owner',?,?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	in, issues, err := InputFromDatabase(ctx, db, nil)
	if err != nil || len(issues) > 0 {
		t.Fatalf("input %v %+v", err, issues)
	}
	p, err := Build(in, Policy{})
	if err != ErrIssues {
		t.Fatalf("pending review accepted: %v", err)
	}
	if _, err := Apply(ctx, db, p, DatabaseIdentity{}); err != ErrIssues {
		t.Fatalf("pending plan reached SQL: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='marketplace_conversion_run'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pending plan left conversion writes: %d %v", count, err)
	}
}

func TestMigrationPreservesAllCapturedScalarMetadataMySQL(t *testing.T) {
	for _, kind := range []model.PluginType{model.PluginTypeSkill, model.PluginTypeExpert} {
		t.Run(string(kind), func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			seedPublicationInput(t, db, kind, false, false)
			if _, err := db.Exec(`UPDATE plugins SET category_id='retained-category',publisher='original publisher',creator_name='original creator',created_by_type='bot',created_by_bot_uid='bot',created_by_bot_name='Bot',icon='🧪',tool_count=7,rating=4 WHERE plugin_id='source'`); err != nil {
				t.Fatal(err)
			}
			in, issues, err := InputFromDatabase(ctx, db, nil)
			if err != nil || len(issues) > 0 {
				t.Fatalf("input %v %+v", err, issues)
			}
			p, err := Build(in, Policy{})
			if err != nil {
				t.Fatalf("build %v %+v", err, p.Issues)
			}
			var binding DatabaseIdentity
			if err := db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			if r, err := Apply(ctx, db, p, binding); err != nil || !r.Complete {
				t.Fatalf("apply %v %+v", err, r)
			}
			var values []string
			err = readDatabaseRows(ctx, db, `SELECT category_id,tags_json,publisher,creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,icon,tool_count,rating,created_at FROM plugins WHERE plugin_id IN (?,?) ORDER BY plugin_id`, func(row map[string][]byte) error {
				b, _ := json.Marshal(row)
				values = append(values, string(b))
				return nil
			}, "source", p.Nodes[0].Plugin.ID)
			if err != nil || len(values) == 0 {
				t.Fatal("metadata read failed", err)
			}
			if len(values) == 2 && values[0] != values[1] {
				t.Fatal("clone changed captured scalar metadata")
			}
			// For in-place conversion compare the captured model to the database projection.
			got, err := pluginrepo.New(db).Get(ctx, pluginrepo.Scope{Admin: true}, p.Nodes[0].Plugin.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := in.Resources[0].Plugin
			if !reflect.DeepEqual(got.CategoryID, want.CategoryID) || got.Publisher != want.Publisher || got.CreatorName != want.CreatorName || got.CreatedByType != want.CreatedByType || !reflect.DeepEqual(got.CreatedByBotUID, want.CreatedByBotUID) || !reflect.DeepEqual(got.CreatedByBotName, want.CreatedByBotName) || got.Icon != want.Icon || got.ToolCount != want.ToolCount || !reflect.DeepEqual(got.Rating, want.Rating) || !got.CreatedAt.Equal(want.CreatedAt) {
				t.Fatal("captured scalar metadata not preserved")
			}
		})
	}
}

func TestExtractedMCPPersistedConfigAndAudienceAndIconMySQL(t *testing.T) {
	for _, visibility := range []string{"system", "space", "private"} {
		t.Run(visibility, func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			in := seedPublicationInput(t, db, model.PluginTypeExpert, false, true)
			config := `{"mcpServers":{"search":{"url":"https://example.invalid/mcp?key=fixture-only-value","headers":{"Authorization":"Bearer fixture-only-value"},"env":{"REGION":"cn","TOKEN":"fixture-only-value"}}},"rootOptions":{"enabled":true}}`
			pkg := testPackage([]File{{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("original instructions")}, {Path: "mcp.json", MIME: "application/json", Data: []byte(config)}})
			hash, err := libplugin.ComputePluginHash(in.Resources[0].Plugin.Manifest, pkg)
			if err != nil {
				t.Fatal(err)
			}
			var space any = "space-a"
			if visibility == "system" {
				space = nil
			}
			iconKey := "mcp-icons/original-upload/logo.png"
			if _, err := db.Exec(`UPDATE plugins SET plugin_json=?,plugin_hash=?,visibility=?,space_id=?,listing_state='published',is_embedded=1,owner_uid='owner',icon=? WHERE plugin_id='source'`, string(pkg), hash, visibility, space, iconKey); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE plugin_versions SET plugin_json=?,plugin_hash=? WHERE version_id='old'`, string(pkg), hash); err != nil {
				t.Fatal(err)
			}
			repo := pluginrepo.New(db)
			scopes := []pluginrepo.Scope{{CallerUID: "owner", SpaceID: "space-a"}, {CallerUID: "other", SpaceID: "space-a"}, {CallerUID: "other", SpaceID: "space-b"}}
			allowed := make([]bool, len(scopes))
			for i, scope := range scopes {
				_, err := repo.Get(ctx, scope, "source")
				allowed[i] = err == nil
			}
			store := storage.NewLocal(t.TempDir(), "http://local")
			if err := store.PutObject(ctx, iconKey, bytes.NewReader([]byte("icon")), 4, "image/png"); err != nil {
				t.Fatal(err)
			}
			read := artifacts.NewReader(store.GetObject)
			in, issues, err := InputFromDatabase(ctx, db, read)
			if err != nil || len(issues) > 0 {
				t.Fatalf("input %v %+v", err, issues)
			}
			if in.Resources[0].IconFile == nil || len(in.Resources[0].Evidence.Objects) != 1 {
				t.Fatal("scalar icon missing from frozen evidence")
			}
			p, err := Build(in, Policy{})
			if err != nil {
				t.Fatalf("build %v %+v", err, p.Issues)
			}
			for _, n := range p.Nodes {
				if !n.Clone || n.Plugin.ID == "source" {
					t.Fatal("icon regression must cover cloned identities")
				}
			}
			if err := PublishObjects(ctx, p, store); err != nil {
				t.Fatal(err)
			}
			var binding DatabaseIdentity
			if err := db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			if r, err := Apply(ctx, db, p, binding, read); err != nil || !r.Complete {
				t.Fatalf("apply %v %+v", err, r)
			}
			found := false
			for _, n := range p.Nodes {
				if n.Plugin.Type != model.PluginTypeConnector {
					continue
				}
				found = true
				if n.Plugin.Icon != iconKey || n.IconFile == nil {
					t.Fatal("split connector lost verified global icon")
				}
				var persisted []byte
				if err := db.QueryRow(`SELECT plugin_json FROM plugins WHERE plugin_id=?`, n.Plugin.ID).Scan(&persisted); err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Attachments []struct {
						Path string `json:"path"`
						Raw  string `json:"raw_content"`
					} `json:"attachments"`
				}
				if json.Unmarshal(persisted, &doc) != nil {
					t.Fatal("invalid persisted package")
				}
				same := false
				for _, a := range doc.Attachments {
					if a.Path == "mcp.json" {
						same = sameJSON([]byte(a.Raw), []byte(config))
					}
				}
				if !same {
					t.Fatal("persisted MCP configuration changed")
				}
				for i, scope := range scopes {
					_, err := repo.Get(ctx, scope, n.Plugin.ID)
					if (err == nil) != allowed[i] {
						t.Fatal("extraction broadened or narrowed source read audience")
					}
				}
			}
			if !found {
				t.Fatal("no extracted connector")
			}
			inventory, err := InspectPlanObjects(ctx, p, store)
			if err != nil || len(inventory) != 1 || inventory[0].Status != "verified" {
				t.Fatalf("icon inventory missing: %v %+v", err, inventory)
			}
			if err := store.PutObject(ctx, iconKey, bytes.NewReader([]byte("evil")), 4, "image/png"); err != nil {
				t.Fatal(err)
			}
			if err := VerifyPlanSourceObjects(ctx, p, read); err == nil {
				t.Fatal("changed scalar icon accepted as source")
			}
			if err := VerifyObjects(ctx, p, store); err == nil {
				t.Fatal("changed retained icon accepted by final verification")
			}
			inventory, err = InspectPlanObjects(ctx, p, store)
			if err != nil || len(inventory) != 1 || inventory[0].Status != "verification_failed" {
				t.Fatal("icon drift not shown in inventory")
			}
		})
	}
}
