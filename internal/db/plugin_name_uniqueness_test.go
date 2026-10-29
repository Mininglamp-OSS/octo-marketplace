package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func nameFixture(t *testing.T, db *sql.DB, repo *pluginrepo.Repo, id, name, typ, space, listing string) {
	t.Helper()
	p := model.Plugin{
		ID: id, Name: name, Type: model.PluginType(typ),
		Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingState(listing),
		Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`),
		ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1,
		CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner",
	}
	_, err := repo.Create(context.Background(), pluginrepo.Scope{CallerUID: "owner", SpaceID: space}, pluginrepo.Mutation{
		Plugin: p, OperatorID: "owner", OperatorName: "Owner",
	})
	if err != nil {
		t.Fatalf("create %s/%s: %v", id, name, err)
	}
}

func TestPluginNameUniquenessPerSpaceType(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	nameFixture(t, db, repo, "expert-a", "Alpha", "expert", "space-a", "draft")
	// Same display name is allowed across types and Spaces.
	nameFixture(t, db, repo, "skill-a", "Alpha", "skill", "space-a", "draft")
	nameFixture(t, db, repo, "expert-b", "Alpha", "expert", "space-b", "draft")
	// Same type + Space + normalized name is rejected.
	err := func() error {
		_, e := repo.Create(context.Background(), pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}, pluginrepo.Mutation{
			Plugin: model.Plugin{ID: "expert-dup", Name: " alpha ", Type: model.PluginTypeExpert, Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1, CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner"}, OperatorID: "owner", OperatorName: "Owner",
		})
		return e
	}()
	if !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("duplicate name error = %v, want ErrNameTaken", err)
	}
}

func TestPluginNameUniquenessFullWidthAndCase(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	nameFixture(t, db, repo, "skill-beta", "Beta", "skill", "space-a", "draft")
	err := func() error {
		_, e := repo.Create(context.Background(), pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}, pluginrepo.Mutation{
			Plugin: model.Plugin{ID: "skill-beta-fullwidth", Name: "ＢＥＴＡ", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1, CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner"}, OperatorID: "owner", OperatorName: "Owner",
		})
		return e
	}()
	if !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("full-width duplicate error = %v, want ErrNameTaken", err)
	}
}

func TestPluginNameUniquenessSoftDeleteAndDelist(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	nameFixture(t, db, repo, "skill-gamma", "Gamma", "skill", "space-a", "draft")
	// Delisted is not deleted and must keep occupying the name.
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill-gamma'`); err != nil {
		t.Fatal(err)
	}
	err := func() error {
		_, e := repo.Create(context.Background(), pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}, pluginrepo.Mutation{
			Plugin: model.Plugin{ID: "skill-gamma-dupe", Name: "gamma", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1, CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner"}, OperatorID: "owner", OperatorName: "Owner",
		})
		return e
	}()
	if !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("delisted duplicate error = %v, want ErrNameTaken", err)
	}
	if _, err := db.Exec(`UPDATE plugins SET deleted_at=NOW(3) WHERE plugin_id='skill-gamma'`); err != nil {
		t.Fatal(err)
	}
	nameFixture(t, db, repo, "skill-gamma-new", "Gamma", "skill", "space-a", "draft")
}

func TestPluginNameRenameUpdatesKey(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	nameFixture(t, db, repo, "skill-echo", "Echo", "skill", "space-a", "draft")
	ctx := context.Background()
	scope := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
	got, err := repo.Get(ctx, scope, "skill-echo")
	if err != nil {
		t.Fatal(err)
	}
	got.Name = "Zeta"
	if _, err := repo.Update(ctx, scope, pluginrepo.Mutation{Plugin: *got, OperatorID: "owner"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	// The new name is now occupied.
	if _, err := repo.Create(ctx, scope, pluginrepo.Mutation{
		Plugin: model.Plugin{ID: "skill-zeta-dupe", Name: " zeta ", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1, CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner"}, OperatorID: "owner", OperatorName: "Owner",
	}); !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("renamed duplicate error = %v, want ErrNameTaken", err)
	}
	// The old name is free after the rename.
	nameFixture(t, db, repo, "skill-echo-2", "Echo", "skill", "space-a", "draft")
}

func TestPluginNamePublishedReservationSurvivesDraftRename(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	nameFixture(t, db, repo, "skill-published-alpha", "Alpha", "skill", "space-a", "published")
	// Simulate version management: published display name stays Alpha while the
	// editable draft name moves to Beta.
	if _, err := db.Exec(`UPDATE plugins SET published_name_key='alpha',name_key='beta' WHERE plugin_id='skill-published-alpha'`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
	// Another draft Alpha must still collide with the published display name.
	err := func() error {
		_, e := repo.Create(ctx, scope, pluginrepo.Mutation{
			Plugin: model.Plugin{ID: "skill-new-alpha", Name: "Alpha", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1, CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner"}, OperatorID: "owner", OperatorName: "Owner",
		})
		return e
	}()
	if !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("published Alpha was released by draft rename: %v", err)
	}
	// The published row also reserves its editable draft name Beta, so another
	// active draft Beta must not be created.
	err = func() error {
		_, e := repo.Create(ctx, scope, pluginrepo.Mutation{
			Plugin: model.Plugin{ID: "skill-new-beta", Name: "Beta", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1, CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner"}, OperatorID: "owner", OperatorName: "Owner",
		})
		return e
	}()
	if !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("draft Beta reservation was released: %v", err)
	}
}

func TestPluginNameBinaryKeyKeepsAccentsDistinct(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	nameFixture(t, db, repo, "skill-cafe", "Cafe", "skill", "space-a", "draft")
	// Go NFKC keeps Café distinct from Cafe; the binary indexed key must agree.
	nameFixture(t, db, repo, "skill-cafe-accent", "Café", "skill", "space-a", "draft")
}

func TestPluginNamePublishedReservationThroughService(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	ctx := context.Background()
	owner := managedOwner()
	write := func(name, version string) pluginsvc.WriteRequest {
		manifest, _ := json.Marshal(map[string]any{
			"$schema": "cowork-plugin-manifest-2.0.json", "plugin_name": name,
			"plugin_type": "skill", "name": "name-test", "description": "name test",
			"labels": []any{}, "examples": []any{},
		})
		pkg := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# test"}]}`)
		return pluginsvc.WriteRequest{Name: name, Type: model.PluginTypeSkill, Visibility: model.PluginVisibilityPrivate, Version: version, Manifest: manifest, Package: pkg, Tags: json.RawMessage(`[]`)}
	}
	created, err := svc.Create(ctx, owner, write("Alpha", "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: created.Plugin.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, owner, created.Plugin.ID, write("Beta", "2.0.0")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, owner, write("Alpha", "1.0.0")); !errors.Is(err, pluginsvc.ErrNameTaken) {
		t.Fatalf("published Alpha was released by draft rename: %v", err)
	}
}

func TestPluginNameApprovalConflictMapsToNameTaken(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	ctx := context.Background()
	seed(t, db, seedPlugin{id: "pending-name", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	if _, err := db.Exec(`UPDATE plugins SET plugin_name='Beta',name_key='beta',name_scope='space-a' WHERE plugin_id='pending-name'`); err != nil {
		t.Fatal(err)
	}
	// The frozen submission is Alpha; a different active row already owns Alpha.
	seed(t, db, seedPlugin{id: "active-alpha", visibility: "space", listingState: "draft", currentVersion: "1.0.0"})
	if _, err := db.Exec(`UPDATE plugins SET plugin_name='Alpha',name_key='alpha',name_scope='space-a' WHERE plugin_id='active-alpha'`); err != nil {
		t.Fatal(err)
	}
	req := newRequest("pending-name", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Alpha"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	_, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam"})
	if !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("approval name conflict = %v, want ErrNameTaken", err)
	}
	// The service must map the repository sentinel, not leak a raw MySQL 1062.
	svc := pluginsvc.New(repo)
	if _, err := svc.ApproveReview(ctx, managedReviewer(), req.ID); !errors.Is(err, pluginsvc.ErrNameTaken) {
		t.Fatalf("service approval name conflict = %v, want ErrNameTaken", err)
	}
}

func TestPluginNameRepositoryRejectsBlankWrites(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	ctx := context.Background()
	scope := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
	blank := model.Plugin{ID: "blank-create", Name: "   ", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1, CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner"}
	if _, err := repo.Create(ctx, scope, pluginrepo.Mutation{Plugin: blank, OperatorID: "owner", OperatorName: "Owner"}); !errors.Is(err, pluginrepo.ErrInvalidName) {
		t.Fatalf("blank create error = %v, want ErrInvalidName", err)
	}
	nameFixture(t, db, repo, "valid-name", "Valid", "skill", "space-a", "draft")
	got, err := repo.Get(ctx, scope, "valid-name")
	if err != nil {
		t.Fatal(err)
	}
	got.Name = " "
	if _, err := repo.Update(ctx, scope, pluginrepo.Mutation{Plugin: *got, OperatorID: "owner"}); !errors.Is(err, pluginrepo.ErrInvalidName) {
		t.Fatalf("blank update error = %v, want ErrInvalidName", err)
	}
}

// A historical saved version is not a formal publication. The legacy backfill
// must leave published_name_key NULL; otherwise renaming the draft frees the
// draft but leaves a phantom published reservation.
func TestSavedVersionDoesNotReservePublishedName(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	ctx := context.Background()
	scope := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
	nameFixture(t, db, repo, "saved-version", "Alpha", "skill", "space-a", "draft")
	if _, err := db.Exec(`UPDATE plugins SET name_key='alpha',name_scope='space-a',published_name_key=NULL WHERE plugin_id='saved-version'`); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, scope, "saved-version")
	if err != nil {
		t.Fatal(err)
	}
	got.Name = "Beta"
	if _, err := repo.Update(ctx, scope, pluginrepo.Mutation{Plugin: *got, OperatorID: "owner"}); err != nil {
		t.Fatal(err)
	}
	nameFixture(t, db, repo, "saved-version-alpha", "Alpha", "skill", "space-a", "draft")
}

func TestPluginNameLegacyApprovalConflictRollsBack(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	ctx := context.Background()
	seed(t, db, seedPlugin{id: "pending-name", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	if _, err := db.Exec(`UPDATE plugins SET plugin_name='Beta',name_key='beta',name_scope='space-a' WHERE plugin_id='pending-name'`); err != nil {
		t.Fatal(err)
	}
	// The frozen submission is Alpha; a different active row already owns Alpha.
	seed(t, db, seedPlugin{id: "active-alpha", visibility: "space", listingState: "draft", currentVersion: "1.0.0"})
	if _, err := db.Exec(`UPDATE plugins SET plugin_name='Alpha',name_key=NULL,published_name_key=NULL,name_scope='space-a' WHERE plugin_id='active-alpha'`); err != nil {
		t.Fatal(err)
	}
	req := newRequest("pending-name", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Alpha"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	before := releaseNameWriteEvidence(t, db, "pending-name")
	_, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam"})
	if !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("approval name conflict = %v, want ErrNameTaken", err)
	}
	// The service must map the repository sentinel, not leak a raw MySQL 1062.
	svc := pluginsvc.New(repo)
	if _, err := svc.ApproveReview(ctx, managedReviewer(), req.ID); !errors.Is(err, pluginsvc.ErrNameTaken) {
		t.Fatalf("service approval name conflict = %v, want ErrNameTaken", err)
	}

	if after := releaseNameWriteEvidence(t, db, "pending-name"); after != before {
		t.Fatalf("conflicting approval changed state: %s -> %s", before, after)
	}
}

// Snapshot state and append-only evidence together: a failed name check must
// roll back the whole approval/release transaction, not only its row update.
func releaseNameWriteEvidence(t *testing.T, database *sql.DB, id string) string {
	t.Helper()
	var result string
	err := database.QueryRow(`SELECT CONCAT_WS('|',plugin_name,listing_state,visibility,COALESCE(published_version_id,''),COALESCE(published_name_key,''),(SELECT COUNT(*) FROM plugin_version_releases r WHERE r.plugin_id=p.plugin_id),(SELECT COUNT(*) FROM plugin_audit_logs a WHERE a.plugin_id=p.plugin_id),(SELECT COALESCE(GROUP_CONCAT(status ORDER BY review_id),'') FROM plugin_review_requests q WHERE q.plugin_id=p.plugin_id)) FROM plugins p WHERE plugin_id=?`, id).Scan(&result)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestManagedReleaseRejectsHistoricalNameCollision(t *testing.T) {
	for _, path := range []string{"publish", "approval", "adoption"} {
		t.Run(path, func(t *testing.T) {
			database := reviewDB(t)
			repo := pluginrepo.New(database)
			svc := pluginsvc.New(repo).WithVersionManagement(true)
			ctx, owner := context.Background(), managedOwner()
			visibility := model.PluginVisibilityPrivate
			if path == "approval" {
				visibility = model.PluginVisibilitySpace
			}
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), false, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			created, err := svc.Create(ctx, owner, managedWrite("1.0.0", "name regression", visibility))
			if err != nil {
				t.Fatal(err)
			}
			id := created.Plugin.ID
			var reviewID string
			if path != "publish" {
				result, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id})
				if err != nil {
					t.Fatal(err)
				}
				if result.Review != nil {
					reviewID = result.Review.ID
				}
			}
			if path == "adoption" {
				if _, err := database.Exec(`UPDATE plugins SET published_version_id=NULL,published_name_key=NULL WHERE plugin_id=?`, id); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`DELETE FROM plugin_version_releases WHERE plugin_id=?`, id); err != nil {
					t.Fatal(err)
				}
			}
			seed(t, database, seedPlugin{id: "legacy-name-conflict", typ: "expert", visibility: "space", listingState: "published"})
			if _, err := database.Exec(`UPDATE plugins SET plugin_name='Versioned Expert',name_key=NULL,published_name_key=NULL WHERE plugin_id='legacy-name-conflict'`); err != nil {
				t.Fatal(err)
			}
			before := releaseNameWriteEvidence(t, database, id)
			if path == "approval" {
				_, err = svc.ApproveReview(ctx, managedReviewer(), reviewID)
			} else {
				_, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id})
			}
			if !errors.Is(err, pluginsvc.ErrNameTaken) {
				t.Fatalf("%s collision: %v", path, err)
			}
			if after := releaseNameWriteEvidence(t, database, id); after != before {
				t.Fatalf("failed %s wrote state: %s -> %s", path, before, after)
			}
			if _, err := database.Exec(`UPDATE plugins SET deleted_at=NOW(3) WHERE plugin_id='legacy-name-conflict'`); err != nil {
				t.Fatal(err)
			}
			if path == "approval" {
				_, err = svc.ApproveReview(ctx, managedReviewer(), reviewID)
			} else {
				_, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id})
			}
			if err != nil {
				t.Fatalf("retry after removing collision: %v", err)
			}
		})
	}
}
