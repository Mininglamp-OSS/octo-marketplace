package db

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/stockmigration"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

func TestStockMigrationResolvesFilesBeforeLocksAndRejectsChangedSource(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(strconv.FormatBool(change), func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			seedStockRoot(t, database, "prelock", "private", "published")
			files := map[string]string{"SKILL.md": "---\nname: skill\ndescription: fixture\n---\nbody"}
			resolver := func(context.Context, *model.Plugin) (map[string]string, error) { return files, nil }
			d, err := stockmigration.ReadWithResolver(ctx, database, 1000, resolver)
			if err != nil {
				t.Fatal(err)
			}
			plan := stockmigration.Build(d)
			calls := 0
			applyResolver := func(ctx context.Context, _ *model.Plugin) (map[string]string, error) {
				calls++
				// A separate transaction must be able to lock/update source rows while
				// file bytes are being resolved. This times out with the old ordering.
				writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				name := "prelock"
				if change {
					name = "concurrent edit"
				}
				if _, err := database.ExecContext(writeCtx, `UPDATE plugins SET plugin_name=? WHERE plugin_id='prelock'`, name); err != nil {
					return nil, err
				}
				return files, nil
			}
			n, err := stockmigration.ApplyRootWithResolver(ctx, database, 1000, "prelock-batch", "prelock", plan.Digest, true, applyResolver)
			if calls != 1 {
				t.Fatalf("resolver calls=%d", calls)
			}
			if change {
				if !errors.Is(err, stockmigration.ErrConflict) || n != 0 {
					t.Fatalf("changed source: n=%d err=%v", n, err)
				}
				var records int
				if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_stock_migration_roots`).Scan(&records); err != nil || records != 0 {
					t.Fatalf("audit count=%d err=%v", records, err)
				}
			} else if err != nil || n != 4 {
				t.Fatalf("apply n=%d err=%v", n, err)
			}
		})
	}
}

func TestStockMigrationRemainsEditableAndPublishable(t *testing.T) {
	for _, tc := range []struct{ old, migrated, next string }{
		{"v1", "1.0.0", "1.0.1"},
		{"1.2.999999999", "1.3.0", "1.3.1"},
		{"1.999999999.999999999", "2.0.0", "2.0.1"},
	} {
		t.Run(tc.old, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			const id = "migration-upgrade"
			seedStockRoot(t, database, id, "private", "published")
			if _, err := database.Exec(`UPDATE plugins SET current_version=? WHERE plugin_id=?`, tc.old, id); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE plugin_versions SET declared_version=? WHERE plugin_id=?`, tc.old, id); err != nil {
				t.Fatal(err)
			}
			evidence, err := stockmigration.Read(ctx, database, 1000)
			if err != nil {
				t.Fatal(err)
			}
			plan := stockmigration.Build(evidence)
			if len(plan.Exceptions) != 0 {
				t.Fatalf("migration exceptions: %+v", plan.Exceptions)
			}
			if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "upgrade-batch", id, plan.Digest, true); err != nil {
				t.Fatal(err)
			}
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
			if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id}); err != nil {
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

func seedStockRoot(t *testing.T, database *sql.DB, id, visibility, listing string) {
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
func TestStockMigrationRehearsalResumeRollbackAndHistoricalIntegrity(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedStockRoot(t, database, "one", "private", "published")
	seedStockRoot(t, database, "two", "space", "draft")
	original := func() string {
		var value string
		if err := database.QueryRow(`SELECT CONCAT(manifest_json,plugin_json,relations_json) FROM plugin_versions WHERE version_id='one-old'`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}()
	d, err := stockmigration.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	plan := stockmigration.Build(d)
	if len(plan.Exceptions) > 0 || len(plan.Units) != 2 {
		t.Fatalf("plan exceptions %+v units%d", plan.Exceptions, len(plan.Units))
	}
	if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "rehearsal", "one", plan.Digest, false); !errors.Is(err, stockmigration.ErrG4) {
		t.Fatalf("G4 gate: %v", err)
	}
	n, err := stockmigration.ApplyRoot(ctx, database, 1000, "rehearsal", "one", plan.Digest, true)
	if err != nil || n != 4 {
		t.Fatalf("apply one nodes%d err%v", n, err)
	}
	if n, err = stockmigration.ApplyRoot(ctx, database, 1000, "rehearsal", "one", plan.Digest, true); err != nil || n != 0 {
		t.Fatalf("repeat %d %v", n, err)
	}
	// Simulate an interrupted process: no in-memory cursor survives. Re-preview
	// remaining roots and continue the same batch, skipping its committed unit.
	d, err = stockmigration.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	resume := stockmigration.Build(d)
	if len(resume.Exceptions) > 0 || len(resume.Units) != 1 || resume.Units[0].RootID != "two" {
		t.Fatalf("resume: %+v", resume)
	}
	if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "rehearsal", "two", plan.Digest, true); !errors.Is(err, stockmigration.ErrConflict) {
		t.Fatalf("stale preview %v", err)
	}
	if n, err = stockmigration.ApplyRoot(ctx, database, 1000, "rehearsal", "two", resume.Digest, true); err != nil || n != 4 {
		t.Fatalf("apply two %d %v", n, err)
	}
	var roots, created, releases, indexes int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_stock_migration_roots WHERE batch_id='rehearsal'`).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT COUNT(*) FROM plugins WHERE is_embedded=0`).Scan(&created)
	database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases`).Scan(&releases)
	database.QueryRow(`SELECT COUNT(*) FROM plugin_version_relation_refs`).Scan(&indexes)
	if roots != 2 || created != 8 || releases != 5 || indexes != 6 {
		t.Fatalf("roots%d independent%d releases%d indexes%d", roots, created, releases, indexes)
	}
	var changed string
	database.QueryRow(`SELECT CONCAT(manifest_json,plugin_json,relations_json) FROM plugin_versions WHERE version_id='one-old'`).Scan(&changed)
	if changed != original {
		t.Fatal("history rewritten")
	}
	var fake int
	database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE version_id IN ('one-old','two-old')`).Scan(&fake)
	if fake != 0 {
		t.Fatal("fabricated legacy release")
	}
	if err = stockmigration.Rollback(ctx, database, "rehearsal"); err != nil {
		t.Fatal(err)
	}
	var pointer, listing string
	database.QueryRow(`SELECT current_version_id,listing_state FROM plugins WHERE plugin_id='one'`).Scan(&pointer, &listing)
	if pointer != "one-old" || listing != "published" {
		t.Fatalf("rollback pointer=%s listing=%s", pointer, listing)
	}
	var retained int
	database.QueryRow(`SELECT COUNT(*) FROM plugin_versions`).Scan(&retained)
	if retained != 8 {
		t.Fatalf("rollback deleted history: %d", retained)
	}
	if err = stockmigration.Rollback(ctx, database, "rehearsal"); err != nil {
		t.Fatalf("repeat rollback %v", err)
	}
	if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "rehearsal", "one", plan.Digest, true); !errors.Is(err, stockmigration.ErrConflict) {
		t.Fatalf("retired batch allowed apply %v", err)
	}
}
func TestStockMigrationBlocksExceptionsAndConcurrentRollbackEdits(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedStockRoot(t, database, "private", "private", "published")
	d, err := stockmigration.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	p := stockmigration.Build(d)
	if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "b", "private", p.Digest, true); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='private'`); err != nil {
		t.Fatal(err)
	}
	if err = stockmigration.Rollback(ctx, database, "b"); !errors.Is(err, stockmigration.ErrConflict) {
		t.Fatalf("concurrent delist overwritten: %v", err)
	}
}

func TestStockMigrationPinsAuditedIndependentMemberAfterResume(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedStockRoot(t, database, "team", "private", "published")
	if _, err := database.Exec(`UPDATE plugins SET is_embedded=0,current_version_id='member-old',current_version='1.0.0' WHERE plugin_id='team-expert'`); err != nil {
		t.Fatal(err)
	}
	seedVersionRow(t, database, "member-old", "team-expert", "1")
	if _, err := database.Exec(`UPDATE plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id SET v.manifest_hash=p.manifest_hash,v.plugin_hash=p.plugin_hash WHERE v.version_id='member-old'`); err != nil {
		t.Fatal(err)
	}
	d, err := stockmigration.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	plan := stockmigration.Build(d)
	if len(plan.Exceptions) > 0 || len(plan.Units) != 2 || plan.Units[0].RootID != "team-expert" || len(plan.Units[1].Dependencies) != 1 {
		t.Fatalf("independent member plan exceptions%+v units%+v", plan.Exceptions, plan.Units)
	}
	if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "independent", "team", plan.Digest, true); !errors.Is(err, stockmigration.ErrConflict) {
		t.Fatalf("team applied before released member: %v", err)
	}
	if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "independent", "team-expert", plan.Digest, true); err != nil {
		t.Fatal(err)
	}
	d, err = stockmigration.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	resume := stockmigration.Build(d)
	if len(resume.Exceptions) > 0 || len(resume.Units) != 1 || resume.Units[0].RootID != "team" {
		t.Fatalf("resume exceptions%+v units%d", resume.Exceptions, len(resume.Units))
	}
	if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "independent", "team", resume.Digest, true); err != nil {
		t.Fatal(err)
	}
	var expected, actual string
	if err = database.QueryRow(`SELECT published_version_id FROM plugins WHERE plugin_id='team-expert'`).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(`SELECT r.target_version_id FROM plugin_version_relation_refs r JOIN plugins p ON p.published_version_id=r.source_version_id WHERE p.plugin_id='team'`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if expected != actual {
		t.Fatal("team did not pin exact audited member version")
	}
	if err = stockmigration.Rollback(ctx, database, "independent"); err != nil {
		t.Fatal(err)
	}
}
func TestStockMigrationFailureRollsBackOnlyThisRootAndChildEditBlocksRollback(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedStockRoot(t, database, "root", "private", "published")
	d, err := stockmigration.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	plan := stockmigration.Build(d)
	if _, err = database.Exec(`CREATE TRIGGER reject_stock_root_update BEFORE UPDATE ON plugins FOR EACH ROW BEGIN IF OLD.plugin_id='root' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture failure'; END IF; END`); err != nil {
		t.Fatal(err)
	}
	if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "failure", "root", plan.Digest, true); !errors.Is(err, stockmigration.ErrWrite) {
		t.Fatalf("mid-unit failure: %v", err)
	}
	var count int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugins`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("partial children retained: count%d err%v", count, err)
	}
	if _, err = database.Exec(`DROP TRIGGER reject_stock_root_update`); err != nil {
		t.Fatal(err)
	}
	if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "failure", "root", plan.Digest, true); err != nil {
		t.Fatal(err)
	}
	child := plan.Units[0].Nodes[0].TargetID
	if _, err = database.Exec(`UPDATE plugins SET plugin_hash='independently-edited' WHERE plugin_id=?`, child); err != nil {
		t.Fatal(err)
	}
	if err = stockmigration.Rollback(ctx, database, "failure"); !errors.Is(err, stockmigration.ErrConflict) {
		t.Fatalf("child edit overwritten: %v", err)
	}
	var pointer string
	if err = database.QueryRow(`SELECT current_version_id FROM plugins WHERE plugin_id='root'`).Scan(&pointer); err != nil || pointer == "root-old" {
		t.Fatal("rollback partially restored root before child CAS failed")
	}
}

func TestStockMigrationVerifiesStoredAndLegacyArchiveBytesOnPreviewAndApply(t *testing.T) {
	for _, mode := range []string{"stored", "legacy-archive"} {
		t.Run(mode, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			seedStockRoot(t, database, "files", "private", "published")
			store := storage.NewLocal(t.TempDir(), "")
			put := func(key string, content []byte) {
				if err := store.PutObject(ctx, key, bytes.NewReader(content), int64(len(content)), ""); err != nil {
					t.Fatal(err)
				}
			}
			body := "---\nname: skill\ndescription: fixture\n---\n" + strings.Repeat("verified body\n", 8000)
			content := []byte(body)
			key := "plugins/space-a/attachments/SKILL.md"
			attachments := []any{}
			keys := map[string]string{}
			if mode == "stored" {
				sum := sha256.Sum256(content)
				attachments = append(attachments, map[string]any{"path": "SKILL.md", "content_type": "raw", "mime_type": "text/markdown", "raw_content": "---\nname: skill\ndescription: fixture\n---\nbody"}, map[string]any{"path": "references/large.md", "content_type": "storage", "storage_uri": key, "mime_type": "text/markdown", "content_size": len(content), "content_hash": "sha256:" + hex.EncodeToString(sum[:])})
				keys["references/large.md"] = key
				put(key, content)
			} else {
				key = "experts/original/skills/skill.zip"
				var data bytes.Buffer
				archive := zip.NewWriter(&data)
				for path, text := range map[string]string{"wrapped/SKILL.md": body, "wrapped/references/note.md": "real supporting bytes"} {
					w, err := archive.Create(path)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = w.Write([]byte(text)); err != nil {
						t.Fatal(err)
					}
				}
				if err := archive.Close(); err != nil {
					t.Fatal(err)
				}
				content = append([]byte(nil), data.Bytes()...)
				put(key, content)
				ref, _ := json.Marshal(map[string]string{"zip_object_key": key})
				attachments = append(attachments, map[string]any{"path": "SKILL.md", "content_type": "raw", "mime_type": "text/markdown", "raw_content": "stub"}, map[string]any{"path": "skill/ref.json", "content_type": "raw", "mime_type": "application/json", "raw_content": string(ref)})
			}
			pkg, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-package-2.0.json", "attachments": attachments})
			sidecar, _ := json.Marshal(keys)
			var name, manifest, tags string
			if err := database.QueryRow(`SELECT plugin_name,manifest_json,tags_json FROM plugins WHERE plugin_id='files-skill'`).Scan(&name, &manifest, &tags); err != nil {
				t.Fatal(err)
			}
			docs, err := pluginsvc.CanonicalizeMigratedDocuments(name, model.PluginTypeSkill, json.RawMessage(tags), json.RawMessage(manifest), pkg, "space-a")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = database.Exec(`UPDATE plugins SET plugin_json=?,attachment_keys_json=?,manifest_hash=?,plugin_hash=? WHERE plugin_id='files-skill'`, string(docs.Package), string(sidecar), docs.ManifestHash, docs.PluginHash); err != nil {
				t.Fatal(err)
			}
			resolver := func(ctx context.Context, p *model.Plugin) (map[string]string, error) {
				return pluginsvc.ResolveMigrationSkillFiles(ctx, store, p)
			}
			d, err := stockmigration.ReadWithResolver(ctx, database, 1000, resolver)
			if err != nil {
				t.Fatal(err)
			}
			plan := stockmigration.Build(d)
			if len(plan.Exceptions) > 0 {
				t.Fatalf("%s exceptions: %+v", mode, plan.Exceptions)
			}
			put(key, []byte("corrupt changed bytes"))
			if _, err = stockmigration.ApplyRootWithResolver(ctx, database, 1000, "files-batch", "files", plan.Digest, true, resolver); !errors.Is(err, stockmigration.ErrConflict) {
				t.Fatalf("changed source bytes accepted: %v", err)
			}
			put(key, content)
			if _, err = stockmigration.ApplyRootWithResolver(ctx, database, 1000, "files-batch", "files", plan.Digest, true, resolver); err != nil {
				t.Fatal(err)
			}
			skillID := plan.Units[0].Nodes[0].TargetID
			var converted, unchanged string
			if err = database.QueryRow(`SELECT plugin_json FROM plugins WHERE plugin_id=?`, skillID).Scan(&converted); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(converted, "verified body") || strings.Contains(converted, "skill/ref.json") {
				t.Fatal("actual source files were not materialized into new resource")
			}
			if err = database.QueryRow(`SELECT plugin_json FROM plugins WHERE plugin_id='files-skill'`).Scan(&unchanged); err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(unchanged, modeSourceMarker(mode)) {
				t.Fatal("original source pointer removed")
			}
			reader, err := store.GetObject(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			after, err := io.ReadAll(reader)
			reader.Close()
			if err != nil || !bytes.Equal(after, content) {
				t.Fatal("migration modified original file")
			}
		})
	}
}
func modeSourceMarker(mode string) string {
	if mode == "stored" {
		return "storage"
	}
	return "skill/ref.json"
}

func TestStockMigratedTeamReservesDraftAndPublishedNames(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	const id = "reserved-team"
	seedStockRoot(t, database, id, "private", "published")
	evidence, err := stockmigration.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	plan := stockmigration.Build(evidence)
	if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "team-names", id, plan.Digest, true); err != nil {
		t.Fatal(err)
	}
	var draft, published string
	if err = database.QueryRow(`SELECT name_key,published_name_key FROM plugins WHERE plugin_id=?`, id).Scan(&draft, &published); err != nil || draft != id || published != id {
		t.Fatalf("keys: %q %q %v", draft, published, err)
	}
	var count int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_name_reservations WHERE plugin_id=? AND name_key=?`, id, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("reservation: %d %v", count, err)
	}
	duplicate := uploadNameMutation("duplicate-team", " ＲＥＳＥＲＶＥＤ－ＴＥＡＭ ", "expert_team", "space-a")
	if _, err = pluginrepo.New(database).Create(ctx, pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}, duplicate); !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("normalized duplicate: %v", err)
	}
}

func TestStockMigrationRollbackRejectsConcurrentNameChanges(t *testing.T) {
	for _, field := range []string{"plugin_name", "name_key", "published_name_key", "name_scope", "legacy_receipt", "unchanged"} {
		t.Run(field, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			seedStockRoot(t, database, "root", "private", "published")
			d, err := stockmigration.Read(ctx, database, 1000)
			if err != nil {
				t.Fatal(err)
			}
			plan := stockmigration.Build(d)
			if _, err = stockmigration.ApplyRoot(ctx, database, 1000, "name-cas", "root", plan.Digest, true); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "legacy_receipt":
				_, err = database.Exec(`UPDATE plugin_stock_migration_roots SET after_json=JSON_REMOVE(after_json,'$.name_state') WHERE batch_id='name-cas'`)
			case "unchanged":
			default:
				// Constant field allowlist above; simulate a metadata/index write without
				// touching the content hashes or release pointers used by the old CAS.
				_, err = database.Exec(`UPDATE plugins SET ` + field + `='changed' WHERE plugin_id='root'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			var before string
			if err = database.QueryRow(`SELECT JSON_ARRAY(plugin_name,name_key,published_name_key,name_scope,current_version_id,published_version_id,manifest_hash,plugin_hash) FROM plugins WHERE plugin_id='root'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			err = stockmigration.Rollback(ctx, database, "name-cas")
			if field == "unchanged" {
				if err != nil {
					t.Fatalf("unchanged rollback: %v", err)
				}
				return
			}
			if !errors.Is(err, stockmigration.ErrConflict) {
				t.Fatalf("concurrent %s edit: %v", field, err)
			}
			var after, state string
			if err = database.QueryRow(`SELECT JSON_ARRAY(plugin_name,name_key,published_name_key,name_scope,current_version_id,published_version_id,manifest_hash,plugin_hash) FROM plugins WHERE plugin_id='root'`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatalf("failed rollback mutated root: before=%s after=%s", before, after)
			}
			if err = database.QueryRow(`SELECT state FROM plugin_stock_migration_batches WHERE batch_id='name-cas'`).Scan(&state); err != nil || state != "active" {
				t.Fatalf("batch unexpectedly retired: %s %v", state, err)
			}
		})
	}
}
