package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// TestSpaceIntentDraftIsInvisibleToTheSpaceButVisibleToItsOwner is the security
// test for the listing_state read predicate, and it is table-driven on purpose.
//
// Splitting visibility (declared intent) from listing_state (actually listed)
// means `visibility='space'` can now sit on a row nobody has approved. Every read
// path that used to trust visibility alone would hand that draft to the whole
// Space. visibilitySQL is shared by nine call sites, so the fix is one constant —
// but a TENTH call site added later without it is exactly the regression this
// table is here to catch. Add the new surface as a case; do not assert only on
// the constant's text.
//
// Each probe returns whether the caller can reach the draft AT ALL through that
// surface. The owner must reach it everywhere (they have to edit and publish it);
// a same-Space colleague must reach it nowhere.
func TestSpaceIntentDraftIsInvisibleToTheSpaceButVisibleToItsOwner(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()

	// The subject: org-INTENT, not yet approved.
	seed(t, database, seedPlugin{
		id: "draft-1", visibility: "space", listingState: "draft",
		currentVersionID: "ver-draft", currentVersion: "0.9.0",
	})
	// A published sibling owned by the colleague, used as the relation SOURCE so
	// the relation and version probes have something legitimate to read from.
	seed(t, database, seedPlugin{
		id: "listed-1", typ: "expert", visibility: "space", listingState: "published",
		owner: "user-2", currentVersionID: "ver-listed", currentVersion: "1.0.0",
	})
	seedVersionRow(t, database, "ver-draft", "draft-1", "1")
	seedVersionRow(t, database, "ver-listed", "listed-1", "1")

	owner := pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a"}
	colleague := pluginrepo.Scope{CallerUID: "user-2", SpaceID: "space-a"}

	probes := []struct {
		surface string
		// ownerMustReach is false for the surfaces where the owner direction is not
		// meaningful or is deliberately inverted: the market GRID and the two facets
		// that mirror it (ListTags on the grid path, ListPlacementCategories) exclude
		// the owner's own unpublished rows by design — a chip or a count that
		// advertises a row the grid will not return is its own defect, covered by
		// TestGridFacetsAgreeWithTheGrid — and the adopt probe's source plugin belongs
		// to the colleague, so the owner could never edit it in the first place.
		ownerMustReach bool
		reaches        func(*testing.T, pluginrepo.Scope) bool
	}{
		{"Get", true, func(t *testing.T, sc pluginrepo.Scope) bool {
			_, err := repo.Get(ctx, sc, "draft-1")
			if err != nil && !errors.Is(err, pluginrepo.ErrNotFound) {
				t.Fatalf("Get: %v", err)
			}
			return err == nil
		}},
		{"List", false, func(t *testing.T, sc pluginrepo.Scope) bool {
			items, _, err := repo.List(ctx, sc, pluginrepo.ListFilter{PlacementCode: "default"})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			return containsPlugin(items, "draft-1")
		}},
		{"List(mine)", true, func(t *testing.T, sc pluginrepo.Scope) bool {
			items, _, err := repo.List(ctx, sc, pluginrepo.ListFilter{PlacementCode: "default", Mine: true})
			if err != nil {
				t.Fatalf("List(mine): %v", err)
			}
			return containsPlugin(items, "draft-1")
		}},
		{"GetWithRelations", true, func(t *testing.T, sc pluginrepo.Scope) bool {
			_, _, err := repo.GetWithRelations(ctx, sc, "draft-1")
			if err != nil && !errors.Is(err, pluginrepo.ErrNotFound) {
				t.Fatalf("GetWithRelations: %v", err)
			}
			return err == nil
		}},
		{"ListTags", false, func(t *testing.T, sc pluginrepo.Scope) bool {
			tags, err := repo.ListTags(ctx, sc, pluginrepo.TagListFilter{PlacementCode: "default", Type: model.PluginTypeSkill})
			if err != nil {
				t.Fatalf("ListTags: %v", err)
			}
			for _, tag := range tags {
				if tag.Name == "draft-only-tag" {
					return true
				}
			}
			return false
		}},
		{"ListTags(mine)", true, func(t *testing.T, sc pluginrepo.Scope) bool {
			tags, err := repo.ListTags(ctx, sc, pluginrepo.TagListFilter{PlacementCode: "default", Type: model.PluginTypeSkill, Mine: true})
			if err != nil {
				t.Fatalf("ListTags(mine): %v", err)
			}
			for _, tag := range tags {
				if tag.Name == "draft-only-tag" {
					return true
				}
			}
			return false
		}},
		{"ListVersions", true, func(t *testing.T, sc pluginrepo.Scope) bool {
			// ListVersions gates on Get first, so an unreachable plugin surfaces as
			// ErrNotFound rather than an empty page.
			_, total, err := repo.ListVersions(ctx, sc, "draft-1", 20, 0)
			if err != nil && !errors.Is(err, pluginrepo.ErrNotFound) {
				t.Fatalf("ListVersions: %v", err)
			}
			return err == nil && total > 0
		}},
		{"ListPlacementCategories", false, func(t *testing.T, sc pluginrepo.Scope) bool {
			cats, err := repo.ListPlacementCategories(ctx, sc, "default", model.PluginTypeSkill)
			if err != nil {
				t.Fatalf("ListPlacementCategories: %v", err)
			}
			for _, c := range cats {
				if c.ID == "cat-draft" {
					// The category itself is placement configuration and is always
					// returned; only the COUNT is scoped, and a non-zero count
					// confirms the draft's existence.
					return c.PluginCount > 0
				}
			}
			return false
		}},
		{"lockRelationTargets", false, func(t *testing.T, sc pluginrepo.Scope) bool {
			// The write path: can a colleague ADOPT the unpublished draft as a
			// relation target of their own published expert? ErrInvalidRelation is
			// what an unreachable target produces.
			err := adoptAsRelationTarget(ctx, repo, sc, "listed-1", "draft-1")
			if err != nil && !errors.Is(err, pluginrepo.ErrInvalidRelation) && !errors.Is(err, pluginrepo.ErrNotFound) {
				t.Fatalf("Update(adopt): %v", err)
			}
			return err == nil
		}},
	}

	// The draft needs a tag and a category the probes can look for.
	if _, err := database.Exec(
		`UPDATE plugins SET tags_json=JSON_ARRAY('draft-only-tag'), category_id='cat-draft' WHERE plugin_id='draft-1'`,
	); err != nil {
		t.Fatalf("tag/categorize draft: %v", err)
	}
	seedCategory(t, database, "cat-draft", `["skill"]`)
	// ListPlacementCategories returns categories REGISTERED at the placement, so
	// the category has to be registered before its count can be probed at all.
	if _, err := database.Exec(`INSERT INTO plugin_category_placements
		(placement_id, placement_code, plugin_type, category_id, visible, sort_order, created_at, updated_at)
		VALUES ('cp-draft', 'default', 'skill', 'cat-draft', 1, 0, NOW(3), NOW(3))`,
	); err != nil {
		t.Fatalf("register category at placement: %v", err)
	}
	if _, err := database.Exec(`UPDATE plugin_placements SET category_id='cat-draft' WHERE plugin_id='draft-1'`); err != nil {
		t.Fatalf("place draft in category: %v", err)
	}

	for _, probe := range probes {
		t.Run(probe.surface, func(t *testing.T) {
			if probe.ownerMustReach && !probe.reaches(t, owner) {
				// Not a leak, but just as broken: an author who cannot read their
				// own draft cannot edit or publish it. The owner disjunct of
				// visibilitySQL is deliberately NOT gated on listing_state.
				t.Errorf("%s: the owner cannot reach their own draft", probe.surface)
			}
			if probe.reaches(t, colleague) {
				t.Errorf("%s: LEAK — an unapproved space-intent draft reached another member of the Space", probe.surface)
			}
		})
	}
}

// TestCrossSpaceDraftLeakage pins the nesting of the predicate: the owner
// disjunct lives INSIDE the `space_id = ?` conjunct, so matching owner_uid is not
// on its own enough. Flattening the parentheses while "simplifying" would let an
// author read their row from a Space it does not belong to, and is an easy edit to
// make by accident.
func TestCrossSpaceDraftLeakage(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "draft-1", visibility: "space", listingState: "draft"})

	for _, tc := range []struct {
		name  string
		scope pluginrepo.Scope
	}{
		{"outsider in another Space", pluginrepo.Scope{CallerUID: "user-9", SpaceID: "space-b"}},
		{"the owner acting in another Space", pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-b"}},
		{"a colleague in the owning Space", pluginrepo.Scope{CallerUID: "user-2", SpaceID: "space-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := repo.Get(ctx, tc.scope, "draft-1"); !errors.Is(err, pluginrepo.ErrNotFound) {
				t.Errorf("Get = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestDelistedPluginLeavesTheMarketButStaysReadableByItsOwner covers the other
// unpublished state. A delisted row must behave like a draft for everyone else
// while staying reachable to its owner, who is expected to edit and republish it.
func TestDelistedPluginLeavesTheMarketButStaysReadableByItsOwner(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "gone-1", visibility: "space", listingState: "delisted", currentVersion: "1.0.0"})

	owner := pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a"}
	colleague := pluginrepo.Scope{CallerUID: "user-2", SpaceID: "space-a"}

	if _, err := repo.Get(ctx, owner, "gone-1"); err != nil {
		t.Fatalf("the owner cannot read their delisted plugin: %v", err)
	}
	if _, err := repo.Get(ctx, colleague, "gone-1"); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("Get as colleague = %v, want ErrNotFound", err)
	}
	items, _, err := repo.List(ctx, owner, pluginrepo.ListFilter{PlacementCode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if containsPlugin(items, "gone-1") {
		t.Error("a delisted plugin is still in the market grid")
	}
	mine, _, err := repo.List(ctx, owner, pluginrepo.ListFilter{PlacementCode: "default", Mine: true})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPlugin(mine, "gone-1") {
		t.Error("a delisted plugin vanished from 我的发布; its owner can no longer republish it")
	}
}

// TestHasPendingReviewIsScopedThroughThePlugin covers the pre-check the write
// path uses to refuse a visibility change mid-review. It is scoped through the
// plugin rather than keyed on plugin_id alone, so it cannot be used to probe
// whether an arbitrary plugin has a review open.
func TestHasPendingReviewIsScopedThroughThePlugin(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})

	owner := pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a"}

	if pending, err := repo.HasPendingReview(ctx, owner, "plugin-1"); err != nil || pending {
		t.Fatalf("HasPendingReview before submit = %v, %v; want false", pending, err)
	}

	req := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, owner, req, snapshotOf(`{"plugin_name":"Frozen"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}

	if pending, err := repo.HasPendingReview(ctx, owner, "plugin-1"); err != nil || !pending {
		t.Fatalf("HasPendingReview after submit = %v, %v; want true", pending, err)
	}
	for _, tc := range []struct {
		name  string
		scope pluginrepo.Scope
	}{
		{"a colleague in the same Space", pluginrepo.Scope{CallerUID: "user-2", SpaceID: "space-a"}},
		{"the owner acting in another Space", pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pending, err := repo.HasPendingReview(ctx, tc.scope, "plugin-1")
			if err != nil {
				t.Fatal(err)
			}
			if pending {
				t.Error("a non-owner learned that a review is open on someone else's plugin")
			}
		})
	}

	// A settled request releases it: cancel is how an author gets back to a
	// state where they can change visibility again.
	if _, _, err := repo.CancelReview(ctx, owner, req.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if pending, err := repo.HasPendingReview(ctx, owner, "plugin-1"); err != nil || pending {
		t.Fatalf("HasPendingReview after cancel = %v, %v; want false", pending, err)
	}
}

func containsPlugin(items []model.Plugin, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
func seedVersionRow(t *testing.T, database *sql.DB, versionID, pluginID, seq string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO plugin_versions
		(version_id, plugin_id, version, manifest_json, plugin_json, manifest_hash, plugin_hash,
		 relations_json, created_by, created_at)
		VALUES (?, ?, ?, JSON_OBJECT(), JSON_OBJECT(), REPEAT('a',71), REPEAT('b',71),
		        JSON_ARRAY(), 'user-1', NOW(3))`,
		versionID, pluginID, seq,
	); err != nil {
		t.Fatalf("seed version %s: %v", versionID, err)
	}
}

func seedCategory(t *testing.T, database *sql.DB, categoryID, typesJSON string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO plugin_categories
		(category_id, name, plugin_types_json, sort_order, status, created_at, updated_at)
		VALUES (?, ?, CAST(? AS JSON), 0, 1, NOW(3), NOW(3))`,
		categoryID, categoryID, typesJSON,
	); err != nil {
		t.Fatalf("seed category %s: %v", categoryID, err)
	}
}

// adoptAsRelationTarget drives the WRITE path that embeds visibilitySQL: it has
// the caller edit a plugin they own and declare targetID as a relation target.
// lockRelationTargets resolves that target under the caller's scope, so an
// unreachable target fails rather than being silently wired in.
func adoptAsRelationTarget(ctx context.Context, repo *pluginrepo.Repo, scope pluginrepo.Scope, sourceID, targetID string) error {
	source, err := repo.Get(ctx, scope, sourceID)
	if err != nil {
		return err
	}
	_, err = repo.Update(ctx, scope, pluginrepo.Mutation{
		Plugin: *source,
		Relations: []model.PluginRelation{{
			TargetPluginID: targetID,
			Type:           "expert_skill",
			Data:           json.RawMessage(`{}`),
			Status:         1,
		}},
		OperatorID: scope.CallerUID,
		RequestID:  "adopt-probe",
	})
	return err
}
