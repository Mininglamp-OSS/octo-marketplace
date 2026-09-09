package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// listingFixture seeds one owned plugin in the given state.
func listingFixture(t *testing.T, visibility model.PluginVisibility, listing model.PluginListingState) (*fakeStore, *Service) {
	t.Helper()
	space := "space-a"
	version := "1.0.0"
	manifest := json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Example Plugin","plugin_type":"expert","name":"example-plugin","description":"An example plugin.","labels":[],"examples":[]}`)
	pkg := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[]}`)
	store := &fakeStore{
		relations: map[string][]model.PluginRelation{},
		plugins: map[string]*model.Plugin{
			"plugin-1": {
				ID: "plugin-1", Name: "Example Plugin", Type: model.PluginTypeExpert,
				OwnerUID: "user-1", SpaceID: &space, Visibility: visibility, ListingState: listing,
				CurrentVersion: &version, PluginHash: "sha256:p",
				Tags: json.RawMessage(`[]`), Manifest: manifest, Package: pkg,
			},
		},
	}
	// SubmitReview reads the request back after inserting it, so the fake needs
	// something to hand back or the review branch looks like it produced nothing.
	store.review.stored = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: space,
		Status: model.ReviewStatusPending, Kind: model.ReviewKindFirst, Version: "1.0.0",
		ApplicantUID: "user-1",
	}
	return store, fixedService(store)
}

// The whole reason Publish exists as one endpoint: the BACKEND decides whether
// publishing means listing or reviewing. A client that had to make this call
// itself would either list something unreviewed or strand a plugin in draft.
func TestPublishRoutesPrivateToImmediateAndSpaceToReview(t *testing.T) {
	t.Run("private lists immediately with no review", func(t *testing.T) {
		store, svc := listingFixture(t, model.PluginVisibilityPrivate, model.PluginListingStateDraft)
		result, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1"})
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if store.listing.publishCalls != 1 {
			t.Errorf("PublishPlugin called %d times, want 1", store.listing.publishCalls)
		}
		if store.review.insertReq != nil {
			t.Error("a private publish opened a review request")
		}
		if result.Review != nil {
			t.Error("result carries a review for a private publish")
		}
		if result.Plugin.Plugin.ListingState != model.PluginListingStatePublished {
			t.Errorf("listing_state = %q, want published", result.Plugin.Plugin.ListingState)
		}
	})

	t.Run("space opens a review and leaves the plugin a draft", func(t *testing.T) {
		store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
		result, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1"})
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if store.listing.publishCalls != 0 {
			t.Error("an org-visible publish listed the plugin directly, bypassing review")
		}
		if store.review.insertReq == nil {
			t.Fatal("no review request was opened")
		}
		if result.Review == nil {
			t.Error("result does not carry the review request; the client cannot tell which branch fired")
		}
		if result.Plugin.Plugin.ListingState != model.PluginListingStateDraft {
			t.Errorf("listing_state = %q, want draft until approval", result.Plugin.Plugin.ListingState)
		}
	})
}

// The review branch needs a version LABEL. Defaulting it to the draft's own
// current_version is what lets the 发布 button carry no fields at all.
func TestPublishDefaultsTheVersionLabelToTheDraftCurrentVersion(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	if _, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1"}); err != nil {
		t.Fatal(err)
	}
	if store.review.insertReq == nil {
		t.Fatal("no review request")
	}
	if got := store.review.insertReq.Version; got != "1.0.0" {
		t.Errorf("version = %q, want the draft's current_version 1.0.0", got)
	}
}

// An explicit label still wins, which is how the 升级版本 flow names its release.
func TestPublishHonoursAnExplicitVersionLabel(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	if _, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1", Version: "2.1.0", Changelog: "notes"}); err != nil {
		t.Fatal(err)
	}
	if got := store.review.insertReq.Version; got != "2.1.0" {
		t.Errorf("version = %q, want 2.1.0", got)
	}
}

// A second press means the client's view is stale, so it is a conflict rather
// than a silent no-op.
func TestPublishRefusesAnAlreadyPublishedPlugin(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilityPrivate, model.PluginListingStatePublished)
	_, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1"})
	if !errors.Is(err, ErrAlreadyPublished) {
		t.Fatalf("err = %v, want ErrAlreadyPublished", err)
	}
	if store.listing.publishCalls != 0 {
		t.Error("a redundant publish still reached the repository")
	}
}

// Publishing while a request is open would either duplicate the request or list
// the plugin behind the reviewer's back.
func TestPublishRefusesWhenAReviewIsPending(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.review.hasPending = true
	_, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1"})
	if !errors.Is(err, ErrReviewPending) {
		t.Fatalf("err = %v, want ErrReviewPending", err)
	}
	if store.review.insertReq != nil || store.listing.publishCalls != 0 {
		t.Error("a publish during a pending review still reached the repository")
	}
}

// A delisted plugin is republishable — that is the difference between a takedown
// and a deletion.
func TestPublishRelistsADelistedPlugin(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilityPrivate, model.PluginListingStateDelisted)
	if _, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1"}); err != nil {
		t.Fatalf("republishing a delisted plugin was refused: %v", err)
	}
	if store.listing.publishCalls != 1 {
		t.Errorf("PublishPlugin called %d times, want 1", store.listing.publishCalls)
	}
}

// Both refusals are 404-shaped: a non-owner must not learn the plugin exists, and
// an embedded child is published by its container.
func TestPublishRefusesANonOwnerAndAnEmbeddedChild(t *testing.T) {
	t.Run("non-owner", func(t *testing.T) {
		store, svc := listingFixture(t, model.PluginVisibilityPrivate, model.PluginListingStateDraft)
		store.plugins["plugin-1"].OwnerUID = "someone-else"
		if _, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if store.listing.publishCalls != 0 {
			t.Error("a non-owner publish reached the repository")
		}
	})
	t.Run("embedded child", func(t *testing.T) {
		store, svc := listingFixture(t, model.PluginVisibilityPrivate, model.PluginListingStateDraft)
		store.plugins["plugin-1"].IsEmbedded = true
		if _, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if store.listing.publishCalls != 0 {
			t.Error("an embedded child was published independently")
		}
	})
}

// Delist uses the SAME authority as approve/reject. A second notion of "who
// moderates this Space" would drift from the first.
func TestDelistRequiresTheReviewerRole(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caller  Caller
		allowed bool
	}{
		{"a plain member", Caller{UID: "user-2", Name: "M", SpaceID: "space-a", SpaceRole: SpaceRoleMember}, false},
		{"the OWNER acting as a plain member", Caller{UID: "user-1", Name: "O", SpaceID: "space-a", SpaceRole: SpaceRoleMember}, false},
		{"a Space admin", Caller{UID: "admin-1", Name: "A", SpaceID: "space-a", SpaceRole: SpaceRoleAdmin}, true},
		{"a Space owner", Caller{UID: "owner-1", Name: "O", SpaceID: "space-a", SpaceRole: SpaceRoleOwner}, true},
		{"a system admin", Caller{UID: "sys-1", Name: "S", SpaceID: "space-a", IsSystemAdmin: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStatePublished)
			_, err := svc.Delist(context.Background(), tc.caller, DelistParams{PluginID: "plugin-1"})
			if tc.allowed {
				if err != nil {
					t.Fatalf("Delist: %v", err)
				}
				if store.listing.delistCalls != 1 {
					t.Errorf("DelistPlugin called %d times, want 1", store.listing.delistCalls)
				}
				return
			}
			if !errors.Is(err, ErrReviewForbidden) {
				t.Fatalf("err = %v, want ErrReviewForbidden", err)
			}
			if store.listing.delistCalls != 0 {
				t.Error("an unauthorized delist reached the repository")
			}
		})
	}
}

// The author explicitly cannot take their own plugin down. This is the point of
// removing self-delisting: a plugin the org depends on must not vanish at its
// author's discretion.
func TestTheAuthorCannotDelistTheirOwnPlugin(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStatePublished)
	author := Caller{UID: "user-1", Name: "Author", SpaceID: "space-a", SpaceRole: SpaceRoleMember}
	if _, err := svc.Delist(context.Background(), author, DelistParams{PluginID: "plugin-1"}); !errors.Is(err, ErrReviewForbidden) {
		t.Fatalf("err = %v, want ErrReviewForbidden", err)
	}
	if store.listing.delistCalls != 0 {
		t.Error("the author delisted their own plugin")
	}
}

// A repository state-CAS miss is a conflict, not a 500 and not a 404.
func TestDelistOfAnUnpublishedPluginConflicts(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.listing.delistErr = pluginrepo.ErrConflict
	admin := Caller{UID: "admin-1", Name: "A", SpaceID: "space-a", SpaceRole: SpaceRoleAdmin}
	if _, err := svc.Delist(context.Background(), admin, DelistParams{PluginID: "plugin-1"}); !errors.Is(err, ErrNotPublished) {
		t.Fatalf("err = %v, want ErrNotPublished", err)
	}
}

// A plugin in another Space stays a 404: confirming existence across a Space
// boundary is a leak even to an admin, who is only an admin of THEIR Space.
func TestDelistAcrossSpacesIsNotFound(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStatePublished)
	store.listing.delistErr = pluginrepo.ErrNotFound
	admin := Caller{UID: "admin-9", Name: "A", SpaceID: "space-b", SpaceRole: SpaceRoleAdmin}
	if _, err := svc.Delist(context.Background(), admin, DelistParams{PluginID: "plugin-1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// DisplayStatus is the one place the two axes are folded together, so its
// precedence is pinned as a full truth table rather than sampled.
func TestDisplayStatusPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listing model.PluginListingState
		pending bool
		latest  model.ReviewStatus
		want    model.PluginDisplayStatus
	}{
		{"a fresh draft", model.PluginListingStateDraft, false, "", model.PluginDisplayStatusDraft},
		{"a draft with an open request", model.PluginListingStateDraft, true, model.ReviewStatusPending, model.PluginDisplayStatusPendingReview},
		{"a rejected draft", model.PluginListingStateDraft, false, model.ReviewStatusRejected, model.PluginDisplayStatusRejected},
		// Withdrawing returns the plugin to 草稿; there is no lingering "withdrawn".
		{"a canceled draft is just a draft", model.PluginListingStateDraft, false, model.ReviewStatusCanceled, model.PluginDisplayStatusDraft},
		{"a listed plugin", model.PluginListingStatePublished, false, model.ReviewStatusApproved, model.PluginDisplayStatusPublished},
		// The requirement that forced pending to outrank the listing axis: hiding an
		// open request behind 已发布 would leave the author waiting with no signal.
		{"a listed plugin with a new version under review", model.PluginListingStatePublished, true, model.ReviewStatusPending, model.PluginDisplayStatusPendingReview},
		{"a delisted plugin", model.PluginListingStateDelisted, false, model.ReviewStatusApproved, model.PluginDisplayStatusDelisted},
		{"a delisted plugin whose old request was rejected", model.PluginListingStateDelisted, false, model.ReviewStatusRejected, model.PluginDisplayStatusDelisted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &model.Plugin{ListingState: tc.listing}
			if got := p.DisplayStatus(tc.pending, tc.latest); got != tc.want {
				t.Errorf("DisplayStatus = %q, want %q", got, tc.want)
			}
		})
	}
}
