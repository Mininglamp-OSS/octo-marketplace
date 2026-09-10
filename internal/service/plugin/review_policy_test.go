package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func TestUpdateReviewPolicyRequiresSpaceReviewerAndIsSharedBySpace(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilityPrivate, model.PluginListingStateDraft)
	caller := testCaller
	caller.SpaceRole = SpaceRoleMember
	if _, err := svc.UpdateReviewPolicy(context.Background(), caller, false); !errors.Is(err, ErrReviewPolicyForbidden) {
		t.Fatalf("err=%v, want ErrReviewPolicyForbidden", err)
	}
	if store.reviewPolicySet != nil {
		t.Fatal("member changed reviewer-only policy")
	}

	caller.SpaceRole = SpaceRoleAdmin
	policy, err := svc.UpdateReviewPolicy(context.Background(), caller, false)
	if err != nil {
		t.Fatal(err)
	}
	if policy.IsAutoApproveEnabled || store.reviewPolicySet == nil || *store.reviewPolicySet {
		t.Fatal("admin update was not persisted")
	}

	owner := caller
	owner.UID = "owner-1"
	owner.SpaceRole = SpaceRoleOwner
	shared, err := svc.GetReviewPolicy(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if shared.IsAutoApproveEnabled {
		t.Fatal("owner did not observe the Space policy updated by admin")
	}
}

func TestSubmitReviewAutoApprovesWithoutReviewerRole(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.reviewPolicy = model.PluginReviewPolicy{IsAutoApproveEnabled: true}
	caller := testCaller
	caller.SpaceRole = SpaceRoleMember

	review, err := svc.SubmitReview(context.Background(), caller, ReviewSubmitParams{
		PluginID: "plugin-1", Version: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Status != model.ReviewStatusApproved {
		t.Fatalf("status=%q, want approved", review.Status)
	}
	if got := store.review.approveParams.DecisionSource; got != model.ReviewDecisionSourcePolicy {
		t.Fatalf("decision_source=%q, want policy", got)
	}
}

func TestSubmitReviewAutoApproveDoesNotDependOnPostInsertReadback(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.reviewPolicy = model.PluginReviewPolicy{IsAutoApproveEnabled: true}
	store.review.getErr = errors.New("read replica unavailable")
	version := "1.0.0"
	store.review.approved = &model.Plugin{
		ID:             "plugin-1",
		CurrentVersion: &version,
		ListingState:   model.PluginListingStatePublished,
	}

	review, err := svc.SubmitReview(context.Background(), testCaller, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"})
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if review.ID != "review-1" || review.Status != model.ReviewStatusApproved {
		t.Fatalf("review=%#v, want committed approval without readback", review)
	}
	if review.PluginListingState != model.PluginListingStatePublished {
		t.Fatalf("listing_state=%q, want published committed projection", review.PluginListingState)
	}
	if review.CurrentVersion == nil || *review.CurrentVersion != "1.0.0" {
		t.Fatalf("current_version=%v, want approved version 1.0.0", review.CurrentVersion)
	}
	if store.review.approveParams.ReviewID != "review-1" || store.review.cancelCalls != 0 {
		t.Fatalf("approve=%#v cancelCalls=%d", store.review.approveParams, store.review.cancelCalls)
	}
}

func TestPublishAutoApproveDoesNotDependOnPostInsertReadback(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.reviewPolicy = model.PluginReviewPolicy{IsAutoApproveEnabled: true}
	store.review.getErr = errors.New("read replica unavailable")
	published := *store.plugins["plugin-1"]
	published.ListingState = model.PluginListingStatePublished
	store.review.approved = &published

	result, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1"})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if result.Review != nil || result.Plugin.Plugin.ListingState != model.PluginListingStatePublished {
		t.Fatalf("result=%#v, want committed publication without readback", result)
	}
	if store.review.approveParams.ReviewID != "review-1" || store.review.cancelCalls != 0 {
		t.Fatalf("approve=%#v cancelCalls=%d", store.review.approveParams, store.review.cancelCalls)
	}
}

func TestSubmitReviewAutoApproveFailureCancelsAndAllowsRetry(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.reviewPolicy = model.PluginReviewPolicy{IsAutoApproveEnabled: true}
	store.review.approveErr = pluginrepo.ErrDeadlock

	_, err := svc.SubmitReview(context.Background(), testCaller, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"})
	if !errors.Is(err, ErrDeadlock) {
		t.Fatalf("err=%v, want ErrDeadlock", err)
	}
	if store.review.cancelCalls != 1 || store.review.cancelReviewID != "review-1" || store.review.cancelUID != testCaller.UID {
		t.Fatalf("cancel = calls:%d review:%q uid:%q", store.review.cancelCalls, store.review.cancelReviewID, store.review.cancelUID)
	}
	if store.review.stored.Status != model.ReviewStatusCanceled || store.review.hasPending {
		t.Fatalf("compensation left review pending: status=%q pending=%v", store.review.stored.Status, store.review.hasPending)
	}

	store.review.approveErr = nil
	store.review.stored.Status = model.ReviewStatusPending
	review, err := svc.SubmitReview(context.Background(), testCaller, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if review.Status != model.ReviewStatusApproved {
		t.Fatalf("retry status=%q, want approved", review.Status)
	}
}

func TestPublishAutoApproveFailureCancelsAndAllowsRetry(t *testing.T) {
	store, svc := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.reviewPolicy = model.PluginReviewPolicy{IsAutoApproveEnabled: true}
	store.review.approveErr = pluginrepo.ErrDeadlock

	if _, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1"}); !errors.Is(err, ErrDeadlock) {
		t.Fatalf("err=%v, want ErrDeadlock", err)
	}
	if store.review.cancelCalls != 1 || store.review.stored.Status != model.ReviewStatusCanceled || store.review.hasPending {
		t.Fatalf("compensation did not clear publish review: calls=%d status=%q pending=%v", store.review.cancelCalls, store.review.stored.Status, store.review.hasPending)
	}

	store.review.approveErr = nil
	store.review.stored.Status = model.ReviewStatusPending
	published := *store.plugins["plugin-1"]
	published.ListingState = model.PluginListingStatePublished
	store.review.approved = &published
	result, err := svc.Publish(context.Background(), testCaller, PublishParams{PluginID: "plugin-1"})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if result.Review != nil || result.Plugin.Plugin.ListingState != model.PluginListingStatePublished {
		t.Fatalf("retry result=%#v, want published", result)
	}
}

func TestAutoApproveCompensationCleansOnlyOrphanedObjects(t *testing.T) {
	store, _ := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.reviewPolicy = model.PluginReviewPolicy{IsAutoApproveEnabled: true}
	store.review.approveErr = pluginrepo.ErrDeadlock
	store.review.cancelFrozen = json.RawMessage(`{"only":"` + orphanedKey + `","shared":"` + sharedKey + `"}`)
	store.review.cancelRetained = map[string]struct{}{sharedKey: {}}
	objects := &importStorage{objects: map[string][]byte{orphanedKey: []byte("orphan"), sharedKey: []byte("shared")}}
	svc := New(store, objects)

	_, err := svc.SubmitReview(context.Background(), testCaller, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"})
	if !errors.Is(err, ErrDeadlock) {
		t.Fatalf("err=%v, want ErrDeadlock", err)
	}
	if len(objects.deletes) != 1 || objects.deletes[0] != orphanedKey {
		t.Fatalf("deleted=%v, want only %q", objects.deletes, orphanedKey)
	}
	if _, ok := objects.objects[sharedKey]; !ok {
		t.Fatal("compensation deleted an object retained by the live row or version history")
	}
}

func TestAutoApproveCompensationCleansObjectsBeforeReleasingParseTask(t *testing.T) {
	store, _ := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.reviewPolicy = model.PluginReviewPolicy{IsAutoApproveEnabled: true}
	store.review.approveErr = pluginrepo.ErrDeadlock
	store.review.cancelFrozen = json.RawMessage(`{"orphan":"` + orphanedKey + `"}`)
	var events []string
	objects := &importStorage{
		objects: map[string][]byte{orphanedKey: []byte("orphan")},
		events:  &events,
	}
	parseTasks := &fakeParseTasks{events: &events}
	svc := New(store, objects).WithParseTasks(parseTasks)

	_, err := svc.autoApproveReview(context.Background(), testCaller, store.review.stored, "task-1")
	if !errors.Is(err, ErrDeadlock) {
		t.Fatalf("err=%v, want ErrDeadlock", err)
	}
	want := []string{"delete:" + orphanedKey, "release:task-1"}
	if len(events) != len(want) || events[0] != want[0] || events[1] != want[1] {
		t.Fatalf("events=%v, want %v", events, want)
	}
}

func TestAutoApproveCompensationFailureDispatchesFallbackCard(t *testing.T) {
	store, _ := listingFixture(t, model.PluginVisibilitySpace, model.PluginListingStateDraft)
	store.reviewPolicy = model.PluginReviewPolicy{IsAutoApproveEnabled: true}
	store.review.approveErr = pluginrepo.ErrDeadlock
	store.review.cancelErr = errors.New("database unavailable")
	notifier := &fakeNotifier{enabled: true}
	var hooks []string
	svc := fixedService(store).WithNotify(notifier, syncBestEffort(&hooks))

	_, err := svc.SubmitReview(context.Background(), testCaller, ReviewSubmitParams{PluginID: "plugin-1", Version: "1.0.0"})
	if !errors.Is(err, ErrDeadlock) {
		t.Fatalf("err=%v, want original ErrDeadlock", err)
	}
	if len(hooks) != 1 || len(notifier.notifyIn) != 1 {
		t.Fatalf("fallback dispatch hooks=%v notifications=%d", hooks, len(notifier.notifyIn))
	}
	if store.review.stored.Status != model.ReviewStatusPending {
		t.Fatalf("failed compensation unexpectedly changed status to %q", store.review.stored.Status)
	}
}
