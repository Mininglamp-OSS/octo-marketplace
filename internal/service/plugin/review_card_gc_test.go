package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// rejectSidecarStore hands the reject path a real pair of sidecars.
//
// The shared reviewFake returns (nil, nil) from RejectReview, which makes the
// object GC a no-op no matter whether the caller wires it up — so a test built on
// it cannot tell "collected nothing" from "never called". Wrapping rather than
// widening the shared fake keeps this file self-contained.
//
// It returns the sidecars even alongside an error. The real repository happens to
// return nils on a lost CAS today, but it reads the frozen sidecar BEFORE the CAS
// and the signature permits handing it back; the caller must decide from the
// error, not from the emptiness of what it was given.
type rejectSidecarStore struct {
	*fakeStore
	frozen   json.RawMessage
	retained map[string]struct{}
}

func (s *rejectSidecarStore) RejectReview(ctx context.Context, sc pluginrepo.Scope, p pluginrepo.RejectReviewParams) (json.RawMessage, map[string]struct{}, error) {
	_, _, err := s.fakeStore.RejectReview(ctx, sc, p)
	return s.frozen, s.retained, err
}

const (
	orphanedKey = "plugins/space-a/attachments/rejected-only.md"
	sharedKey   = "plugins/space-a/attachments/still-referenced.md"
)

// TestIMDenyCollectsTheSubmissionsOrphanedObjects closes the gap between the two
// reject doors.
//
// A web reject GCs the objects the refused submission spilled at freeze time; an
// IM approval-card deny used to discard both sidecars and collect nothing, so the
// SAME decision leaked or did not leak depending on which button the admin
// happened to press. The comment that justified it claimed GC would cost an extra
// round trip, which was simply false: RejectReview returns both sidecars out of
// its own transaction.
//
// The retained key must survive — that is the half a naive "delete everything the
// submission mentioned" gets wrong, and object-storage deletes do not come back.
func TestIMDenyCollectsTheSubmissionsOrphanedObjects(t *testing.T) {
	store, _ := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
	}
	repo := &rejectSidecarStore{
		fakeStore: store,
		frozen:    json.RawMessage(`{"SKILL.md":"` + orphanedKey + `","docs/shared.md":"` + sharedKey + `"}`),
		// The live row (and an older approved version) still points at sharedKey.
		retained: map[string]struct{}{sharedKey: {}},
	}
	objects := &importStorage{objects: map[string][]byte{
		orphanedKey: []byte("rejected body"),
		sharedKey:   []byte("live body"),
	}}
	svc := New(repo, objects).WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleAdmin)}, nil)

	out, err := svc.DecideReviewFromCard(context.Background(), "42", "admin-1", "deny", "review-1")
	if err != nil {
		t.Fatalf("DecideReviewFromCard: %v", err)
	}
	if out.Disposition != cardDispositionApplied || out.State != cardStateDenied {
		t.Fatalf("result = %+v, want applied/denied", out)
	}
	if len(objects.deletes) != 1 || objects.deletes[0] != orphanedKey {
		t.Fatalf("deleted objects = %v, want exactly [%s]; an IM deny leaks every object its submission spilled", objects.deletes, orphanedKey)
	}
	if _, ok := objects.objects[sharedKey]; !ok {
		t.Error("the GC deleted a key the live plugin still references")
	}
}

// A deny that LOST the CAS race changed nothing, so it must not collect anything
// either: the winner's outcome owns those objects now.
func TestIMDenyThatLosesTheRaceCollectsNothing(t *testing.T) {
	store, _ := reviewFixture(t)
	store.review.anySpaceReq = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusPending, ApplicantUID: "user-1", PluginName: "Demo",
	}
	store.review.anySpaceSecond = &model.PluginReviewRequest{
		ID: "review-1", PluginID: "plugin-1", SpaceID: "space-a",
		Status: model.ReviewStatusApproved, ApplicantUID: "user-1", PluginName: "Demo",
	}
	store.review.rejectErr = pluginrepo.ErrConflict
	repo := &rejectSidecarStore{
		fakeStore: store,
		frozen:    json.RawMessage(`{"SKILL.md":"` + orphanedKey + `"}`),
	}
	objects := &importStorage{objects: map[string][]byte{orphanedKey: []byte("body")}}
	svc := New(repo, objects).WithNotify(&fakeNotifier{enabled: true, role: roleOf(SpaceRoleAdmin)}, nil)

	out, err := svc.DecideReviewFromCard(context.Background(), "43", "admin-2", "deny", "review-1")
	if err != nil {
		t.Fatalf("DecideReviewFromCard: %v", err)
	}
	if out.Disposition != cardDispositionConflict {
		t.Fatalf("disposition = %q, want conflict", out.Disposition)
	}
	if len(objects.deletes) != 0 {
		t.Fatalf("a lost race deleted %v; the winner's decision owns those objects", objects.deletes)
	}
}
