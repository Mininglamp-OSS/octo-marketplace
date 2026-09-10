package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Temporary compatibility publishes tenant creates immediately and allows
// owners to edit listed plugins. The declared audience and pending-review
// safeguards remain enforced. Assert the values sent to persistence.

func visibilityFixture(t *testing.T, current model.PluginVisibility, listing model.PluginListingState) (*fakeStore, *Service) {
	t.Helper()
	space := "space-a"
	manifest := json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Example Plugin","plugin_type":"expert","name":"example-plugin","description":"An example plugin.","labels":["one","two"],"examples":[]}`)
	pkg := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"AGENTS.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Example Plugin"}]}`)
	store := &fakeStore{
		relations: map[string][]model.PluginRelation{},
		plugins: map[string]*model.Plugin{
			"plugin-1": {
				ID: "plugin-1", Name: "Example Plugin", Type: model.PluginTypeExpert,
				OwnerUID: "user-1", SpaceID: &space, Visibility: current, ListingState: listing,
				Tags: json.RawMessage(`["one","two"]`), Manifest: manifest, Package: pkg,
			},
		},
	}
	return store, fixedService(store)
}

// A fresh tenant create lands PUBLISHED (listed immediately) and KEEPS the
// visibility it declared. Upload-is-publish: there is no separate publish step
// and no Space-review round trip.
func TestTenantCreateLandsPublishedWithTheDeclaredVisibility(t *testing.T) {
	for _, asked := range []model.PluginVisibility{
		model.PluginVisibilitySpace,
		model.PluginVisibilityPrivate,
		"",
	} {
		store := &fakeStore{}
		svc := fixedService(store)
		req := validRequest()
		req.Visibility = asked
		_, err := svc.Create(context.Background(), testCaller, req)
		if asked == "" {
			// An unset visibility was already invalid before this change and stays so.
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("empty visibility err = %v, want ErrInvalidRequest", err)
			}
			if store.create != nil {
				t.Fatalf("empty visibility persisted %q", store.create.Visibility)
			}
			continue
		}
		if err != nil {
			t.Fatalf("visibility=%q create err = %v", asked, err)
		}
		if store.create == nil {
			t.Fatalf("visibility=%q: nothing persisted", asked)
		}
		if store.create.Visibility != asked {
			t.Errorf("asked for %q, PERSISTED %q; the declared intent must survive", asked, store.create.Visibility)
		}
		if store.create.ListingState != model.PluginListingStatePublished {
			t.Errorf("visibility=%q PERSISTED listing_state %q; a tenant create must list immediately", asked, store.create.ListingState)
		}
	}
}

// `public` is retired on the write path and garbage is still a 400. Declaring an
// intent freely does not mean declaring anything at all.
func TestTenantCreateStillRejectsUnwritableVisibility(t *testing.T) {
	for _, asked := range []model.PluginVisibility{
		model.PluginVisibilityPublic,
		model.PluginVisibilitySystem, // system needs IsSystemAdmin
		"nonsense",
	} {
		store := &fakeStore{}
		svc := fixedService(store)
		req := validRequest()
		req.Visibility = asked
		if _, err := svc.Create(context.Background(), testCaller, req); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("visibility=%q err = %v, want ErrInvalidRequest", asked, err)
		}
		if store.create != nil {
			t.Errorf("visibility=%q was PERSISTED as %q", asked, store.create.Visibility)
		}
	}
}

// Raising the declared intent on an UNLISTED row is now legal, and has to be:
// the author who saved a private draft and then decided to share it with the org
// changes exactly this field. It still lists nothing — listing_state is untouched
// by an ordinary save, which the fake asserts by the absence of any state change.
func TestTenantMayChangeVisibilityIntentOnAnUnlistedRow(t *testing.T) {
	for _, tc := range []struct {
		name      string
		current   model.PluginVisibility
		listing   model.PluginListingState
		requested model.PluginVisibility
	}{
		{"draft private -> space", model.PluginVisibilityPrivate, model.PluginListingStateDraft, model.PluginVisibilitySpace},
		{"draft space -> private", model.PluginVisibilitySpace, model.PluginListingStateDraft, model.PluginVisibilityPrivate},
		{"delisted space -> private", model.PluginVisibilitySpace, model.PluginListingStateDelisted, model.PluginVisibilityPrivate},
		{"delisted space -> space", model.PluginVisibilitySpace, model.PluginListingStateDelisted, model.PluginVisibilitySpace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, svc := visibilityFixture(t, tc.current, tc.listing)
			req := validRequest()
			req.Visibility = tc.requested
			if _, err := svc.Update(context.Background(), testCaller, "plugin-1", req); err != nil {
				t.Fatalf("err = %v; changing intent on an unlisted row must be allowed", err)
			}
			if store.update == nil {
				t.Fatal("nothing persisted")
			}
			if store.update.Visibility != tc.requested {
				t.Fatalf("persisted visibility = %q, want %q", store.update.Visibility, tc.requested)
			}
		})
	}
}

// While the review rollout is deferred, the owner can edit a listed plugin
// directly. The edit persists like any other save.
func TestTenantCanEditAListedPluginDirectly(t *testing.T) {
	store, svc := visibilityFixture(t, model.PluginVisibilitySpace, model.PluginListingStatePublished)
	req := validRequest()
	req.Visibility = model.PluginVisibilitySpace
	req.Publisher = "Someone else"
	_, err := svc.Update(context.Background(), testCaller, "plugin-1", req)
	if err != nil {
		t.Fatalf("err = %v; a listed plugin must be owner-editable", err)
	}
	if store.update == nil {
		t.Fatal("the edit of a listed plugin was not persisted")
	}
}

// Published private plugins remain owner-editable.
func TestTenantMayEditAPublishedPrivatePlugin(t *testing.T) {
	store, svc := visibilityFixture(t, model.PluginVisibilityPrivate, model.PluginListingStatePublished)
	req := validRequest()
	req.Visibility = model.PluginVisibilityPrivate
	req.Publisher = "Updated"
	if _, err := svc.Update(context.Background(), testCaller, "plugin-1", req); err != nil {
		t.Fatalf("editing a published private plugin was refused: %v", err)
	}
	if store.update == nil {
		t.Fatal("nothing persisted")
	}
}

// Unlisted rows are freely editable in every combination, which is what makes
// "edit and publish again" work after a rejection or a takedown.
func TestTenantMayEditAnUnlistedRow(t *testing.T) {
	for _, tc := range []struct {
		name       string
		visibility model.PluginVisibility
		listing    model.PluginListingState
	}{
		{"private draft", model.PluginVisibilityPrivate, model.PluginListingStateDraft},
		{"space-intent draft", model.PluginVisibilitySpace, model.PluginListingStateDraft},
		{"delisted", model.PluginVisibilitySpace, model.PluginListingStateDelisted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, svc := visibilityFixture(t, tc.visibility, tc.listing)
			req := validRequest()
			req.Visibility = tc.visibility
			if _, err := svc.Update(context.Background(), testCaller, "plugin-1", req); err != nil {
				t.Fatalf("editing %s was refused: %v", tc.name, err)
			}
			if store.update == nil {
				t.Fatal("nothing persisted")
			}
		})
	}
}

// Lowering visibility on a listed plugin is now just an ordinary edit: it is
// allowed, it persists the new visibility, and it does NOT delist the row
// while the review gate and listing reset are temporarily bypassed.
func TestLoweringVisibilityOnAListedPluginIsAnOrdinaryEdit(t *testing.T) {
	store, svc := visibilityFixture(t, model.PluginVisibilitySpace, model.PluginListingStatePublished)
	req := validRequest()
	req.Visibility = model.PluginVisibilityPrivate
	_, err := svc.Update(context.Background(), testCaller, "plugin-1", req)
	if err != nil {
		t.Fatalf("err = %v; changing visibility on a listed plugin must be allowed", err)
	}
	if store.update == nil {
		t.Fatal("the visibility change was not persisted")
	}
	if store.update.Visibility != model.PluginVisibilityPrivate {
		t.Fatalf("persisted visibility = %q, want private", store.update.Visibility)
	}
}

// Content edits during a pending review are deliberately fine — the reviewer acts
// on a frozen snapshot. Changing VISIBILITY is not: ApproveReview stamps
// visibility=space, so approving a request whose author has since switched to
// 仅自己可见 would publish a row against their last stated intent.
func TestVisibilityChangeIsRefusedWhileAReviewIsPending(t *testing.T) {
	store, svc := visibilityFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.review.hasPending = true
	req := validRequest()
	req.Visibility = model.PluginVisibilityPrivate
	_, err := svc.Update(context.Background(), testCaller, "plugin-1", req)
	if !errors.Is(err, ErrReviewPending) {
		t.Fatalf("err = %v, want ErrReviewPending", err)
	}
	if store.update != nil {
		t.Fatal("a visibility change during a pending review was PERSISTED")
	}
	if store.review.pendingPlugin != "plugin-1" {
		t.Errorf("pending check ran against %q, want plugin-1", store.review.pendingPlugin)
	}
}

// The pending check must not fire when visibility is unchanged: an author fixing
// a typo while their request sits in the queue is the normal case, and the
// snapshot already protects the reviewer from it.
func TestContentEditIsAllowedWhileAReviewIsPending(t *testing.T) {
	store, svc := visibilityFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.review.hasPending = true
	req := validRequest()
	req.Visibility = model.PluginVisibilitySpace
	req.Publisher = "Typo fixed"
	if _, err := svc.Update(context.Background(), testCaller, "plugin-1", req); err != nil {
		t.Fatalf("a content edit during a pending review was refused: %v", err)
	}
	if store.update == nil {
		t.Fatal("nothing persisted")
	}
	if store.review.pendingCalls != 0 {
		t.Errorf("the pending check ran %d times for an unchanged visibility; it should be skipped", store.review.pendingCalls)
	}
}
