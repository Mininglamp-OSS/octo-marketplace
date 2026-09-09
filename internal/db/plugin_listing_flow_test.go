package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// TestPublishImmediatelyListsAPrivatePlugin covers the no-review branch and the
// placement self-heal that has to come with it: flipping listing_state alone lists
// nothing for a row whose default placement is missing or hidden, and the author
// cannot repair that afterwards.
func TestPublishImmediatelyListsAPrivatePlugin(t *testing.T) {
	for _, tc := range []struct {
		name      string
		placement string // "" = leave the seeded visible one alone
	}{
		{"with a healthy placement", ""},
		{"with a hidden placement", "hide"},
		{"with no placement at all", "drop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := reviewDB(t)
			repo := pluginrepo.New(database)
			ctx := context.Background()
			seed(t, database, seedPlugin{id: "plugin-1", visibility: "private", listingState: "draft", currentVersion: "1.0.0"})
			switch tc.placement {
			case "hide":
				if _, err := database.Exec(`UPDATE plugin_placements SET visible=0 WHERE plugin_id='plugin-1'`); err != nil {
					t.Fatal(err)
				}
			case "drop":
				if _, err := database.Exec(`DELETE FROM plugin_placements WHERE plugin_id='plugin-1'`); err != nil {
					t.Fatal(err)
				}
			}

			owner := tenantScope()
			published, err := repo.PublishPlugin(ctx, owner, pluginrepo.PublishParams{
				PluginID: "plugin-1", OperatorID: "user-1", OperatorName: "Alice", RequestID: "req-1",
			})
			if err != nil {
				t.Fatalf("PublishPlugin: %v", err)
			}
			if published.ListingState != model.PluginListingStatePublished {
				t.Errorf("listing_state = %q, want published", published.ListingState)
			}
			// visibility is untouched: a private publish lists the plugin to its owner
			// only, which is exactly what 仅自己可见 + 已发布 means.
			if published.Visibility != model.PluginVisibilityPrivate {
				t.Errorf("visibility = %q; publish must not widen the audience", published.Visibility)
			}
			var visible int
			if err := database.QueryRow(
				`SELECT COUNT(*) FROM plugin_placements WHERE plugin_id='plugin-1' AND placement_code='default' AND visible=1`,
			).Scan(&visible); err != nil {
				t.Fatal(err)
			}
			if visible != 1 {
				t.Errorf("visible default placements = %d, want 1; the plugin is listed but unreachable", visible)
			}
			// No version is minted: every save already snapshots one, and
			// plugin_versions.version is a per-plugin counter, not the author's label.
			var versions int
			if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id='plugin-1'`).Scan(&versions); err != nil {
				t.Fatal(err)
			}
			if versions != 0 {
				t.Errorf("publish minted %d version rows; it must not perturb the counter", versions)
			}
			assertAudit(t, database, "plugin-1", "publish", 1)
		})
	}
}

// TestDelistCancelsThePendingReviewAndFreesTheSlot is the interlock that keeps an
// approval from relisting a plugin behind the admin's back, and it also has to
// release the single-pending slot so the author can resubmit after editing.
func TestDelistCancelsThePendingReviewAndFreesTheSlot(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "published", currentVersion: "1.0.0"})

	owner := tenantScope()
	reviewer := reviewerScope()

	// An upgrade request sitting in the queue when the admin takes the plugin down.
	req := newRequest("plugin-1", "2.0.0")
	if err := repo.InsertReviewRequest(ctx, owner, req, snapshotOf(`{"plugin_name":"V2"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}

	delisted, err := repo.DelistPlugin(ctx, reviewer, pluginrepo.DelistParams{
		PluginID: "plugin-1", OperatorID: "admin-1", OperatorName: "Adam", RequestID: "req-1", Reason: "policy violation",
	})
	if err != nil {
		t.Fatalf("DelistPlugin: %v", err)
	}
	if delisted.ListingState != model.PluginListingStateDelisted {
		t.Errorf("listing_state = %q, want delisted", delisted.ListingState)
	}
	// visibility is untouched: the declared intent survives a takedown so the
	// author can edit and republish without restating it.
	if delisted.Visibility != model.PluginVisibilitySpace {
		t.Errorf("visibility = %q; delist must not rewrite the declared intent", delisted.Visibility)
	}

	var status, reason string
	if err := database.QueryRow(`SELECT status, COALESCE(reason,'') FROM plugin_review_requests WHERE review_id=?`, req.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != string(model.ReviewStatusCanceled) {
		t.Errorf("pending request status = %q, want canceled; an approval could still relist it", status)
	}
	if reason != "policy violation" {
		t.Errorf("cancellation reason = %q, want the admin's reason", reason)
	}

	// The slot is free: the author can resubmit after editing.
	again := newRequest("plugin-1", "2.0.1")
	if err := repo.InsertReviewRequest(ctx, owner, again, snapshotOf(`{"plugin_name":"V2.0.1"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatalf("resubmit after delist: %v; the single-pending slot was not released", err)
	}

	// Placements are deliberately untouched — hiding one would also hide the plugin
	// from its own author's 我的发布, which shares the placement join.
	var visible int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM plugin_placements WHERE plugin_id='plugin-1' AND visible=1`,
	).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible != 1 {
		t.Errorf("visible placements after delist = %d, want 1 (untouched)", visible)
	}
	assertAudit(t, database, "plugin-1", "delist", 1)
}

// A delisted plugin's published label stays spent, or a republish could reuse a
// version the org already saw.
func TestPublishedLabelsIncludeADelistedPluginsCurrentVersion(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "delisted", currentVersion: "1.0.0"})

	err := repo.InsertReviewRequest(ctx, tenantScope(), newRequest("plugin-1", "1.0.0"),
		snapshotOf(`{"plugin_name":"Again"}`, `{"attachments":[]}`, nil))
	if !errors.Is(err, pluginrepo.ErrLabelTaken) {
		t.Fatalf("reusing a delisted plugin's live label err = %v, want ErrLabelTaken", err)
	}
	// A fresh label is fine.
	if err := repo.InsertReviewRequest(ctx, tenantScope(), newRequest("plugin-1", "1.0.1"),
		snapshotOf(`{"plugin_name":"Again"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatalf("a fresh label after delisting was refused: %v", err)
	}
}

// Both transactions CAS on state, so a double-click produces one winner and one
// ErrConflict rather than two audit rows for the same event.
func TestConcurrentPublishAndDelistProduceOneWinner(t *testing.T) {
	t.Run("publish", func(t *testing.T) {
		database := reviewDB(t)
		repo := pluginrepo.New(database)
		seed(t, database, seedPlugin{id: "plugin-1", visibility: "private", listingState: "draft", currentVersion: "1.0.0"})
		errs := raceTwo(func() error {
			_, err := repo.PublishPlugin(context.Background(), tenantScope(), pluginrepo.PublishParams{
				PluginID: "plugin-1", OperatorID: "user-1", OperatorName: "Alice",
			})
			return err
		})
		assertOneWinner(t, errs)
		assertAudit(t, database, "plugin-1", "publish", 1)
	})

	t.Run("delist", func(t *testing.T) {
		database := reviewDB(t)
		repo := pluginrepo.New(database)
		seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "published", currentVersion: "1.0.0"})
		errs := raceTwo(func() error {
			_, err := repo.DelistPlugin(context.Background(), reviewerScope(), pluginrepo.DelistParams{
				PluginID: "plugin-1", OperatorID: "admin-1", OperatorName: "Adam",
			})
			return err
		})
		assertOneWinner(t, errs)
		assertAudit(t, database, "plugin-1", "delist", 1)
	})
}

// TestDelistRefusesRowsThatAreNotOrgContent pins the two guards that decide WHAT
// a Space admin's takedown power reaches. The transaction locks by space_id
// alone, so without them the role check is the only thing standing between an
// admin and any row in the Space.
//
//   - Another member's PRIVATE published plugin is not org content: nobody but
//     its author can read it (visibilitySQL admits a private row only to its
//     owner), so there is nothing to take down — and a successful delist would be
//     an existence oracle for a plugin the admin cannot GET, plus a way to
//     interfere with a colleague's private work.
//   - An EMBEDDED child is listed and un-listed by its container. Taking one down
//     on its own leaves the container published while a member it declares is
//     hidden, and every non-owner install of the PARENT then fails with
//     ErrDependencyHidden.
//
// Both must be ErrNotFound — the same answer the read the admin is allowed to
// make would give — and must leave the row and the audit trail untouched.
func TestDelistRefusesRowsThatAreNotOrgContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  seedPlugin
	}{
		{
			name: "another member's published private plugin",
			row:  seedPlugin{id: "p-others-private", visibility: "private", listingState: "published", owner: "user-2", currentVersion: "1.0.0"},
		},
		{
			// The deliberate consequence: a published private row has no takedown
			// path at all, not even for its own author holding the admin role. It
			// needs none — published+private is "listed to its owner alone".
			name: "the admin's own published private plugin",
			row:  seedPlugin{id: "p-own-private", visibility: "private", listingState: "published", owner: "admin-1", currentVersion: "1.0.0"},
		},
		{
			name: "an embedded child promoted with its container",
			row:  seedPlugin{id: "p-child", visibility: "space", listingState: "published", embedded: true, currentVersion: "1.0.0"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := reviewDB(t)
			repo := pluginrepo.New(database)
			seed(t, database, tc.row)

			_, err := repo.DelistPlugin(context.Background(), reviewerScope(), pluginrepo.DelistParams{
				PluginID: tc.row.id, OperatorID: "admin-1", OperatorName: "Adam", Reason: "takedown",
			})
			if !errors.Is(err, pluginrepo.ErrNotFound) {
				t.Fatalf("DelistPlugin = %v, want ErrNotFound", err)
			}
			var listing string
			if err := database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, tc.row.id).Scan(&listing); err != nil {
				t.Fatal(err)
			}
			if listing != string(model.PluginListingStatePublished) {
				t.Errorf("listing_state = %q; the row was taken down anyway", listing)
			}
			assertAudit(t, database, tc.row.id, "delist", 0)
		})
	}
}

// A takedown from a DIFFERENT Space must be indistinguishable from a plugin that
// does not exist: confirming existence across a Space boundary is a leak even to
// a real admin, and delist is the one listing transaction that does not lock by
// owner.
func TestDelistFromAnotherSpaceIsNotFound(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "published", currentVersion: "1.0.0"})

	foreignAdmin := pluginrepo.Scope{CallerUID: "admin-9", SpaceID: "space-b"}
	_, err := repo.DelistPlugin(context.Background(), foreignAdmin, pluginrepo.DelistParams{
		PluginID: "plugin-1", OperatorID: "admin-9", OperatorName: "Eve",
	})
	if !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("DelistPlugin = %v, want ErrNotFound", err)
	}
	var listing string
	if err := database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id='plugin-1'`).Scan(&listing); err != nil {
		t.Fatal(err)
	}
	if listing != string(model.PluginListingStatePublished) {
		t.Errorf("listing_state = %q; an admin of another Space took the plugin down", listing)
	}
	assertAudit(t, database, "plugin-1", "delist", 0)
}

// TestDelistDemotesEmbeddedChildren closes P1-2: delisting a container must take
// its embedded children down with it. Before the fix DelistPlugin un-listed only
// the top row, leaving every bundled skill / member expert at space+published —
// a state visibilitySQL admits to any Space member — so after the takedown a
// colleague could still GET, download and install the child's full content
// behind the hidden parent. demoteEmbeddedChildren now flips the children to
// delisted in the same transaction while leaving their `space` visibility so a
// later re-approve re-promotes them.
func TestDelistDemotesEmbeddedChildren(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)

	// A published expert with a bundled (embedded) skill.
	seed(t, database, seedPlugin{id: "expert-1", typ: "expert", visibility: "space", listingState: "published", currentVersion: "1.0.0"})
	seed(t, database, seedPlugin{id: "skill-1", typ: "skill", visibility: "space", listingState: "published", embedded: true, currentVersion: "1.0.0"})
	seedRelation(t, database, "rel-1", "expert-1", "skill-1", "expert_skill")

	if _, err := repo.DelistPlugin(context.Background(), reviewerScope(), pluginrepo.DelistParams{
		PluginID: "expert-1", OperatorID: "admin-1", OperatorName: "Adam", Reason: "takedown",
	}); err != nil {
		t.Fatalf("DelistPlugin: %v", err)
	}

	var parentListing, childListing, childVisibility string
	if err := database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id='expert-1'`).Scan(&parentListing); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT listing_state, visibility FROM plugins WHERE plugin_id='skill-1'`).Scan(&childListing, &childVisibility); err != nil {
		t.Fatal(err)
	}
	if parentListing != string(model.PluginListingStateDelisted) {
		t.Errorf("parent listing_state = %q, want delisted", parentListing)
	}
	if childListing != string(model.PluginListingStateDelisted) {
		t.Fatalf("child listing_state = %q; the bundled skill stayed org-readable behind the delisted parent", childListing)
	}
	if childVisibility != string(model.PluginVisibilitySpace) {
		t.Errorf("child visibility = %q, want space (kept so a re-approve re-promotes it)", childVisibility)
	}
}

func raceTwo(fn func() error) []error {
	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = fn()
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

func assertOneWinner(t *testing.T, errs []error) {
	t.Helper()
	var won, conflicted int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, pluginrepo.ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if won != 1 || conflicted != 1 {
		t.Fatalf("won=%d conflicted=%d, want exactly one of each", won, conflicted)
	}
}

func assertAudit(t *testing.T, database *sql.DB, pluginID, action string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM plugin_audit_logs WHERE plugin_id=? AND action=?`, pluginID, action,
	).Scan(&got); err != nil {
		t.Fatalf("count %s audit rows: %v", action, err)
	}
	if got != want {
		t.Errorf("%s audit rows = %d, want %d", action, got, want)
	}
}

// TestMineListingCarriesTheDerivedStatus is what lets 我的发布 render a status column
// without fetching the review list and joining client-side. It also pins the
// precedence that mattered most in review: a LISTED plugin with a pending upgrade
// reads 审核中, not 已发布.
func TestMineListingCarriesTheDerivedStatus(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()

	seed(t, database, seedPlugin{id: "p-draft", visibility: "private", listingState: "draft", currentVersion: "1.0.0"})
	seed(t, database, seedPlugin{id: "p-live", visibility: "space", listingState: "published", currentVersion: "1.0.0"})
	seed(t, database, seedPlugin{id: "p-gone", visibility: "space", listingState: "delisted", currentVersion: "1.0.0"})
	seed(t, database, seedPlugin{id: "p-pending", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	seed(t, database, seedPlugin{id: "p-rejected", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	// A listed plugin whose NEXT version is under review.
	seed(t, database, seedPlugin{id: "p-upgrading", visibility: "space", listingState: "published", currentVersion: "1.0.0"})

	if err := repo.InsertReviewRequest(ctx, owner, newRequest("p-pending", "1.0.0"),
		snapshotOf(`{"plugin_name":"P"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertReviewRequest(ctx, owner, newRequest("p-upgrading", "2.0.0"),
		snapshotOf(`{"plugin_name":"P"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	rejected := newRequest("p-rejected", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, owner, rejected,
		snapshotOf(`{"plugin_name":"P"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.RejectReview(ctx, reviewerScope(), pluginrepo.RejectReviewParams{
		ReviewID: rejected.ID, ReviewerUID: "admin-1", ReviewerName: "Adam", Reason: "no",
	}); err != nil {
		t.Fatal(err)
	}

	items, _, err := repo.List(ctx, owner, pluginrepo.ListFilter{
		PlacementCode: "default", Mine: true, IncludeReviewState: true, Limit: 100,
	})
	if err != nil {
		t.Fatalf("List(mine): %v", err)
	}
	got := map[string]model.PluginDisplayStatus{}
	for i := range items {
		got[items[i].ID] = items[i].DisplayStatus(items[i].HasPendingReview, items[i].LatestReviewStatus)
	}
	for id, want := range map[string]model.PluginDisplayStatus{
		"p-draft":     model.PluginDisplayStatusDraft,
		"p-live":      model.PluginDisplayStatusPublished,
		"p-gone":      model.PluginDisplayStatusDelisted,
		"p-pending":   model.PluginDisplayStatusPendingReview,
		"p-rejected":  model.PluginDisplayStatusRejected,
		"p-upgrading": model.PluginDisplayStatusPendingReview,
	} {
		if got[id] != want {
			t.Errorf("%s display status = %q, want %q", id, got[id], want)
		}
	}

	// The marketplace grid must NOT pay for the extra lookups, and does not need
	// them: every row it returns is published by construction.
	grid, _, err := repo.List(ctx, owner, pluginrepo.ListFilter{PlacementCode: "default", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for i := range grid {
		if grid[i].HasPendingReview || grid[i].LatestReviewID != "" {
			t.Errorf("%s carries review state on the grid path", grid[i].ID)
		}
		if grid[i].ListingState != model.PluginListingStatePublished {
			t.Errorf("%s is on the grid with listing_state %q", grid[i].ID, grid[i].ListingState)
		}
	}
}

// TestReviewQueueHidesRequestsForDeletedPlugins pins a defect found by running
// the real queue against real data: the list joined `plugins` but never excluded
// a deleted one, so a reviewer saw rows they could not act on — ApproveReview
// locks the plugin with `deleted_at IS NULL` and 404s.
//
// The count is asserted alongside the page because the two used to disagree:
// only the page query joined plugins at all, so `total` counted rows the page
// could never return and the UI paginated against a number that did not exist.
func TestReviewQueueHidesRequestsForDeletedPlugins(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()
	reviewer := reviewerScope()

	seed(t, database, seedPlugin{id: "alive", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	seed(t, database, seedPlugin{id: "gone", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	for _, id := range []string{"alive", "gone"} {
		if err := repo.InsertReviewRequest(ctx, owner, newRequest(id, "1.0.0"),
			snapshotOf(`{"plugin_name":"P"}`, `{"attachments":[]}`, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`UPDATE plugins SET deleted_at=NOW(3) WHERE plugin_id='gone'`); err != nil {
		t.Fatal(err)
	}

	items, total, err := repo.ListReviewRequests(ctx, reviewer, pluginrepo.ReviewListFilter{
		SpaceID: "space-a", Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListReviewRequests: %v", err)
	}
	if total != int64(len(items)) {
		t.Errorf("total=%d but the page returned %d rows; the count and the page disagree", total, len(items))
	}
	for _, item := range items {
		if item.PluginID == "gone" {
			t.Error("the queue lists a request for a deleted plugin; approving it 404s")
		}
	}
	if len(items) != 1 {
		t.Errorf("items=%d, want 1 (only the surviving plugin's request)", len(items))
	}
}

// TestWideningVisibilityOnAPublishedPluginUnlistsIt closes a bypass found by
// driving the real API: a PUBLISHED PRIVATE plugin is editable by design —
// nobody else can read it — but writing `space` onto it while it stayed
// published listed it to the whole organization with no review at all. That is
// the one thing this workflow exists to prevent, and it was reachable from the
// ordinary edit form.
//
// Widening now drops the row back to draft, which costs the author nothing and
// puts it back on the 发布 path where the new visibility routes it through
// review.
func TestWideningVisibilityOnAPublishedPluginUnlistsIt(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()
	colleague := pluginrepo.Scope{CallerUID: "user-2", SpaceID: "space-a"}

	seed(t, database, seedPlugin{id: "p1", visibility: "private", listingState: "published", currentVersion: "1.0.0"})

	current, err := repo.Get(ctx, owner, "p1")
	if err != nil {
		t.Fatal(err)
	}
	widened := *current
	widened.Visibility = model.PluginVisibilitySpace
	if _, err := repo.Update(ctx, owner, pluginrepo.Mutation{
		Plugin: widened, OperatorID: "user-1", ResetListingToDraft: true,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, err := repo.Get(ctx, owner, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Visibility != model.PluginVisibilitySpace {
		t.Errorf("visibility = %q, want space (the declared intent must be stored)", after.Visibility)
	}
	if after.ListingState != model.PluginListingStateDraft {
		t.Fatalf("listing_state = %q, want draft; the plugin is org-visible without review", after.ListingState)
	}
	if _, err := repo.Get(ctx, colleague, "p1"); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Errorf("a colleague can read it (%v); widening bypassed review", err)
	}

	// A content-only edit must NOT un-list anything.
	seed(t, database, seedPlugin{id: "p2", visibility: "private", listingState: "published", currentVersion: "1.0.0"})
	live, err := repo.Get(ctx, owner, "p2")
	if err != nil {
		t.Fatal(err)
	}
	edited := *live
	edited.Publisher = "renamed"
	if _, err := repo.Update(ctx, owner, pluginrepo.Mutation{Plugin: edited, OperatorID: "user-1"}); err != nil {
		t.Fatal(err)
	}
	stillLive, err := repo.Get(ctx, owner, "p2")
	if err != nil {
		t.Fatal(err)
	}
	if stillLive.ListingState != model.PluginListingStatePublished {
		t.Errorf("a content-only edit un-listed the plugin (%q)", stillLive.ListingState)
	}
}

// TestPublishRefusesAPluginThatBecameOrgVisibleMidFlight closes a TOCTOU on the
// exact privilege boundary this feature exists to defend.
//
// Service.Publish decides "this is private, list it directly, no review" from an
// UNLOCKED read taken several round trips before the transaction. The owner can
// raise the row to `space` in that window through an ordinary upsert — legal on a
// draft — and the publish would then stamp `published` onto an org-visible row
// with no review request ever created. The owner controls both requests, so it is
// a retry loop, not a lucky race.
//
// Simulated by mutating the row between the read and the call, which is exactly
// what the concurrent upsert does.
func TestPublishRefusesAPluginThatBecameOrgVisibleMidFlight(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "p1", visibility: "private", listingState: "draft", currentVersion: "1.0.0"})

	// The window: the caller's view still says `private`, the row says `space`.
	if _, err := database.Exec(`UPDATE plugins SET visibility='space' WHERE plugin_id='p1'`); err != nil {
		t.Fatal(err)
	}

	_, err := repo.PublishPlugin(ctx, tenantScope(), pluginrepo.PublishParams{
		PluginID: "p1", OperatorID: "user-1", OperatorName: "Alice",
	})
	if !errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatalf("PublishPlugin = %v, want ErrConflict", err)
	}

	var listing string
	if err := database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id='p1'`).Scan(&listing); err != nil {
		t.Fatal(err)
	}
	if listing != string(model.PluginListingStateDraft) {
		t.Fatalf("listing_state = %q; unreviewed content reached the organization marketplace", listing)
	}
	// And nobody else can see it, which is the consequence that actually matters.
	colleague := pluginrepo.Scope{CallerUID: "user-2", SpaceID: "space-a"}
	if _, err := repo.Get(ctx, colleague, "p1"); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Errorf("a colleague can read it (%v); the review gate was bypassed", err)
	}
}

// TestPublishRefusesAPluginWithAReviewSubmittedMidFlight closes the sibling
// TOCTOU: Service.Publish reads "no pending review" UNLOCKED, several round trips
// before the transaction. A concurrent SubmitReview can commit a first-listing
// request in that window; publish then stamps `published` onto the still-private
// row, landing it in private+published+pending — a state where the reviewer's
// later approval takes the content-only branch and never lists it, an
// approved-but-invisible row. PublishPlugin now re-checks the pending request
// under the plugin row lock and refuses. Simulated by inserting the request
// between the service's read and the repository call.
func TestPublishRefusesAPluginWithAReviewSubmittedMidFlight(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	// A space-intent draft that a concurrent SubmitReview would leave pending.
	// Publish's review branch fires when visibility=space (listing.go:91) and
	// never reaches the direct PublishPlugin path, so seed the state the racing
	// submit would leave rather than the pre-submit state: the row is still a
	// draft with visibility=space, and a pending request is in flight.
	seed(t, database, seedPlugin{id: "p1", visibility: "space", listingState: "draft", currentVersion: "1.0.0"})
	if err := repo.InsertReviewRequest(ctx, tenantScope(), newRequest("p1", "1.0.0"),
		snapshotOf(`{"plugin_name":"V1"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatalf("InsertReviewRequest: %v", err)
	}

	_, err := repo.PublishPlugin(ctx, tenantScope(), pluginrepo.PublishParams{
		PluginID: "p1", OperatorID: "user-1", OperatorName: "Alice",
	})
	// The raced path is rejected either way: if the visibility re-derivation
	// fires first we get ErrConflict (space drafts never go through the direct
	// publish branch to begin with), and if the pending check fires first we
	// get ErrReviewPending. Either outcome refuses the stamp and leaves the row
	// a draft — which is what the assertion below pins.
	if err == nil || (!errors.Is(err, pluginrepo.ErrReviewPending) && !errors.Is(err, pluginrepo.ErrConflict)) {
		t.Fatalf("PublishPlugin = %v, want ErrReviewPending or ErrConflict", err)
	}
	var listing string
	if err := database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id='p1'`).Scan(&listing); err != nil {
		t.Fatal(err)
	}
	if listing != string(model.PluginListingStateDraft) {
		t.Fatalf("listing_state = %q; publish raced past the pending review", listing)
	}
	assertAudit(t, database, "p1", "publish", 0)
}

// TestUpdateRefusesAVisibilityChangeWithAReviewSubmittedMidFlight closes the
// TOCTOU on the guard Service.update added: a visibility change while a review is
// pending is refused, but from an UNLOCKED read. A concurrent SubmitReview can
// land after that read; ApproveReview would then stamp visibility=space against
// the author's since-changed intent. Repo.Update now re-checks the pending
// request under the plugin row lock when the service flags a visibility change.
func TestUpdateRefusesAVisibilityChangeWithAReviewSubmittedMidFlight(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()
	// A space-intent draft with a submission in flight.
	seed(t, database, seedPlugin{id: "p1", visibility: "space", listingState: "draft", currentVersion: "1.0.0"})
	if err := repo.InsertReviewRequest(ctx, owner, newRequest("p1", "1.0.0"),
		snapshotOf(`{"plugin_name":"V1"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatalf("InsertReviewRequest: %v", err)
	}

	current, err := repo.Get(ctx, owner, "p1")
	if err != nil {
		t.Fatal(err)
	}
	narrowed := *current
	narrowed.Visibility = model.PluginVisibilityPrivate
	_, err = repo.Update(ctx, owner, pluginrepo.Mutation{
		Plugin: narrowed, OperatorID: "user-1", RefusePendingReview: true,
	})
	if !errors.Is(err, pluginrepo.ErrReviewPending) {
		t.Fatalf("Update = %v, want ErrReviewPending", err)
	}
	// The row is untouched: still space-intent, so the pending approval matches
	// the author's stated intent.
	after, err := repo.Get(ctx, owner, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Visibility != model.PluginVisibilitySpace {
		t.Errorf("visibility = %q; the change slipped past the pending guard", after.Visibility)
	}
}

// TestApproveListsAStrandedPublishedPrivateRow pins the both-axes isFirst
// derivation. A published+PRIVATE row is not org content (only its owner reads
// it), so approving its first-listing request must STAMP visibility=space rather
// than take the content-only upgrade branch and leave it invisible forever. The
// state is reachable only through a lost race that PublishPlugin now refuses, but
// ApproveReview derives the branch defensively from both axes regardless.
func TestApproveListsAStrandedPublishedPrivateRow(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()
	colleague := pluginrepo.Scope{CallerUID: "user-2", SpaceID: "space-a"}
	seed(t, database, seedPlugin{id: "p1", visibility: "private", listingState: "published", currentVersion: "1.0.0"})
	req := newRequest("p1", "2.0.0")
	if err := repo.InsertReviewRequest(ctx, owner, req,
		snapshotOf(`{"plugin_name":"V2"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatalf("InsertReviewRequest: %v", err)
	}

	out, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	})
	if err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}
	if out.Visibility != model.PluginVisibilitySpace {
		t.Errorf("visibility = %q; approval left a published+private row unlisted", out.Visibility)
	}
	if out.ListingState != model.PluginListingStatePublished {
		t.Errorf("listing_state = %q, want published", out.ListingState)
	}
	// The consequence that matters: a colleague can now read it.
	if _, err := repo.Get(ctx, colleague, "p1"); err != nil {
		t.Errorf("colleague cannot read the approved plugin (%v); approval was a silent no-op", err)
	}
}

// TestUpdateRefusesAContentSaveThatOverlapsAnApproval closes P1-1: the other
// half of the listed-gate TOCTOU. Editing content while a first-listing review
// is pending is legal BY DESIGN — the reviewer acts on the frozen snapshot — so
// this is the normal usage pattern, not an exotic race. Service.update decides
// `listed_requires_review` from an UNLOCKED read taken several round trips before
// the transaction: the row reads space+draft, so the gate does not fire. If an
// admin approves the pending request in that window (stamping space+published
// with the frozen snapshot), the owner's save then locks the now-published row
// and — before this fix — wrote its content columns straight onto it, silently
// replacing the just-approved snapshot with content no reviewer ever saw.
//
// Repo.Update now re-derives the gate from the row it LOCKS (EnforceListingGate,
// set by the service for every non-admin caller): a locked space+published row is
// refused with ErrListedRequiresReview and the content never lands. The approval
// simulated by flipping the row the way ApproveReview leaves it, between the
// service's read and the repository call.
func TestUpdateRefusesAContentSaveThatOverlapsAnApproval(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()
	colleague := pluginrepo.Scope{CallerUID: "user-2", SpaceID: "space-a"}

	// A space-intent draft with a first-listing request in flight — the state of
	// anything awaiting review.
	seed(t, database, seedPlugin{
		id: "p1", visibility: "space", listingState: "draft", currentVersion: "1.0.0",
		manifest: `{"plugin_name":"APPROVED"}`,
	})

	// The owner's Update reads the row while it is still space+draft.
	current, err := repo.Get(ctx, owner, "p1")
	if err != nil {
		t.Fatal(err)
	}

	// The window: an admin approval commits, stamping space+published with the
	// frozen snapshot, before the owner's save takes the lock.
	if _, err := database.Exec(
		`UPDATE plugins SET listing_state='published' WHERE plugin_id='p1'`); err != nil {
		t.Fatal(err)
	}

	// The owner's content-only save (visibility unchanged) now lands. The service
	// sets EnforceListingGate for the non-admin caller, so Repo.Update re-derives
	// the gate from the LOCKED (space+published) row and refuses.
	edited := *current
	edited.Manifest = json.RawMessage(`{"plugin_name":"UNREVIEWED"}`)
	edited.ManifestHash = strings.Repeat("c", 71)
	_, err = repo.Update(ctx, owner, pluginrepo.Mutation{
		Plugin: edited, OperatorID: "user-1", EnforceListingGate: true,
	})
	if !errors.Is(err, pluginrepo.ErrListedRequiresReview) {
		t.Fatalf("Update = %v, want ErrListedRequiresReview; unreviewed content reached a live org row", err)
	}

	// The approved snapshot survives untouched; the whole Space still reads it.
	after, err := repo.Get(ctx, colleague, "p1")
	if err != nil {
		t.Fatalf("colleague cannot read the listed plugin (%v)", err)
	}
	if got := string(after.Manifest); !strings.Contains(got, "APPROVED") {
		t.Fatalf("manifest = %s; the content save overwrote the approved snapshot", got)
	}
}

// TestEnforceListingGateAllowsEditingAnUnlistedRow guards the gate's precision: a
// non-admin caller with EnforceListingGate set can still freely edit a draft (or
// delisted) row, and a widen of a locked-published row is re-derived to un-list
// it from the LOCKED value rather than the service's stale hint.
func TestEnforceListingGateAllowsEditingAnUnlistedRow(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()

	// A plain draft edit is allowed.
	seed(t, database, seedPlugin{id: "p1", visibility: "space", listingState: "draft", currentVersion: "1.0.0"})
	draft, err := repo.Get(ctx, owner, "p1")
	if err != nil {
		t.Fatal(err)
	}
	edited := *draft
	edited.Publisher = "renamed"
	if _, err := repo.Update(ctx, owner, pluginrepo.Mutation{
		Plugin: edited, OperatorID: "user-1", EnforceListingGate: true,
	}); err != nil {
		t.Fatalf("Update of a draft with the gate on = %v, want success", err)
	}

	// A widen of a published PRIVATE row un-lists it — re-derived from the locked
	// row even when the service did not set the ResetListingToDraft hint.
	seed(t, database, seedPlugin{id: "p2", visibility: "private", listingState: "published", currentVersion: "1.0.0"})
	priv, err := repo.Get(ctx, owner, "p2")
	if err != nil {
		t.Fatal(err)
	}
	widened := *priv
	widened.Visibility = model.PluginVisibilitySpace
	if _, err := repo.Update(ctx, owner, pluginrepo.Mutation{
		Plugin: widened, OperatorID: "user-1", EnforceListingGate: true,
	}); err != nil {
		t.Fatalf("widen with the gate on = %v, want success", err)
	}
	after, err := repo.Get(ctx, owner, "p2")
	if err != nil {
		t.Fatal(err)
	}
	if after.ListingState != model.PluginListingStateDraft {
		t.Fatalf("listing_state = %q, want draft; the widen did not un-list from the locked row", after.ListingState)
	}
}

// Forward-only, restated under the row lock. Both update surfaces compare the
// submitted label against an UNLOCKED read and then take the FOR UPDATE lock
// several round trips later; anything that advances current_version in that window
// makes the comparison stale. The reachable route is an admin edit racing an
// approval: AdminUpdate sets SnapshotVersion without EnforceListingGate, so before
// this guard a regressing label could land on an already-listed row.
//
// The mutation carries a label BELOW the stored one, which is exactly the state the
// service hands the repo after losing that race — the pre-check saw 1.0.0 and
// approved 1.0.0, but the locked row is already at 2.0.0. It must be refused, and
// the row must be untouched.
func TestUpdateRefusesALabelTheLockedRowHasMovedPast(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()

	seed(t, database, seedPlugin{id: "p1", visibility: "space", listingState: "published", currentVersion: "2.0.0"})
	current, err := repo.Get(ctx, owner, "p1")
	if err != nil {
		t.Fatal(err)
	}
	stale := *current
	behind := "1.0.0"
	stale.CurrentVersion = &behind
	stale.Publisher = "edited"

	_, err = repo.Update(ctx, owner, pluginrepo.Mutation{
		Plugin: stale, OperatorID: "user-1", SnapshotVersion: true, EnforceForwardOnlyVersion: true,
	})
	if !errors.Is(err, pluginrepo.ErrVersionRegressed) {
		t.Fatalf("Update with a label below the locked row = %v, want ErrVersionRegressed", err)
	}
	after, err := repo.Get(ctx, owner, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if after.CurrentVersion == nil || *after.CurrentVersion != "2.0.0" {
		t.Errorf("current_version = %v, want 2.0.0 untouched", after.CurrentVersion)
	}
	if after.Publisher == "edited" {
		t.Error("the refused update committed its other columns; the guard must abort the whole transaction")
	}

	// The same save with the label the locked row actually carries is fine — the
	// guard must not refuse an ordinary edit that re-sends the stored label, which
	// is what every fetch-edit-save client does.
	unchanged := *current
	unchanged.Publisher = "edited"
	if _, err := repo.Update(ctx, owner, pluginrepo.Mutation{
		Plugin: unchanged, OperatorID: "user-1", SnapshotVersion: true, EnforceForwardOnlyVersion: true,
	}); err != nil {
		t.Fatalf("re-sending the stored label was refused: %v", err)
	}
}
