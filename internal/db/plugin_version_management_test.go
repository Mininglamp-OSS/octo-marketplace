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
