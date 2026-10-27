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

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
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

func TestManagedVersionReadsSeparateDraftAndReleasedAccess(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	owner := managedOwner()
	if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), true, owner.UID, owner.Name); err != nil {
		t.Fatal(err)
	}
	created, err := svc.Create(ctx, owner, managedWrite("1.0.0", "released instructions", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Plugin.ID
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id}); err != nil {
		t.Fatal(err)
	}
	state, err := repo.GetVersionState(ctx, tenantScope(), id)
	if err != nil || state.PublishedVersionID == nil {
		t.Fatalf("missing published version: %+v err=%v", state, err)
	}
	releasedID := *state.PublishedVersionID
	draft, err := svc.Update(ctx, owner, id, managedWrite("1.0.1", "private draft instructions", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	draftID := *draft.Plugin.CurrentVersionID
	for _, tc := range []struct {
		name    string
		caller  pluginsvc.Caller
		version string
		allowed bool
	}{
		{"owner draft", owner, draftID, true},
		{"member draft", pluginsvc.Caller{UID: "member", SpaceID: owner.SpaceID}, draftID, false},
		{"member release", pluginsvc.Caller{UID: "member", SpaceID: owner.SpaceID}, releasedID, true},
		{"cross space draft", pluginsvc.Caller{UID: owner.UID, SpaceID: "space-b"}, draftID, false},
		{"cross space release", pluginsvc.Caller{UID: owner.UID, SpaceID: "space-b"}, releasedID, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := svc.ManagedVersion(ctx, tc.caller, id, tc.version, false)
			if !tc.allowed {
				if !errors.Is(err, pluginsvc.ErrNotFound) {
					t.Fatalf("hidden version: %+v err=%v", v, err)
				}
				return
			}
			if err != nil || v.ID != tc.version {
				t.Fatalf("allowed version: %+v err=%v", v, err)
			}
		})
	}
}

func TestManagedPrivateReleasePreservesDraft(t *testing.T) {
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
	// Confirmed matrix: an admin save of a LISTED system resource publishes in the
	// same transaction, so the public read switches immediately.
	view, err := svc.Detail(ctx, reader, id, true)
	if err != nil || !strings.Contains(string(view.Plugin.Package), "admin draft") {
		t.Fatalf("admin save did not publish: %+v %v", view, err)
	}
	var publishedID, currentID string
	if err = database.QueryRow(`SELECT published_version_id,current_version_id FROM plugins WHERE plugin_id=?`, id).Scan(&publishedID, &currentID); err != nil {
		t.Fatal(err)
	}
	if publishedID != currentID || publishedID == v1 {
		t.Fatalf("published=%s current=%s, want the new snapshot published", publishedID, currentID)
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
	if _, err = svc.VersionFile(ctx, reader, tenant.Plugin.ID, vid, "old.bin", false); !errors.Is(err, pluginsvc.ErrIntegrity) {
		t.Fatalf("missing object served: %v", err)
	}
	if _, err = svc.ManagedVersion(ctx, reader, id, v1, false); err != nil {
		t.Fatalf("old public release inaccessible: %v", err)
	}
}

// A delisted system resource is off the market; an admin edit only advances the
// editable head and must not silently re-list it through a release.
func TestManagedAdminDelistedSystemUpdateOnlySaves(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	ctx := context.Background()
	admin := pluginsvc.Caller{UID: "market-admin", Name: "Admin", IsSystemAdmin: true}
	d, err := svc.AdminCreate(ctx, admin, managedWrite("1.0.0", "listed", model.PluginVisibilitySystem))
	if err != nil {
		t.Fatal(err)
	}
	id := d.Plugin.ID
	v1 := *d.Plugin.CurrentVersionID
	if _, err = database.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AdminUpdate(ctx, admin, id, managedWrite("2.0.0", "later edit", model.PluginVisibilitySystem)); err != nil {
		t.Fatal(err)
	}
	var publishedID, currentID string
	if err = database.QueryRow(`SELECT published_version_id,current_version_id FROM plugins WHERE plugin_id=?`, id).Scan(&publishedID, &currentID); err != nil {
		t.Fatal(err)
	}
	if publishedID != v1 || currentID == v1 {
		t.Fatalf("published=%s current=%s, want published kept at %s", publishedID, currentID, v1)
	}
	var releases int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=? AND version_label='2.0.0'`, id).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if releases != 0 {
		t.Fatalf("delisted admin edit minted %d releases", releases)
	}
}

// The publish-on-write exception is limited to system resources: an admin edit of
// a tenant resource preserves the tenant's published version.
func TestManagedAdminTenantUpdateOnlySaves(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	ctx := context.Background()
	owner := managedOwner()
	d, err := svc.Create(ctx, owner, managedWrite("1.0.0", "tenant v1", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	id := d.Plugin.ID
	v1 := *d.Plugin.CurrentVersionID
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id}); err != nil {
		t.Fatal(err)
	}
	admin := pluginsvc.Caller{UID: "market-admin", Name: "Admin", IsSystemAdmin: true}
	if _, err = svc.AdminUpdate(ctx, admin, id, managedWrite("2.0.0", "tenant v2", model.PluginVisibilityPrivate)); err != nil {
		t.Fatal(err)
	}
	var publishedID, currentID string
	if err = database.QueryRow(`SELECT published_version_id,current_version_id FROM plugins WHERE plugin_id=?`, id).Scan(&publishedID, &currentID); err != nil {
		t.Fatal(err)
	}
	if publishedID != v1 || currentID == v1 {
		t.Fatalf("published=%s current=%s, want published kept at %s", publishedID, currentID, v1)
	}
	view, err := svc.Detail(ctx, owner, id, true)
	if err != nil || !strings.Contains(string(view.Plugin.Package), "tenant v1") {
		t.Fatalf("tenant release drifted: %+v %v", view, err)
	}
}

// A first listing of a container must promote its bundled rows with it, or the
// listed expert/squad is uninstallable by anyone but its author.
func TestManagedFirstListingPromotesEmbeddedChildren(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	ctx := context.Background()
	owner := managedOwner()
	if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), false, "reviewer", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	d, err := svc.Create(ctx, owner, managedWrite("1.0.0", "container", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	id := d.Plugin.ID
	seed(t, database, seedPlugin{id: "child-1", typ: "skill", visibility: "private", listingState: "draft", embedded: true, owner: owner.UID})
	seedRelation(t, database, "rel-child", id, "child-1", "expert_skill")
	pub, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id})
	if err != nil {
		t.Fatal(err)
	}
	if pub.Review == nil {
		t.Fatal("missing pending review")
	}
	if _, err = svc.ApproveReview(ctx, managedReviewer(), pub.Review.ID); err != nil {
		t.Fatal(err)
	}
	var visibility, listing string
	if err = database.QueryRow(`SELECT visibility,listing_state FROM plugins WHERE plugin_id='child-1'`).Scan(&visibility, &listing); err != nil {
		t.Fatal(err)
	}
	if visibility != string(model.PluginVisibilitySpace) || listing != string(model.PluginListingStatePublished) {
		t.Fatalf("embedded child stayed %s/%s", visibility, listing)
	}
	var live int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_relations WHERE source_plugin_id=? AND target_plugin_id='child-1' AND deleted_at IS NULL`, id).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live relation count=%d, want the frozen edge", live)
	}
	// Another member of the Space must be able to read the promoted child, which is
	// what an install resolves before it counts declared relations.
	other := pluginsvc.Caller{UID: "teammate", SpaceID: "space-a"}
	if _, err = svc.Detail(ctx, other, "child-1", false); err != nil {
		t.Fatalf("teammate cannot read embedded child: %v", err)
	}
}

// Resources published during the V1 window have snapshots but no release row.
// Pressing publish adopts the approved snapshot as the formal baseline instead of
// dead-locking on "version_migration_required" + "label_taken".
func TestManagedLegacyPublishedBaselineAdoption(t *testing.T) {
	for _, visibility := range []model.PluginVisibility{model.PluginVisibilityPrivate, model.PluginVisibilitySpace} {
		t.Run(string(visibility), func(t *testing.T) {
			database := reviewDB(t)
			repo := pluginrepo.New(database)
			svc := pluginsvc.New(repo).WithVersionManagement(true)
			ctx := context.Background()
			owner := managedOwner()
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), false, "reviewer", "Reviewer"); err != nil {
				t.Fatal(err)
			}
			d, err := svc.Create(ctx, owner, managedWrite("1.0.0", "approved v1", visibility))
			if err != nil {
				t.Fatal(err)
			}
			id := d.Plugin.ID
			pub, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id})
			if err != nil {
				t.Fatal(err)
			}
			if pub.Review != nil {
				if _, err = svc.ApproveReview(ctx, managedReviewer(), pub.Review.ID); err != nil {
					t.Fatal(err)
				}
			}
			// Reconstruct the V1 state: immutable snapshot + listing, no release.
			if _, err = database.Exec(`UPDATE plugins SET published_version_id=NULL WHERE plugin_id=?`, id); err != nil {
				t.Fatal(err)
			}
			if _, err = database.Exec(`DELETE FROM plugin_version_releases WHERE plugin_id=?`, id); err != nil {
				t.Fatal(err)
			}
			adopted, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id})
			if err != nil {
				t.Fatalf("adoption failed: %v", err)
			}
			if adopted.Review != nil {
				t.Fatal("adoption opened a review instead of registering the baseline")
			}
			state, err := repo.GetVersionState(ctx, tenantScope(), id)
			if err != nil || state.PublishedVersionID == nil {
				t.Fatalf("missing adopted release: %+v %v", state, err)
			}
			// Editing is unblocked, and the published read still serves v1.
			if _, err = svc.Update(ctx, owner, id, managedWrite("2.0.0", "next draft", visibility)); err != nil {
				t.Fatalf("edit after adoption: %v", err)
			}
			view, err := svc.Detail(ctx, owner, id, true)
			if err != nil || !strings.Contains(string(view.Plugin.Package), "approved v1") {
				t.Fatalf("published read drifted: %+v %v", view, err)
			}
		})
	}
}

// A system resource listed during the V1 window is adopted through the existing
// admin publish entry: the approved snapshot becomes the formal release, after
// which an admin save uses the publish-on-write matrix.
func TestManagedAdminAdoptsLegacySystemBaseline(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	ctx := context.Background()
	admin := pluginsvc.Caller{UID: "market-admin", Name: "Admin", IsSystemAdmin: true}
	d, err := svc.AdminCreate(ctx, admin, managedWrite("1.0.0", "system v1", model.PluginVisibilitySystem))
	if err != nil {
		t.Fatal(err)
	}
	id := d.Plugin.ID
	v1 := *d.Plugin.CurrentVersionID
	if _, err = database.Exec(`UPDATE plugins SET published_version_id=NULL WHERE plugin_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`DELETE FROM plugin_version_releases WHERE plugin_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AdminPublishVersion(ctx, admin, id); err != nil {
		t.Fatalf("admin adoption: %v", err)
	}
	state, err := repo.GetVersionState(ctx, pluginrepo.Scope{Admin: true, CallerUID: admin.UID}, id)
	if err != nil || state.PublishedVersionID == nil || *state.PublishedVersionID != v1 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

// Adoption is evidence-gated: without an approved review over the same version
// and content, a Space resource must stay blocked for the M1/M3 inventory rather
// than have an arbitrary draft declared published.
func TestManagedLegacyAdoptionRequiresApprovalEvidence(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	ctx := context.Background()
	owner := managedOwner()
	if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), false, "reviewer", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	d, err := svc.Create(ctx, owner, managedWrite("1.0.0", "unverified", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	id := d.Plugin.ID
	pub, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id})
	if err != nil || pub.Review == nil {
		t.Fatalf("publish=%+v err=%v", pub, err)
	}
	if _, err = svc.ApproveReview(ctx, managedReviewer(), pub.Review.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE plugins SET published_version_id=NULL WHERE plugin_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`DELETE FROM plugin_version_releases WHERE plugin_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`DELETE FROM plugin_review_requests WHERE plugin_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id}); !errors.Is(err, pluginsvc.ErrVersionBaselineUnavailable) {
		t.Fatalf("evidence-free adoption err=%v", err)
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

func TestManagedHistoricalBinaryRetainedAfterReplacement(t *testing.T) {
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
	data, err := svc.VersionFile(ctx, owner, pid, v1, "assets/data.bin", false)
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

// installStubProvisioner records successful provision calls without Loop/Fleet.
type installStubProvisioner struct{ agents int }

func (p *installStubProvisioner) InstallCapability(context.Context, string, string, string, string, fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	p.agents++
	return &fleet.CapabilityInstallResult{ExpertID: "agent-1"}, nil
}

func managedSkillWrite(label, name string, visibility model.PluginVisibility) pluginsvc.WriteRequest {
	manifest, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-manifest-2.0.json", "plugin_name": name, "plugin_type": "skill", "name": strings.ToLower(name), "description": "fixture", "labels": []string{}, "examples": []string{}})
	pkg, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-package-2.0.json", "attachments": []any{map[string]any{"path": "SKILL.md", "content_type": "raw", "mime_type": "text/markdown", "raw_content": "# " + name}}})
	return pluginsvc.WriteRequest{Name: name, Type: model.PluginTypeSkill, Visibility: visibility, Version: label, Manifest: manifest, Package: pkg, Tags: json.RawMessage(`[]`)}
}

func publishManaged(t *testing.T, svc *pluginsvc.Service, owner pluginsvc.Caller, id string) {
	t.Helper()
	ctx := context.Background()
	pub, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id})
	if err != nil {
		t.Fatal(err)
	}
	if pub.Review != nil {
		if _, err = svc.ApproveReview(ctx, managedReviewer(), pub.Review.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagedInstallUsesReleasedRelations(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	provisioner := &installStubProvisioner{}
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithCapabilityInstaller(provisioner)
	ctx := context.Background()
	owner := managedOwner()
	if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), false, "reviewer", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	expert, err := svc.Create(ctx, owner, managedWrite("1.0.0", "v1", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, expert.Plugin.ID)
	teammate := pluginsvc.Caller{UID: "teammate", SpaceID: "space-a"}
	params := pluginsvc.InstallParams{WorkspaceID: "ws", RuntimeID: "rt"}
	if _, err = svc.Install(ctx, teammate, expert.Plugin.ID, params); err != nil {
		t.Fatalf("baseline install: %v", err)
	}
	skill, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "Visible Skill", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, skill.Plugin.ID)
	draft := managedWrite("2.0.0", "later draft", model.PluginVisibilitySpace)
	draft.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.Plugin.ID, Type: "expert_skill"}}
	if _, err = svc.Update(ctx, owner, expert.Plugin.ID, draft); err != nil {
		t.Fatal(err)
	}
	published, err := svc.Detail(ctx, teammate, expert.Plugin.ID, true)
	if err != nil || len(published.Relations) != 0 {
		t.Fatalf("released graph changed: %+v %v", published.Relations, err)
	}
	if _, err = svc.Install(ctx, teammate, expert.Plugin.ID, params); err != nil {
		t.Fatalf("unpublished draft broke the released install: %v", err)
	}
}

func TestManagedFirstApprovalKeepsLaterDraftRelations(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	ctx := context.Background()
	owner := managedOwner()
	if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), false, "reviewer", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	skillA, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "Skill A", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, skillA.Plugin.ID)
	skillB, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "Skill B", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, skillB.Plugin.ID)
	submitted := managedWrite("1.0.0", "submitted v1", model.PluginVisibilitySpace)
	submitted.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skillA.Plugin.ID, Type: "expert_skill"}}
	expert, err := svc.Create(ctx, owner, submitted)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: expert.Plugin.ID})
	if err != nil || pub.Review == nil {
		t.Fatalf("publish=%+v err=%v", pub, err)
	}
	later := managedWrite("2.0.0", "later draft", model.PluginVisibilitySpace)
	later.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skillB.Plugin.ID, Type: "expert_skill"}}
	updated, err := svc.Update(ctx, owner, expert.Plugin.ID, later)
	if err != nil {
		t.Fatal(err)
	}
	laterID := *updated.Plugin.CurrentVersionID
	if _, err = svc.ApproveReview(ctx, managedReviewer(), pub.Review.ID); err != nil {
		t.Fatal(err)
	}
	current, err := svc.CurrentDetail(ctx, owner, expert.Plugin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Plugin.CurrentVersionID == nil || *current.Plugin.CurrentVersionID != laterID {
		t.Fatalf("later draft pointer lost: %+v", current.Plugin.CurrentVersionID)
	}
	if len(current.Relations) != 1 || current.Relations[0].TargetPluginID != skillB.Plugin.ID {
		t.Fatalf("later draft relations overwritten: %+v", current.Relations)
	}
	published, err := svc.Detail(ctx, managedOwner(), expert.Plugin.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(published.Relations) != 1 || published.Relations[0].TargetPluginID != skillA.Plugin.ID {
		t.Fatalf("released topology changed: %+v", published.Relations)
	}
}

func TestManagedInstallStillRejectsHiddenReleasedDependency(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithCapabilityInstaller(&installStubProvisioner{})
	ctx := context.Background()
	owner := managedOwner()
	if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), false, "reviewer", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	hidden, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "Private Skill", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: hidden.Plugin.ID}); err != nil {
		t.Fatal(err)
	}
	req := managedWrite("1.0.0", "expert", model.PluginVisibilityPrivate)
	req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: hidden.Plugin.ID, Type: "expert_skill"}}
	expert, err := svc.Create(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, expert.Plugin.ID)
	// Model a pre-validation historical graph; current writes reject this audience.
	if _, err = database.Exec(`UPDATE plugins SET visibility='space' WHERE plugin_id=?`, expert.Plugin.ID); err != nil {
		t.Fatal(err)
	}
	teammate := pluginsvc.Caller{UID: "teammate", SpaceID: "space-a"}
	if _, err = svc.Install(ctx, teammate, expert.Plugin.ID, pluginsvc.InstallParams{WorkspaceID: "ws", RuntimeID: "rt"}); !errors.Is(err, pluginsvc.ErrDependencyHidden) {
		t.Fatalf("hidden released dependency install err=%v", err)
	}
	versions, _, err := svc.ListVersions(ctx, owner, expert.Plugin.ID, 10, 0)
	if err != nil || len(versions) == 0 {
		t.Fatalf("list released versions: %v %d", err, len(versions))
	}
	if _, err = svc.ManagedVersion(ctx, teammate, expert.Plugin.ID, versions[0].ID, false); !errors.Is(err, pluginsvc.ErrDependencyHidden) {
		t.Fatalf("hidden released dependency historical read err=%v", err)
	}
}

// Legacy released manifests may lack labels. Their tag projection must be empty,
// never the mutable tags of a later unpublished draft. SQL below constructs only
// this historical fixture shape; no production snapshot is rewritten.
func TestManagedMissingReleaseLabelsNeverExposeDraftTags(t *testing.T) {
	for _, labels := range []string{"missing", "null", "empty"} {
		t.Run(labels, func(t *testing.T) {
			database := reviewDB(t)
			repo := pluginrepo.New(database)
			svc := pluginsvc.New(repo).WithVersionManagement(true)
			ctx := context.Background()
			owner := managedOwner()
			d, err := svc.Create(ctx, owner, managedWrite("1.0.0", "released", model.PluginVisibilitySpace))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: d.Plugin.ID}); err != nil {
				t.Fatal(err)
			}
			var published string
			if err = database.QueryRow(`SELECT published_version_id FROM plugins WHERE plugin_id=?`, d.Plugin.ID).Scan(&published); err != nil {
				t.Fatal(err)
			}
			if labels == "missing" {
				_, err = database.Exec(`UPDATE plugin_versions SET manifest_json=JSON_REMOVE(manifest_json,'$.labels') WHERE version_id=?`, published)
			} else if labels == "null" {
				_, err = database.Exec(`UPDATE plugin_versions SET manifest_json=JSON_SET(manifest_json,'$.labels',CAST('null' AS JSON)) WHERE version_id=?`, published)
			}
			if err != nil {
				t.Fatal(err)
			}
			draft := managedWrite("2.0.0", "private draft tags", model.PluginVisibilitySpace)
			draft.Tags = json.RawMessage(`["secret-draft-tag"]`)
			draft.Manifest = json.RawMessage(strings.Replace(string(draft.Manifest), `"labels":[]`, `"labels":["secret-draft-tag"]`, 1))
			if _, err = svc.Update(ctx, owner, d.Plugin.ID, draft); err != nil {
				t.Fatal(err)
			}
			reader := pluginsvc.Caller{UID: "reader", SpaceID: owner.SpaceID}
			tags, err := svc.ListTags(ctx, reader, pluginsvc.TagListParams{Type: model.PluginTypeExpert, Limit: 20})
			if err != nil || len(tags) != 0 {
				t.Fatalf("release %s labels exposed draft tags: %+v err=%v", labels, tags, err)
			}
			items, _, err := svc.List(ctx, reader, pluginsvc.ListParams{Type: model.PluginTypeExpert, Limit: 20})
			if err != nil || len(items) != 1 || string(items[0].Tags) != "[]" {
				t.Fatalf("release %s list leaked draft tags: %+v %v", labels, items, err)
			}
			detail, err := svc.Detail(ctx, reader, d.Plugin.ID, true)
			if err != nil || string(detail.Plugin.Tags) != "[]" {
				t.Fatalf("release %s detail leaked tags: %+v %v", labels, detail, err)
			}
			filtered, _, err := svc.List(ctx, reader, pluginsvc.ListParams{Type: model.PluginTypeExpert, Tags: []string{"secret-draft-tag"}, Limit: 20})
			if err != nil || len(filtered) != 0 {
				t.Fatalf("release %s draft tag matched public filter: %+v %v", labels, filtered, err)
			}
			mine, err := svc.ListTags(ctx, owner, pluginsvc.TagListParams{Type: model.PluginTypeExpert, Mine: true, Limit: 20})
			if err != nil || len(mine) != 1 || mine[0].Name != "secret-draft-tag" {
				t.Fatalf("author's draft tags lost: %+v err=%v", mine, err)
			}
		})
	}
}
