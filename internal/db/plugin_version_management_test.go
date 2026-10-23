package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

func managedWrite(label, content string, visibility model.PluginVisibility) pluginsvc.WriteRequest {
	manifest := json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Versioned Expert","plugin_type":"expert","name":"versioned-expert","description":"Version management fixture.","labels":[],"examples":[]}`)
	raw, _ := json.Marshal(content)
	pkg := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"AGENTS.md","content_type":"raw","mime_type":"text/markdown","raw_content":` + string(raw) + `}]}`)
	return pluginsvc.WriteRequest{Name: "Versioned Expert", Type: model.PluginTypeExpert, Visibility: visibility, Version: label, Manifest: manifest, Package: pkg, Tags: json.RawMessage(`[]`)}
}
func managedOwner() pluginsvc.Caller {
	return pluginsvc.Caller{UID: "user-1", Name: "Owner", SpaceID: "space-a"}
}
func managedReviewer() pluginsvc.Caller {
	return pluginsvc.Caller{UID: "reviewer", Name: "Reviewer", SpaceID: "space-a", SpaceRole: pluginsvc.SpaceRoleAdmin}
}

func TestManagedPrivateReleasePreservesDraftAndRestore(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	ctx := context.Background()
	owner := managedOwner()
	d, err := svc.Create(ctx, owner, managedWrite("1.0.0", "original", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	id := d.Plugin.ID
	v1 := *d.Plugin.CurrentVersionID
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id}); err != nil {
		t.Fatal(err)
	}
	d, err = svc.Update(ctx, owner, id, managedWrite("2.0.0", "new draft", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	v2 := *d.Plugin.CurrentVersionID
	published, err := svc.Detail(ctx, owner, id, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(published.Plugin.Package), "original") || *published.Plugin.CurrentVersionID != v1 {
		t.Fatalf("published drift: %+v", published.Plugin)
	}
	draft, err := svc.CurrentDetail(ctx, owner, id)
	if err != nil || *draft.Plugin.CurrentVersionID != v2 {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
	old, err := svc.ManagedVersion(ctx, owner, id, v1, false)
	if err != nil || old.Release == nil || !old.IsPublished {
		t.Fatalf("old=%+v err=%v", old, err)
	}
	bytes, err := svc.VersionFile(ctx, owner, id, v1, "AGENTS.md", false)
	if err != nil || string(bytes) != "original" {
		t.Fatalf("file=%q err=%v", bytes, err)
	}
	restored, err := svc.RestoreVersion(ctx, owner, id, v1, "3.0.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if *restored.Plugin.CurrentVersionID == v1 || *restored.Plugin.CurrentVersionID == v2 {
		t.Fatal("restore reused immutable ID")
	}
	state, err := repo.GetVersionState(ctx, tenantScope(), id)
	if err != nil || *state.PublishedVersionID != v1 {
		t.Fatalf("restore published: %+v %v", state, err)
	}
	restoredVersion, err := svc.ManagedVersion(ctx, owner, id, *restored.Plugin.CurrentVersionID, false)
	if err != nil {
		t.Fatal(err)
	}
	if restoredVersion.RestoredFromVersionID == nil || *restoredVersion.RestoredFromVersionID != v1 || restoredVersion.Release != nil {
		t.Fatalf("restore provenance: %+v", restoredVersion)
	}
	if _, err = svc.RestoreVersion(ctx, owner, id, v1, "2.0.0", false); !errors.Is(err, pluginsvc.ErrVersionRegressed) {
		t.Fatalf("backwards restore err=%v", err)
	}
	outsider := pluginsvc.Caller{UID: "other", SpaceID: "space-b"}
	if _, err = svc.ManagedVersion(ctx, outsider, id, v1, false); !errors.Is(err, pluginsvc.ErrNotFound) {
		t.Fatalf("cross-space err=%v", err)
	}
	second, err := svc.Create(ctx, owner, managedWrite("1.0.0", "second", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ManagedVersion(ctx, owner, second.Plugin.ID, v1, false); !errors.Is(err, pluginsvc.ErrNotFound) {
		t.Fatalf("foreign resource version err=%v", err)
	}
}

func TestManagedFrozenApprovalKeepsLaterDraft(t *testing.T) {
	for _, auto := range []bool{false, true} {
		t.Run(fmt.Sprint(auto), func(t *testing.T) {
			database := reviewDB(t)
			repo := pluginrepo.New(database)
			svc := pluginsvc.New(repo).WithVersionManagement(true)
			ctx := context.Background()
			owner := managedOwner()
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), auto, "reviewer", "Reviewer"); err != nil {
				t.Fatal(err)
			}
			d, err := svc.Create(ctx, owner, managedWrite("1.0.0", "submitted v1", model.PluginVisibilitySpace))
			if err != nil {
				t.Fatal(err)
			}
			id := d.Plugin.ID
			pub, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id})
			if err != nil {
				t.Fatal(err)
			}
			if !auto {
				if pub.Review == nil {
					t.Fatal("missing pending review")
				}
				if _, err = svc.Update(ctx, owner, id, managedWrite("2.0.0", "later draft", model.PluginVisibilitySpace)); err != nil {
					t.Fatal(err)
				}
				if _, err = svc.ApproveReview(ctx, managedReviewer(), pub.Review.ID); err != nil {
					t.Fatal(err)
				}
			}
			published, err := svc.Detail(ctx, owner, id, true)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(published.Plugin.Package), "submitted v1") {
				t.Fatal("approval used new draft")
			}
			if !auto {
				draft, err := svc.CurrentDetail(ctx, owner, id)
				if err != nil || !strings.Contains(string(draft.Plugin.Package), "later draft") {
					t.Fatalf("approval overwrote draft: %+v %v", draft, err)
				}
			}
			if _, err = svc.Update(ctx, owner, id, managedWrite("3.0.0", "unapproved v3", model.PluginVisibilitySpace)); err != nil {
				t.Fatal(err)
			}
			reader := pluginsvc.Caller{UID: "other", SpaceID: "space-a"}
			list, n, err := svc.ListVersions(ctx, reader, id, 100, 0)
			if err != nil || n != 1 || len(list) != 1 || list[0].Release == nil {
				t.Fatalf("reader history leaked drafts: %+v %d %v", list, n, err)
			}
			draft, err := svc.CurrentDetail(ctx, owner, id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.ManagedVersion(ctx, reader, id, *draft.Plugin.CurrentVersionID, false); !errors.Is(err, pluginsvc.ErrNotFound) {
				t.Fatalf("reader draft err=%v", err)
			}
			if _, err = svc.CurrentDetail(ctx, reader, id); !errors.Is(err, pluginsvc.ErrNotFound) {
				t.Fatalf("reader editing err=%v", err)
			}
			items, _, err := svc.List(ctx, reader, pluginsvc.ListParams{Type: model.PluginTypeExpert, Limit: 20})
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || !strings.Contains(string(items[0].Manifest), "Versioned Expert") || *items[0].CurrentVersion != "1.0.0" {
				t.Fatalf("catalog not release: %+v", items)
			}
			upgrade, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id})
			if err != nil {
				t.Fatal(err)
			}
			if !auto {
				if upgrade.Review == nil {
					t.Fatal("missing upgrade review")
				}
				if err = svc.RejectReview(ctx, managedReviewer(), upgrade.Review.ID, "needs changes"); err != nil {
					t.Fatal(err)
				}
				unchanged, err := svc.Detail(ctx, reader, id, true)
				if err != nil || !strings.Contains(string(unchanged.Plugin.Package), "submitted v1") {
					t.Fatalf("rejection changed release: %v", err)
				}
			}
		})
	}
}

func TestManagedAdminAndHistoricalArtifactIntegrity(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	directory := t.TempDir()
	store := storage.NewLocal(directory, "http://localhost")
	svc := pluginsvc.New(repo, store).WithVersionManagement(true)
	ctx := context.Background()
	admin := pluginsvc.Caller{UID: "market-admin", Name: "Admin", IsSystemAdmin: true}
	d, err := svc.AdminCreate(ctx, admin, managedWrite("1.0.0", "public v1", model.PluginVisibilitySystem))
	if err != nil {
		t.Fatal(err)
	}
	id := d.Plugin.ID
	v1 := *d.Plugin.CurrentVersionID
	if _, err = svc.AdminUpdate(ctx, admin, id, managedWrite("2.0.0", "admin draft", model.PluginVisibilitySystem)); err != nil {
		t.Fatal(err)
	}
	reader := managedOwner()
	view, err := svc.Detail(ctx, reader, id, true)
	if err != nil || !strings.Contains(string(view.Plugin.Package), "public v1") {
		t.Fatalf("admin save published: %+v %v", view, err)
	}
	if _, err = svc.AdminPublishVersion(ctx, admin, id); err != nil {
		t.Fatal(err)
	}
	view, err = svc.Detail(ctx, reader, id, true)
	if err != nil || !strings.Contains(string(view.Plugin.Package), "admin draft") {
		t.Fatalf("admin publish failed: %+v %v", view, err)
	}
	// Corrupt a fixture's stored historical object. No current file may substitute.
	sum := sha256.Sum256([]byte("historical bytes"))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	key := "plugins/space-a/attachments/versions/" + hex.EncodeToString(sum[:])
	file := filepath.Join(directory, key)
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte("historical bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	pkg, _ := json.Marshal(map[string]any{"attachments": []any{map[string]any{"path": "old.bin", "content_type": "storage", "content_size": len("historical bytes"), "content_hash": digest}}})
	keys, _ := json.Marshal(map[string]string{"old.bin": key})
	// Tenant fixture so the own-Space object namespace is authoritative.
	tenant, err := svc.Create(ctx, reader, managedWrite("1.0.0", "fixture", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	vid := *tenant.Plugin.CurrentVersionID
	if _, err = database.Exec(`UPDATE plugin_versions SET plugin_json=?,attachment_keys_json=? WHERE plugin_id=? AND version_id=?`, string(pkg), string(keys), tenant.Plugin.ID, vid); err != nil {
		t.Fatal(err)
	}
	data, err := svc.VersionFile(ctx, reader, tenant.Plugin.ID, vid, "old.bin", false)
	if err != nil || string(data) != "historical bytes" {
		t.Fatalf("historical file=%q %v", data, err)
	}
	if err = os.WriteFile(file, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.VersionFile(ctx, reader, tenant.Plugin.ID, vid, "old.bin", false); !errors.Is(err, pluginsvc.ErrIntegrity) {
		t.Fatalf("corrupt object served: %v", err)
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RestoreVersion(ctx, reader, tenant.Plugin.ID, vid, "2.0.0", false); !errors.Is(err, pluginsvc.ErrIntegrity) {
		t.Fatalf("missing restore allowed: %v", err)
	}
	if _, err = svc.ManagedVersion(ctx, reader, id, v1, false); err != nil {
		t.Fatalf("old public release inaccessible: %v", err)
	}
}

func TestManagedPublicationUsesExpectedHeadAndRejectsLabelReuse(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	ctx := context.Background()
	owner := managedOwner()
	d, err := svc.Create(ctx, owner, managedWrite("1.0.0", "first", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	id := d.Plugin.ID
	v1 := *d.Plugin.CurrentVersionID
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Update(ctx, owner, id, managedWrite("1.0.0", "changed under same draft label", model.PluginVisibilityPrivate)); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.PublishManaged(ctx, tenantScope(), pluginrepo.PublishParams{PluginID: id, ExpectedVersionID: v1, OperatorID: owner.UID}); !errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatalf("stale head published: %v", err)
	}
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id}); !errors.Is(err, pluginsvc.ErrLabelTaken) {
		t.Fatalf("reused published label: %v", err)
	}
	state, err := repo.GetVersionState(ctx, tenantScope(), id)
	if err != nil || *state.PublishedVersionID != v1 {
		t.Fatalf("failed publication changed head: %+v %v", state, err)
	}
	// Current access, including deletion, is authoritative for every old release.
	if err = svc.Delete(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ManagedVersion(ctx, owner, id, v1, false); !errors.Is(err, pluginsvc.ErrNotFound) {
		t.Fatalf("deleted resource's version still visible: %v", err)
	}
	var count int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, id).Scan(&count); err != nil || count != 2 {
		t.Fatalf("delete removed historical storage: %d %v", count, err)
	}
}

func TestManagedCatalogSearchAndTagsUseRelease(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	ctx := context.Background()
	owner := managedOwner()
	req := managedWrite("1.0.0", "original", model.PluginVisibilitySpace)
	req.Tags = json.RawMessage(`["released-tag"]`)
	req.Manifest = json.RawMessage(strings.Replace(string(req.Manifest), `"labels":[]`, `"labels":["released-tag"]`, 1))
	d, err := svc.Create(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: d.Plugin.ID}); err != nil {
		t.Fatal(err)
	}
	req = managedWrite("2.0.0", "secret draft", model.PluginVisibilitySpace)
	req.Name = "Secret Draft Name"
	req.Tags = json.RawMessage(`["secret-draft-tag"]`)
	req.Manifest = json.RawMessage(strings.Replace(strings.Replace(string(req.Manifest), `"plugin_name":"Versioned Expert"`, `"plugin_name":"Secret Draft Name"`, 1), `"labels":[]`, `"labels":["secret-draft-tag"]`, 1))
	if _, err = svc.Update(ctx, owner, d.Plugin.ID, req); err != nil {
		t.Fatal(err)
	}
	reader := pluginsvc.Caller{UID: "reader", SpaceID: owner.SpaceID}
	view, err := svc.Detail(ctx, reader, d.Plugin.ID, false)
	if err != nil || view.Plugin.Name != "Versioned Expert" {
		t.Fatalf("draft name leaked: %+v %v", view, err)
	}
	items, n, err := svc.List(ctx, reader, pluginsvc.ListParams{Type: model.PluginTypeExpert, Keyword: "Secret Draft Name", Limit: 20})
	if err != nil || n != 0 || len(items) != 0 {
		t.Fatalf("draft search leaked: %v %d %v", items, n, err)
	}
	items, n, err = svc.List(ctx, reader, pluginsvc.ListParams{Type: model.PluginTypeExpert, Tags: []string{"released-tag"}, Limit: 20})
	if err != nil || n != 1 || len(items) != 1 {
		t.Fatalf("release filters broken: %v %d %v", items, n, err)
	}
	tags, err := svc.ListTags(ctx, reader, pluginsvc.TagListParams{Type: model.PluginTypeExpert, Limit: 20})
	if err != nil || len(tags) != 1 || tags[0].Name != "released-tag" {
		t.Fatalf("draft tag leaked: %+v %v", tags, err)
	}
}

func TestManagedRestoreRetainsHistoricalBinaryAfterReplacement(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	store := storage.NewLocal(t.TempDir(), "http://localhost")
	svc := pluginsvc.New(repo, store).WithVersionManagement(true)
	ctx := context.Background()
	owner := managedOwner()
	key := "plugins/space-a/attachments/upload.bin"
	write := func(label, bytes string) pluginsvc.WriteRequest {
		t.Helper()
		if err := store.PutObject(ctx, key, strings.NewReader(bytes), int64(len(bytes)), "application/octet-stream"); err != nil {
			t.Fatal(err)
		}
		req := managedWrite(label, "instructions", model.PluginVisibilityPrivate)
		var pkg map[string]any
		if err := json.Unmarshal(req.Package, &pkg); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(bytes))
		pkg["attachments"] = append(pkg["attachments"].([]any), map[string]any{"path": "assets/data.bin", "content_type": "storage", "mime_type": "application/octet-stream", "content_size": len(bytes), "content_hash": "sha256:" + hex.EncodeToString(sum[:]), "storage_uri": key})
		req.Package, _ = json.Marshal(pkg)
		return req
	}
	d, err := svc.Create(ctx, owner, write("1.0.0", "old bytes"))
	if err != nil {
		t.Fatal(err)
	}
	pid := d.Plugin.ID
	v1 := *d.Plugin.CurrentVersionID
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: pid}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Update(ctx, owner, pid, write("2.0.0", "replacement bytes")); err != nil {
		t.Fatal(err)
	}
	restored, err := svc.RestoreVersion(ctx, owner, pid, v1, "3.0.0", false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := svc.VersionFile(ctx, owner, pid, *restored.Plugin.CurrentVersionID, "assets/data.bin", false)
	if err != nil || string(data) != "old bytes" {
		t.Fatalf("restored latest bytes instead of history: %q %v", data, err)
	}
	data, err = svc.VersionFile(ctx, owner, pid, v1, "assets/data.bin", false)
	if err != nil || string(data) != "old bytes" {
		t.Fatalf("source history changed: %q %v", data, err)
	}
}

func TestManagedLegacyPublicationNeedsVerifiedBaseline(t *testing.T) {
	database := reviewDB(t)
	seed(t, database, seedPlugin{id: "legacy-managed", typ: "expert", visibility: "private", listingState: "published", currentVersion: "1.0.0"})
	svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true)
	ctx := context.Background()
	owner := managedOwner()
	if _, err := svc.Update(ctx, owner, "legacy-managed", managedWrite("2.0.0", "new unapproved draft", model.PluginVisibilityPrivate)); !errors.Is(err, pluginsvc.ErrVersionBaselineUnavailable) {
		t.Fatalf("legacy draft escaped compatibility guard: %v", err)
	}
	if _, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: "legacy-managed"}); !errors.Is(err, pluginsvc.ErrVersionBaselineUnavailable) {
		t.Fatalf("legacy save fabricated publication: %v", err)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id='legacy-managed'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed adoption changed history: %d %v", n, err)
	}
}
