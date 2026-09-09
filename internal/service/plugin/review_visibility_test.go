package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// The review gate is listing_state, not visibility. visibility declares INTENT
// ("who should see this once it is listed") and lists nothing on its own; a tenant
// plugin stays `draft` until Publish routes it through review and an approval
// stamps `published`.
//
// That inversion is why the old tests here had to go. They asserted "a tenant
// create always lands private" and "a tenant update may never raise visibility",
// which were the gate back when `private` doubled as the draft state. Under the
// current model an author declares 仅本组织可见 on the draft itself, so both of
// those rules would now block the normal flow rather than protect anything.
//
// Every assertion below is on what WOULD BE PERSISTED, not on an HTTP status: a
// handler that 200s while writing `draft` is correct, and a handler that 400s
// while writing `published` would pass a status-only check.

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

// A fresh tenant create lands as a DRAFT and KEEPS the visibility it declared.
// The declared value surviving is the point: it is what Publish later reads to
// decide whether the plugin needs org review or lists immediately.
func TestTenantCreateLandsDraftWithTheDeclaredVisibility(t *testing.T) {
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
		if store.create.ListingState != model.PluginListingStateDraft {
			t.Errorf("visibility=%q PERSISTED listing_state %q; a tenant create must never list directly", asked, store.create.ListingState)
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

// Once a plugin is listed TO THE ORG the ordinary write path must refuse it:
// every field reachable here is org-visible, so an edit that lands would be an
// unreviewed change to what the whole Space is reading.
func TestTenantCannotEditAListedPluginDirectly(t *testing.T) {
	store, svc := visibilityFixture(t, model.PluginVisibilitySpace, model.PluginListingStatePublished)
	req := validRequest()
	req.Visibility = model.PluginVisibilitySpace
	req.Publisher = "Someone else"
	_, err := svc.Update(context.Background(), testCaller, "plugin-1", req)
	if !errors.Is(err, ErrListedRequiresReview) {
		t.Fatalf("err = %v, want ErrListedRequiresReview", err)
	}
	if store.update != nil {
		t.Fatal("an unreviewed edit of a listed plugin was PERSISTED")
	}
}

// The refusal above is keyed on (published AND space), not on published alone.
// A published PRIVATE plugin has no review channel — Publish lists it directly —
// so refusing edits there would leave it permanently uneditable, and the reason
// for the refusal does not apply: nobody else can read it.
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

// Self-delisting is GONE. Lowering visibility to private used to be how an author
// took a listed plugin down; taking a listed plugin down is now a Space-admin
// action, so this route must close rather than quietly keep working — otherwise
// "only admins can delist" is decorative.
func TestAuthorCannotSelfDelistThroughUpsert(t *testing.T) {
	store, svc := visibilityFixture(t, model.PluginVisibilitySpace, model.PluginListingStatePublished)
	req := validRequest()
	req.Visibility = model.PluginVisibilityPrivate
	_, err := svc.Update(context.Background(), testCaller, "plugin-1", req)
	if !errors.Is(err, ErrListedRequiresReview) {
		t.Fatalf("err = %v, want ErrListedRequiresReview; lowering visibility must not delist", err)
	}
	if store.update != nil {
		t.Fatalf("a self-delist was PERSISTED as visibility %q", store.update.Visibility)
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
