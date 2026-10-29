package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/stockmigration"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestStockMigrationRejectsDraftNameDivergenceFromRelease(t *testing.T) {
	for _, mode := range []string{"saved-draft", "metadata-only"} {
		t.Run(mode, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			const root = "name-source"
			seedStockRoot(t, database, root, "private", "published")
			// Establish a real old release, rather than merely a legacy listing.
			if _, err := database.Exec(`UPDATE plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id SET v.manifest_json=p.manifest_json,v.plugin_json=p.plugin_json,v.manifest_hash=p.manifest_hash,v.plugin_hash=p.plugin_hash WHERE v.version_id=?`, root+"-old"); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,'1.2.3','owner',NOW(3))`, root, root+"-old"); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE plugins SET published_version_id=?,published_name_key=?,name_key=?,name_scope='space-a' WHERE plugin_id=?`, root+"-old", root, root, root); err != nil {
				t.Fatal(err)
			}
			const draftName = "New draft name"
			wantException := "invalid_source_document"
			if mode == "saved-draft" {
				var manifest, pkg, tags []byte
				if err := database.QueryRow(`SELECT manifest_json,plugin_json,tags_json FROM plugins WHERE plugin_id=?`, root).Scan(&manifest, &pkg, &tags); err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err := json.Unmarshal(manifest, &fields); err != nil {
					t.Fatal(err)
				}
				fields["plugin_name"] = draftName
				manifest, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				docs, err := pluginsvc.CanonicalizeMigratedDocuments(draftName, model.PluginTypeExpertTeam, tags, manifest, pkg, "space-a")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`INSERT INTO plugin_versions(version_id,plugin_id,version,declared_version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,created_by,created_at) SELECT ?,plugin_id,2,'1.2.4',?,?,?, ?,relations_json,'owner',NOW(3) FROM plugin_versions WHERE version_id=?`, root+"-draft", string(docs.Manifest), string(docs.Package), docs.ManifestHash, docs.PluginHash, root+"-old"); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`UPDATE plugins SET plugin_name=?,name_key=?,manifest_json=?,plugin_json=?,manifest_hash=?,plugin_hash=?,current_version_id=?,current_version='1.2.4' WHERE plugin_id=?`, draftName, "new draft name", string(docs.Manifest), string(docs.Package), docs.ManifestHash, docs.PluginHash, root+"-draft", root); err != nil {
					t.Fatal(err)
				}
				wantException = "unpublished_current_changes"
			} else if _, err := database.Exec(`UPDATE plugins SET plugin_name=?,name_key=? WHERE plugin_id=?`, draftName, "new draft name", root); err != nil {
				t.Fatal(err)
			}
			data, err := stockmigration.Read(ctx, database, 1000)
			if err != nil {
				t.Fatal(err)
			}
			for _, resource := range data.Resources {
				if resource.Plugin.ID == root && (resource.PublishedName != root || resource.Plugin.Name != draftName) {
					t.Fatalf("fixture did not separate draft and release names: %+v", resource)
				}
			}
			plan := stockmigration.Build(data)
			found := false
			for _, exception := range plan.Exceptions {
				found = found || exception.ResourceID == root && exception.Code == wantException
			}
			if !found {
				t.Fatalf("missing %s guard: %+v", wantException, plan.Exceptions)
			}
			inventory := func() string {
				var value string
				if err := database.QueryRow(`SELECT JSON_ARRAY((SELECT COUNT(*) FROM plugins),(SELECT COUNT(*) FROM plugin_versions),(SELECT COUNT(*) FROM plugin_version_releases),(SELECT COUNT(*) FROM plugin_relations),(SELECT COUNT(*) FROM plugin_stock_migration_batches),(SELECT COUNT(*) FROM plugin_stock_migration_roots),(SELECT JSON_ARRAY(plugin_name,name_key,published_name_key,current_version_id,published_version_id,manifest_hash,plugin_hash) FROM plugins WHERE plugin_id=?))`, root).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			before := inventory()
			if count, err := stockmigration.ApplyRoot(ctx, database, 1000, "name-divergence", root, plan.Digest, true); count != 0 || !errors.Is(err, stockmigration.ErrExceptions) {
				t.Fatalf("draft published by migration: nodes=%d err=%v", count, err)
			}
			if after := inventory(); after != before {
				t.Fatalf("rejected migration changed inventory: before=%s after=%s", before, after)
			}
		})
	}
}
