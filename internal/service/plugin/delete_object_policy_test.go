package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

const (
	liveAttachmentKey    = "plugins/space-a/attachments/live.md"
	spilledSubmissionKey = "plugins/space-a/attachments/submitted-only.md"
)

// TestDeletingAPluginCollectsNoObjects pins the storage policy the delete cascade
// deliberately does NOT adopt.
//
// Deleting a plugin cancels its pending review request (see the cascade in
// Repo.Delete / Repo.DeleteGraph), and on who-is-acting alone that looks like the
// reject/cancel case: the author is abandoning their own submission, and cancel
// garbage-collects the objects that submission spilled. It stops short because
// delete is a SOFT delete — the row, its plugin_versions history and its live
// attachment sidecar are all kept — and nothing in this service ever deletes a
// plugin's own objects. Collecting only the submission's spill would make delete
// the single place a soft delete performs an irreversible external side effect, on
// the smallest slice of what it is knowingly leaving behind.
//
// The choice is asymmetric on purpose: every row a later sweeper would need
// survives the soft delete, so the same difference can still be computed with more
// context, whereas an object-storage delete does not come back.
//
// This test cannot be made red by reverting the cascade — it guards the opposite
// mistake. Wiring any object GC into the delete path turns it red, which is the
// signal to come back and re-argue the comment rather than quietly change it.
func TestDeletingAPluginCollectsNoObjects(t *testing.T) {
	skill := &model.Plugin{
		ID: "plugin-1", Name: "S", Type: model.PluginTypeSkill,
		OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID),
		Visibility:   model.PluginVisibilitySpace,
		ListingState: model.PluginListingStateDraft,
		Tags:         json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`),
		AttachmentKeys: json.RawMessage(`{"SKILL.md":"` + liveAttachmentKey + `"}`),
	}
	// The published+space gate lives in Service.Delete (see
	// TestDeleteRequiresDelistFirst); this test is about the storage policy on a
	// row the author is allowed to delete, so seed a draft (never listed). A
	// private-published row would also qualify; draft is the more common shape.
	store := &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": skill}}
	objects := &importStorage{objects: map[string][]byte{
		liveAttachmentKey:    []byte("live body"),
		spilledSubmissionKey: []byte("frozen submission body"),
	}}

	if err := New(store, objects).Delete(context.Background(), testCaller, "plugin-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if store.deleteID != "plugin-1" {
		t.Fatalf("delete id = %q, want plugin-1; the fixture did not reach the delete path", store.deleteID)
	}
	if len(objects.deletes) != 0 {
		t.Fatalf("delete collected %v; a soft delete must not destroy objects it is otherwise preserving", objects.deletes)
	}
	for _, key := range []string{liveAttachmentKey, spilledSubmissionKey} {
		if _, ok := objects.objects[key]; !ok {
			t.Errorf("object %q is gone", key)
		}
	}
}
