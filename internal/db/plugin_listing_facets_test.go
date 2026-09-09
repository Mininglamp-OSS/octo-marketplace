package db

import (
	"context"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// TestGridFacetsAgreeWithTheGrid pins the rule that makes the facets usable: a
// category count or a tag chip must describe the rows the grid will actually
// return. The grid is gated on listing_state (listedSQL); visibilitySQL is not,
// on purpose, so the AUTHOR of a draft is the one caller for whom the two sets
// diverge — and drafts are commonest exactly where the author is looking.
//
// Before the gate reached the facets, the author saw a category counted 2 next to
// a page holding 1, and a tag chip that filtered to an empty grid. Both are
// asserted from the author's scope, because from anyone else's the draft is
// already invisible and the bug is unobservable.
//
// The mine direction is asserted too: 我的发布 shows every state, so its chips must
// keep the draft's tags or the author cannot filter their own drafts.
func TestGridFacetsAgreeWithTheGrid(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()

	seed(t, database, seedPlugin{id: "listed-1", visibility: "space", listingState: "published", currentVersion: "1.0.0"})
	seed(t, database, seedPlugin{id: "draft-1", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	seedCategory(t, database, "cat-x", `["skill"]`)
	if _, err := database.Exec(`INSERT INTO plugin_category_placements
		(placement_id, placement_code, plugin_type, category_id, visible, sort_order, created_at, updated_at)
		VALUES ('cp-x', 'default', 'skill', 'cat-x', 1, 0, NOW(3), NOW(3))`); err != nil {
		t.Fatalf("register category at placement: %v", err)
	}
	// Both rows sit in the same category; the draft additionally carries a tag no
	// listed row has, which is what makes an empty-grid chip observable.
	if _, err := database.Exec(
		`UPDATE plugins SET category_id='cat-x', tags_json=JSON_ARRAY('shared-tag') WHERE plugin_id='listed-1'`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`UPDATE plugins SET category_id='cat-x', tags_json=JSON_ARRAY('shared-tag','draft-only-tag') WHERE plugin_id='draft-1'`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE plugin_placements SET category_id='cat-x'`); err != nil {
		t.Fatal(err)
	}

	grid, _, err := repo.List(ctx, owner, pluginrepo.ListFilter{PlacementCode: "default", Type: model.PluginTypeSkill, Limit: 100})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(grid) != 1 || grid[0].ID != "listed-1" {
		t.Fatalf("grid = %d rows %v, want only listed-1", len(grid), pluginIDs(grid))
	}

	cats, err := repo.ListPlacementCategories(ctx, owner, "default", model.PluginTypeSkill)
	if err != nil {
		t.Fatalf("ListPlacementCategories: %v", err)
	}
	var counted bool
	for _, c := range cats {
		if c.ID != "cat-x" {
			continue
		}
		counted = true
		if c.PluginCount != len(grid) {
			t.Errorf("category plugin_count = %d, want %d (the rows the grid returns); the author's own draft is counted", c.PluginCount, len(grid))
		}
	}
	if !counted {
		t.Fatal("cat-x was not returned; the fixture never reached the assertion")
	}

	tags, err := repo.ListTags(ctx, owner, pluginrepo.TagListFilter{PlacementCode: "default", Type: model.PluginTypeSkill})
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	got := map[string]int{}
	for _, tag := range tags {
		got[tag.Name] = tag.Count
	}
	if _, ok := got["draft-only-tag"]; ok {
		t.Error("the grid's tag chips offer draft-only-tag; selecting it filters to an empty grid")
	}
	if got["shared-tag"] != 1 {
		t.Errorf("shared-tag count = %d, want 1 (only the listed row)", got["shared-tag"])
	}

	mine, err := repo.ListTags(ctx, owner, pluginrepo.TagListFilter{PlacementCode: "default", Type: model.PluginTypeSkill, Mine: true})
	if err != nil {
		t.Fatalf("ListTags(mine): %v", err)
	}
	mineTags := map[string]int{}
	for _, tag := range mine {
		mineTags[tag.Name] = tag.Count
	}
	if mineTags["draft-only-tag"] != 1 {
		t.Errorf("draft-only-tag count on 我的发布 = %d, want 1; the author cannot filter their own drafts", mineTags["draft-only-tag"])
	}
	if mineTags["shared-tag"] != 2 {
		t.Errorf("shared-tag count on 我的发布 = %d, want 2 (every state)", mineTags["shared-tag"])
	}
}

func pluginIDs(items []model.Plugin) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, items[i].ID)
	}
	return out
}
