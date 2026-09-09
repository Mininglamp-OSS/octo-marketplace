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

// frozenKey is the storage key a zip-submitted upgrade spills at submit time: it
// lives on the request's frozen sidecar and nowhere else.
const frozenKey = "plugins/space-a/attachments/submitted-only.md"

func snapshotWithKeys(keys string) pluginrepo.FrozenSnapshot {
	snap := snapshotOf(`{"plugin_name":"V2"}`, `{"attachments":[]}`, nil)
	snap.AttachmentKeys = json.RawMessage(keys)
	return snap
}

func reviewRow(t *testing.T, database *sql.DB, reviewID string) (status, reviewer, reason string, settled bool) {
	t.Helper()
	var rv, rs sql.NullString
	var reviewedAt sql.NullTime
	if err := database.QueryRow(
		`SELECT status, reviewer_uid, reason, reviewed_at FROM plugin_review_requests WHERE review_id=?`,
		reviewID).Scan(&status, &rv, &rs, &reviewedAt); err != nil {
		t.Fatalf("load review %s: %v", reviewID, err)
	}
	return status, rv.String, rs.String, reviewedAt.Valid
}

// TestDeleteCancelsThePendingReviewRequest pins the cascade that keeps a review
// request from outliving its plugin in a state nobody can leave.
//
// Deleting the plugin used to leave the request `pending` forever. It is not just
// untidy — it is unreachable in BOTH directions. Every read (ListReviewRequests,
// GetReviewRequest, LoadReviewSnapshot) carries `p.deleted_at IS NULL`, so neither
// the applicant nor a reviewer can see the row at all; and every decision path
// loads the plugin through getReviewedPluginForUpdate, which refuses a deleted
// one, so the applicant's own CancelReview answers `plugin not found`. Relaxing
// any single one of those would be dead code, because the request is
// undiscoverable before it is unsettleable. Only the deleting transaction can
// reach it.
//
// The frozen sidecar is asserted intact on purpose: delete deliberately does NOT
// garbage-collect the submission's storage objects, because a soft delete keeps
// the plugin's own row, versions and live sidecar and nothing in this service ever
// deletes a plugin's objects. Collecting only the submission's spill would be the
// one irreversible side effect of an otherwise fully-preserved teardown.
func TestDeleteCancelsThePendingReviewRequest(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()

	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "published", currentVersion: "1.0.0"})
	// A second plugin with its own open request, to prove the cascade is scoped to
	// the deleted plugin rather than sweeping the applicant's whole queue.
	seed(t, database, seedPlugin{id: "plugin-2", visibility: "space", listingState: "published", currentVersion: "1.0.0"})

	doomed := newRequest("plugin-1", "2.0.0")
	if err := repo.InsertReviewRequest(ctx, owner, doomed, snapshotWithKeys(`{"SKILL.md":"`+frozenKey+`"}`)); err != nil {
		t.Fatal(err)
	}
	bystander := newRequest("plugin-2", "2.0.0")
	if err := repo.InsertReviewRequest(ctx, owner, bystander, snapshotOf(`{"plugin_name":"V2"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}

	// Repo.Delete now restates the listed-plugin gate against the FOR UPDATE
	// row (symmetric with Repo.Update's EnforceListingGate), so a non-admin
	// owner-scope call refuses published+space at the repo layer too. AdminDelete
	// must still be able to cascade-cancel pending requests on a listed row it
	// is removing (system admins are the takedown actor), so drive this test
	// through an admin scope — that is also the production shape for removing
	// an abusive listed plugin.
	admin := pluginrepo.Scope{CallerUID: "admin-1", SpaceID: "space-a", Admin: true}
	if err := repo.Delete(ctx, admin, "plugin-1", "admin-1", "admin-1", "req-1", nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	status, reviewer, reason, settled := reviewRow(t, database, doomed.ID)
	if status != string(model.ReviewStatusCanceled) {
		t.Fatalf("request status = %q, want canceled; it can never be settled by anyone now", status)
	}
	if !settled {
		t.Error("reviewed_at is NULL on a settled request")
	}
	if reviewer != "admin-1" {
		t.Errorf("reviewer_uid = %q, want the operator who caused the cancellation", reviewer)
	}
	if reason == "" {
		t.Error("no reason recorded; the row does not say why it was cancelled")
	}

	// The other plugin's request is untouched.
	if status, _, _, _ := reviewRow(t, database, bystander.ID); status != string(model.ReviewStatusPending) {
		t.Errorf("bystander request status = %q, want pending; the cascade swept beyond the deleted plugin", status)
	}

	// The submission's frozen sidecar survives: nothing collected its objects, and
	// the key is still recoverable from the row.
	var keys sql.NullString
	if err := database.QueryRow(`SELECT attachment_keys_json FROM plugin_review_requests WHERE review_id=?`, doomed.ID).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if !keys.Valid || keys.String == "" {
		t.Error("the frozen attachment sidecar was cleared; delete must leave the submission's objects addressable")
	}
}

// A request somebody already decided keeps the decision it actually got: the
// cascade is a CAS on `pending`, not a blind write over every request the plugin
// ever had. Overwriting a rejection with "canceled" would erase the reviewer's
// identity and reason from the only record of it.
func TestDeleteDoesNotOverwriteAlreadyDecidedRequests(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()

	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	decided := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, owner, decided, snapshotOf(`{"plugin_name":"V1"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.RejectReview(ctx, reviewerScope(), pluginrepo.RejectReviewParams{
		ReviewID: decided.ID, ReviewerUID: "admin-1", ReviewerName: "Adam", Reason: "not yet",
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.Delete(ctx, owner, "plugin-1", "user-1", "Alice", "req-1", nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	status, reviewer, reason, _ := reviewRow(t, database, decided.ID)
	if status != string(model.ReviewStatusRejected) {
		t.Errorf("status = %q, want rejected; the delete cascade overwrote a settled decision", status)
	}
	if reviewer != "admin-1" || reason != "not yet" {
		t.Errorf("reviewer=%q reason=%q; the reviewer's decision was overwritten", reviewer, reason)
	}
}

// TestDeleteGraphCancelsPendingReviewsAcrossTheSubtree extends the cascade to the
// container teardown. DeleteGraph soft-deletes the top AND every embedded
// descendant in one transaction, so every one of those rows has to carry the
// cascade or the defect just moves down a level.
//
// The child's request models the only way a real one gets there: SubmitReview
// refuses an is_embedded row, so the request has to predate the row becoming a
// container's child. The cascade lives at the soft-delete itself rather than at
// DeleteGraph's two call sites precisely so this case cannot be reasoned away.
func TestDeleteGraphCancelsPendingReviewsAcrossTheSubtree(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()

	seed(t, database, seedPlugin{id: "expert-1", typ: "expert", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	seed(t, database, seedPlugin{id: "skill-1", typ: "skill", visibility: "space", listingState: "draft", currentVersion: "0.9.0"})
	seedRelation(t, database, "rel-1", "expert-1", "skill-1", "expert_skill")

	topReq := newRequest("expert-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, owner, topReq, snapshotOf(`{"plugin_name":"Top"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	childReq := newRequest("skill-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, owner, childReq, snapshotOf(`{"plugin_name":"Child"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	// The skill becomes the expert's bundled child after its submission was filed.
	if _, err := database.Exec(`UPDATE plugins SET is_embedded=1 WHERE plugin_id='skill-1'`); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteGraph(ctx, owner, "expert-1", "user-1", "Alice", "req-1", nil); err != nil {
		t.Fatalf("DeleteGraph: %v", err)
	}

	// Both rows really were soft-deleted, so both requests really are unreachable.
	for _, id := range []string{"expert-1", "skill-1"} {
		var deleted sql.NullTime
		if err := database.QueryRow(`SELECT deleted_at FROM plugins WHERE plugin_id=?`, id).Scan(&deleted); err != nil {
			t.Fatal(err)
		}
		if !deleted.Valid {
			t.Fatalf("%s was not soft-deleted; the fixture does not exercise the cascade", id)
		}
	}
	for name, id := range map[string]string{"top": topReq.ID, "embedded child": childReq.ID} {
		if status, _, _, _ := reviewRow(t, database, id); status != string(model.ReviewStatusCanceled) {
			t.Errorf("%s request status = %q, want canceled; it outlived its plugin", name, status)
		}
	}
}

// TestDeleteRefusesAPluginApprovedMidFlight pins the under-lock twin of the
// Service.Delete listing gate. Service.Delete reads listing_state/visibility from
// an UNLOCKED GetWithRelations; Repo.Delete re-derives the (published AND space)
// conjunction against the FOR UPDATE-locked row so an ApproveReview that commits
// space+published between the unlocked read and the lock cannot let the author
// soft-delete a plugin the org just listed.
func TestDeleteRefusesAPluginApprovedMidFlight(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()

	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "1.0.0"})
	req := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, owner, req, snapshotOf(`{"plugin_name":"Frozen"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatalf("InsertReviewRequest: %v", err)
	}
	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	}); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}

	err := repo.Delete(ctx, owner, "plugin-1", "user-1", "Alice", "req-1", nil)
	if !errors.Is(err, pluginrepo.ErrListedRequiresReview) {
		t.Fatalf("Delete of a just-approved plugin = %v, want ErrListedRequiresReview", err)
	}
	var deleted sql.NullTime
	if err := database.QueryRow(`SELECT deleted_at FROM plugins WHERE plugin_id='plugin-1'`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.Valid {
		t.Fatalf("plugin was soft-deleted despite the refused Delete: deleted_at=%v", deleted.Time)
	}
}

// TestDeleteGraphRefusesAContainerApprovedMidFlight covers the same race on
// the container-delete path. Graph roots (experts, expert_teams) almost never
// carry incoming live relations, so rejectLiveIncomingRelations cannot protect
// them; the under-lock listing gate is what must.
func TestDeleteGraphRefusesAContainerApprovedMidFlight(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	owner := tenantScope()

	seed(t, database, seedPlugin{id: "expert-1", typ: "expert", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "1.0.0"})
	req := newRequest("expert-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, owner, req, snapshotOf(`{"plugin_name":"Frozen expert"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatalf("InsertReviewRequest: %v", err)
	}
	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	}); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}

	err := repo.DeleteGraph(ctx, owner, "expert-1", "user-1", "Alice", "req-1", nil)
	if !errors.Is(err, pluginrepo.ErrListedRequiresReview) {
		t.Fatalf("DeleteGraph of a just-approved container = %v, want ErrListedRequiresReview", err)
	}
	var deleted sql.NullTime
	if err := database.QueryRow(`SELECT deleted_at FROM plugins WHERE plugin_id='expert-1'`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.Valid {
		t.Fatalf("container was soft-deleted despite the refused DeleteGraph: deleted_at=%v", deleted.Time)
	}
}
