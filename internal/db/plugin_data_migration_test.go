package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/go-sql-driver/mysql"
)

// Exercise the new engine against the same disposable database as the service.
func applyCurrentMigration(t *testing.T, database *sql.DB) {
	t.Helper()
	ctx := context.Background()
	var binding migration.DatabaseIdentity
	if err := database.QueryRow("SELECT DATABASE(),@@server_uuid").Scan(&binding.Name, &binding.ServerUUID); err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(testDSN(t))
	if err != nil {
		t.Fatal("invalid test configuration")
	}
	cfg.DBName = binding.Name
	cfg.ParseTime = false
	cfg.Loc = time.UTC
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	raw, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("raw test connection failed")
	}
	defer raw.Close()
	in, issues, err := migration.InputFromDatabase(ctx, raw, nil)
	if err != nil || len(issues) != 0 {
		t.Fatalf("migration input: %v issues=%v", err, issues)
	}
	plan, err := migration.Build(in, migration.Policy{})
	if err != nil {
		t.Fatalf("migration plan: %v issues=%v", err, plan.Issues)
	}
	for _, n := range plan.Nodes {
		if err := pluginrepo.ValidateReferenceMetadata(n.Plugin.Type, n.Relations); err != nil && (n.Release || len(n.Relations) != 0) {
			t.Fatalf("migrated %s reference metadata: %v", n.Plugin.Type, err)
		}
		if n.Plugin.Type == model.PluginTypeExpert {
			archivedContext := false
			for _, f := range n.Files {
				if f.Path == "expert/context.json" {
					t.Fatal("reserved legacy context was not converted")
				}
				if f.Path == "migration/legacy/expert/context.json" {
					archivedContext = string(f.Data) == `{"role":"researcher"}`
				}
			}
			if !archivedContext {
				t.Fatal("original context bytes were not preserved")
			}
		}
	}
	if report, err := migration.Apply(ctx, database, plan, binding); err != nil || !report.Complete {
		t.Fatalf("apply: %v report=%+v", err, report)
	}
	for _, n := range plan.Nodes {
		if n.Plugin.Type != model.PluginTypeExpert {
			continue
		}
		var currentPackage, versionPackage, artifacts []byte
		if err := database.QueryRow(`SELECT p.plugin_json,v.plugin_json,v.artifacts_json FROM plugins p JOIN plugin_versions v ON v.version_id=p.current_version_id WHERE p.plugin_id=?`, n.Plugin.ID).Scan(&currentPackage, &versionPackage, &artifacts); err != nil {
			t.Fatal(err)
		}
		for _, raw := range [][]byte{currentPackage, versionPackage} {
			var pkg struct {
				Attachments []struct {
					Path string `json:"path"`
					Raw  string `json:"raw_content"`
				} `json:"attachments"`
			}
			if err := json.Unmarshal(raw, &pkg); err != nil {
				t.Fatal(err)
			}
			archived := false
			for _, a := range pkg.Attachments {
				if a.Path == "expert/context.json" {
					t.Fatal("reserved context persisted")
				}
				if a.Path == "migration/legacy/expert/context.json" {
					archived = a.Raw == `{"role":"researcher"}`
				}
			}
			if !archived {
				t.Fatal("stored context archive missing or changed")
			}
		}
		var files []struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(artifacts, &files); err != nil {
			t.Fatal(err)
		}
		archived := false
		for _, f := range files {
			if f.Path == "expert/context.json" {
				t.Fatal("reserved context in persisted artifacts")
			}
			archived = archived || f.Path == "migration/legacy/expert/context.json"
		}
		if !archived {
			t.Fatal("archive missing from persisted artifact manifest")
		}
	}
	// Retry must keep released identities and graph edges unchanged.
	if report, err := migration.Apply(ctx, database, plan, binding); err != nil || !report.Complete {
		t.Fatalf("retry: %v report=%+v", err, report)
	}
}

func TestSingleDatabaseMigrationRemainsEditableAndPublishable(t *testing.T) {
	for _, tc := range []struct{ old, migrated, next string }{
		{"v1", "1.0.0", "1.0.1"},
		{"1.2.999999999", "1.3.0", "1.3.1"},
		{"1.999999999.999999999", "2.0.0", "2.0.1"},
	} {
		t.Run(tc.old, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			const id = "migration-upgrade"
			seedMigrationRoot(t, database, id, "private", "published")
			if _, err := database.Exec(`UPDATE plugins SET current_version=? WHERE plugin_id=?`, tc.old, id); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE plugin_versions SET declared_version=? WHERE plugin_id=?`, tc.old, id); err != nil {
				t.Fatal(err)
			}
			applyCurrentMigration(t, database)
			svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true)
			owner := managedOwner()
			detail, err := svc.CurrentDetail(ctx, owner, id)
			if err != nil {
				t.Fatal(err)
			}
			p := detail.Plugin
			if p.CurrentVersion == nil || *p.CurrentVersion != tc.migrated {
				t.Fatalf("migrated version = %v, want %s", p.CurrentVersion, tc.migrated)
			}
			migratedID := *p.CurrentVersionID
			req := pluginsvc.WriteRequest{
				Name: p.Name, Type: p.Type, Visibility: p.Visibility, Version: tc.next,
				Tags: p.Tags, Manifest: p.Manifest,
				Package: json.RawMessage(strings.ReplaceAll(string(p.Package), "team instructions", "updated team instructions")),
			}
			for _, rel := range detail.Relations {
				req.Relations = append(req.Relations, pluginsvc.RelationRequest{
					ID: rel.ID, TargetPluginID: rel.TargetPluginID, TargetVersionID: rel.TargetVersionID,
					Type: rel.Type, SortOrder: rel.SortOrder, Data: rel.Data,
				})
			}
			updated, err := svc.Update(ctx, owner, id, req)
			if err != nil {
				t.Fatalf("edit after migration: %v", err)
			}
			if _, err = svc.Publish(ctx, owner, explicitTestPublication(t, database, id)); err != nil {
				t.Fatalf("publish after migration: %v", err)
			}
			published, err := svc.ManagedVersion(ctx, owner, id, *updated.Plugin.CurrentVersionID, false)
			if err != nil || published.Release == nil || published.Release.VersionLabel != tc.next || !published.IsPublished {
				t.Fatalf("new release: %+v err=%v", published, err)
			}
			previous, err := svc.ManagedVersion(ctx, owner, id, migratedID, false)
			if err != nil || previous.Release == nil || previous.Release.VersionLabel != tc.migrated || strings.Contains(string(previous.Package), "updated team instructions") {
				t.Fatalf("migrated release mutated: %+v err=%v", previous, err)
			}
		})
	}
}

func seedMigrationRoot(t *testing.T, database *sql.DB, id, visibility, listing string) {
	t.Helper()
	manifest := func(kind, name string) string {
		b, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-manifest-2.0.json", "plugin_name": name, "plugin_type": kind, "name": name, "description": "fixture", "labels": []any{}, "examples": []any{}})
		return string(b)
	}
	pkg := func(files map[string]string) string {
		atts := []any{}
		for path, content := range files {
			atts = append(atts, map[string]any{"path": path, "content_type": "raw", "mime_type": "text/plain", "raw_content": content})
		}
		b, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-package-2.0.json", "attachments": atts})
		return string(b)
	}
	seed(t, database, seedPlugin{id: id, typ: "expert_team", visibility: visibility, listingState: listing, currentVersion: "1.2.3", currentVersionID: id + "-old", manifest: manifest("expert_team", id), pkg: pkg(map[string]string{"AGENTS.md": "team instructions"})})
	seed(t, database, seedPlugin{id: id + "-expert", typ: "expert", visibility: "private", listingState: listing, embedded: true, manifest: manifest("expert", id+"-expert"), pkg: pkg(map[string]string{"AGENTS.md": "member instructions", "mcp.json": `{"mcpServers":{"search":{"command":"search"}}}`, "expert/context.json": `{"role":"researcher"}`})})
	seed(t, database, seedPlugin{id: id + "-skill", typ: "skill", visibility: "private", listingState: listing, embedded: true, manifest: manifest("skill", id+"-skill"), pkg: pkg(map[string]string{"SKILL.md": "---\nname: skill\ndescription: fixture\n---\nbody"})})
	for _, pid := range []string{id, id + "-expert", id + "-skill"} {
		if _, err := database.Exec(`UPDATE plugins SET plugin_name=? WHERE plugin_id=?`, pid, pid); err != nil {
			t.Fatal(err)
		}
	}
	for _, pid := range []string{id, id + "-expert", id + "-skill"} {
		var name, kind, manifest, pkg, tags string
		if err := database.QueryRow(`SELECT plugin_name,plugin_type,manifest_json,plugin_json,tags_json FROM plugins WHERE plugin_id=?`, pid).Scan(&name, &kind, &manifest, &pkg, &tags); err != nil {
			t.Fatal(err)
		}
		docs, err := pluginsvc.CanonicalizeMigratedDocuments(name, model.PluginType(kind), json.RawMessage(tags), json.RawMessage(manifest), json.RawMessage(pkg), "space-a")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(`UPDATE plugins SET manifest_hash=?,plugin_hash=? WHERE plugin_id=?`, docs.ManifestHash, docs.PluginHash, pid); err != nil {
			t.Fatal(err)
		}
	}
	seedVersionRow(t, database, id+"-old", id, "1")
	if _, err := database.Exec(`UPDATE plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id SET v.manifest_hash=p.manifest_hash,v.plugin_hash=p.plugin_hash WHERE v.version_id=?`, id+"-old"); err != nil {
		t.Fatal(err)
	}
	seedRelation(t, database, id+"-member-edge", id, id+"-expert", "expert_team_expert")
	if _, err := database.Exec(`UPDATE plugin_relations SET relation_json=JSON_OBJECT('source_index',7,'member_key','m','role','researcher','is_leader',true),sort_order=7 WHERE relation_id=?`, id+"-member-edge"); err != nil {
		t.Fatal(err)
	}
	seedRelation(t, database, id+"-skill-edge", id+"-expert", id+"-skill", "expert_skill")
}
