package plugin

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// mustChangeState turns a zero-row state CAS into ErrConflict.
//
// Deliberately NOT mustAffect, which reports ErrNotFound. These UPDATEs run after
// a successful FOR UPDATE read, so the row provably exists and zero rows can only
// mean the listing state was not the one the CAS required — a concurrent writer
// won, or the caller's view was stale. Reporting that as "not found" would 404 a
// plugin the caller is looking at, and would tell an admin whose delist lost a
// race that the plugin never existed.
func mustChangeState(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	return nil
}

// PublishParams carries one owner-driven publish of an unlisted plugin.
type PublishParams struct {
	PluginID     string
	OperatorID   string
	OperatorName string
	RequestID    string
}

// DelistParams carries one Space-admin takedown of a listed plugin.
type DelistParams struct {
	PluginID     string
	OperatorID   string
	OperatorName string
	RequestID    string
	// Reason is optional and is recorded on the audit row and on any pending
	// review request this takedown cancels.
	Reason string
}

// PublishPlugin lists a plugin the caller owns, without review.
//
// This is the no-review half of the publish decision: only a plugin whose
// declared visibility is private reaches here, because org-visible intent routes
// through SubmitReview instead. The service enforces that; the repository's job is
// the transaction.
//
// It deliberately does NOT mint a version. Every save already snapshots one
// (plugin_versions is per-save history), and plugin_versions.version is a
// per-plugin auto-increment counter rather than the author's label, so minting
// here would perturb that counter for no new content.
func (r *Repo) PublishPlugin(ctx context.Context, scope Scope, p PublishParams) (_ *model.Plugin, err error) {
	defer func() { err = classifyDeadlock(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Owner-scoped: publishing is an owner action, unlike the reviewer
	// transactions which lock by Space.
	current, err := getOwnedForUpdate(ctx, tx, scope, p.PluginID)
	if err != nil {
		return nil, err
	}
	now := r.now()

	// The service already refused anything but `private`, but it decided that from
	// an UNLOCKED read taken several round trips earlier. Between that read and
	// this transaction the owner can raise the row to `space` through an ordinary
	// upsert — legal on a draft — and this UPDATE would then stamp `published`
	// onto it, putting unreviewed content on the org marketplace with no review
	// request ever created. Re-deriving from the locked row closes the window.
	// Re-deriving from the locked row closes the window. The companion mirror —
	// SubmitReview landing against a row that got lowered to `private` mid-flight
	// — is closed inside InsertReviewRequest, which re-derives visibility under
	// its own FOR UPDATE and refuses drafts that have lost their space intent;
	// it explicitly admits the stranded `private+published` shape so ApproveReview
	// can recover rows that PublishPlugin's lost race left behind.
	if current.Visibility != model.PluginVisibilityPrivate {
		return nil, ErrConflict
	}

	// The service also refused a plugin with a pending review from an UNLOCKED
	// read taken several round trips earlier. Between that read and this
	// transaction the owner can fire a concurrent SubmitReview: its request commits
	// a first-listing row (kind derived from the still-draft plugin) while this
	// publish sees "no pending" and stamps `published` onto the private row. The
	// plugin lands private+published+pending — a state where the reviewer's later
	// approval takes the content-only branch and never lists it, stranding an
	// approved-but-invisible row exactly like the failures earlier rounds closed.
	// Re-checking the pending request under the plugin row's lock closes the
	// window; the request row was inserted inside its own transaction, so once we
	// hold the plugin lock a committed request is visible and an uncommitted one
	// cannot race past this read. ErrReviewPending mirrors the service's own error
	// for the case it already handles.
	var pendingID string
	if err := tx.QueryRowContext(ctx,
		`SELECT review_id FROM plugin_review_requests
		  WHERE plugin_id=? AND status='pending' AND deleted_at IS NULL LIMIT 1`,
		p.PluginID).Scan(&pendingID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, wrapped("check pending review", err)
	}
	if pendingID != "" {
		return nil, ErrReviewPending
	}

	// State CAS rather than a blind write, so a double-click loses with
	// ErrConflict instead of appending a second audit row for the same event.
	// `visibility` is in the predicate too: the check above reads the locked row,
	// and carrying it into the WHERE keeps the guarantee if this ever runs without
	// that read in front of it.
	res, err := tx.ExecContext(ctx,
		`UPDATE plugins SET listing_state=?,updated_at=?
		  WHERE plugin_id=? AND owner_uid=? AND space_id=? AND visibility=?
		    AND listing_state<>? AND deleted_at IS NULL`,
		string(model.PluginListingStatePublished), now,
		p.PluginID, scope.CallerUID, scope.SpaceID, string(model.PluginVisibilityPrivate),
		string(model.PluginListingStatePublished))
	if err != nil {
		return nil, wrapped("publish plugin", err)
	}
	if err := mustChangeState(res); err != nil {
		return nil, err
	}

	// Listing promises "this is in the market now", and the market list needs BOTH
	// the visibility predicate and a visible default placement. Flipping
	// listing_state alone lists nothing for a row whose placement is missing
	// (pre-auto-placement legacy) or hidden, and the author cannot repair it
	// afterwards. Same self-heal ApproveReview performs, and forward-only: it never
	// sets visible=0, so it cannot be used to hide anything.
	if err := ensureVisibleDefaultPlacement(ctx, tx, r.id, now, p.PluginID, current.CategoryID); err != nil {
		return nil, err
	}

	m := Mutation{OperatorID: p.OperatorID, OperatorName: p.OperatorName, RequestID: p.RequestID}
	if err := insertAudit(ctx, tx, r.id(), now, *current, "publish", m, current.PluginHash, current.PluginHash); err != nil {
		return nil, err
	}

	refreshed, err := getOwnedForUpdate(ctx, tx, scope, p.PluginID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return refreshed, nil
}

// DelistPlugin takes a listed plugin out of the marketplace. Space admins only;
// the service checks the role, and the row is locked by SPACE rather than by
// owner because the actor is by definition not the author.
//
// It applies only to a STANDALONE, ORG-VISIBLE row: the two guards below refuse
// an embedded child and anything whose declared visibility is not `space`, each
// with ErrNotFound.
//
// The row stays editable and re-publishable afterwards — delisting is a takedown,
// not a deletion — and its current_version label stays spent so a republish
// cannot reuse a label the org already saw.
func (r *Repo) DelistPlugin(ctx context.Context, scope Scope, p DelistParams) (_ *model.Plugin, err error) {
	defer func() { err = classifyDeadlock(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	current, err := getReviewedPluginForUpdate(ctx, tx, scope.SpaceID, p.PluginID)
	if err != nil {
		return nil, err
	}

	// An embedded child (a bundled skill, a squad member) is listed and un-listed
	// by its container, never on its own: ApproveReview promotes the whole graph in
	// one transaction. Taking a single child down leaves the container published
	// while a member it declares is hidden, and every non-owner install of the
	// PARENT then fails with ErrDependencyHidden — a takedown of one row that
	// silently breaks another. Publish refuses embedded rows for the mirror reason.
	if current.IsEmbedded {
		return nil, ErrNotFound
	}
	// Delist is moderation of ORG content, and `space` is what makes a row org
	// content. A private row is not readable by this admin at all — visibilitySQL
	// admits a private plugin only to its owner — so delisting one is both outside
	// the takedown rationale (nobody but the author can read it, so there is
	// nothing to take down) and an existence oracle: an admin who cannot GET the
	// row would learn that it exists by successfully delisting it, and could
	// interfere with a colleague's private plugin at will. ErrNotFound keeps the
	// answer identical to the read the same admin is allowed to make.
	//
	// The consequence, stated deliberately: a PUBLISHED PRIVATE plugin has no
	// takedown path at all, since self-delisting was removed from the write path.
	// It needs none — published+private means "listed to its owner alone" — and the
	// owner can still drop it back to a draft by widening the visibility, which
	// un-lists it (see the ResetListingToDraft branch in the service's Update).
	if current.Visibility != model.PluginVisibilitySpace {
		return nil, ErrNotFound
	}
	now := r.now()

	// Both guarantees above are re-stated in the CAS predicate, exactly as publish
	// carries its visibility check into the UPDATE: the checks read the locked row,
	// and the predicate keeps them if this ever runs without that read in front.
	res, err := tx.ExecContext(ctx,
		`UPDATE plugins SET listing_state=?,updated_at=?
		  WHERE plugin_id=? AND space_id=? AND listing_state=? AND deleted_at IS NULL
		    AND visibility=? AND is_embedded=0`,
		string(model.PluginListingStateDelisted), now,
		p.PluginID, scope.SpaceID, string(model.PluginListingStatePublished),
		string(model.PluginVisibilitySpace))
	if err != nil {
		return nil, wrapped("delist plugin", err)
	}
	if err := mustChangeState(res); err != nil {
		return nil, err
	}

	// Take the whole container graph down with the top row. A delisted parent
	// whose embedded children stay published leaves their full content org-readable
	// and installable behind the takedown — see demoteEmbeddedChildren. The
	// children keep their `space` visibility so a later re-approve re-promotes them.
	if err := demoteEmbeddedChildren(ctx, tx, p.PluginID, current.Type, scope.SpaceID, now); err != nil {
		return nil, err
	}

	// A pending request on a plugin that just left the market has nothing left to
	// decide, and leaving it open would let an approval silently relist the plugin
	// behind the admin's back. Canceling also releases the single-pending slot (the
	// generated pending_plugin_id column drops out of the unique index), so the
	// author can resubmit after editing.
	reason := p.Reason
	if reason == "" {
		reason = reasonCanceledOnDelist
	}
	if err := cancelPendingReviewFor(ctx, tx, now, p.PluginID, p.OperatorID, p.OperatorName, reason); err != nil {
		return nil, err
	}

	// Placements are deliberately untouched. Hiding the placement would also hide
	// the plugin from its own author's 我的发布 (that list shares the placement
	// join), and listing_state already removes it from every other reader.
	//
	// Storage objects are deliberately NOT garbage-collected either, unlike reject
	// and cancel.
	//
	// This is a policy choice, not a safety one, and the distinction matters now
	// that the GC's retained set is every key the plugin still references (its live
	// sidecar plus the sidecar frozen onto every plugin_versions row — see
	// retainedAttachmentKeys). Under those semantics collecting here would not
	// delete anything the live row or an approved version still points at, so the
	// data-loss argument that used to justify the omission no longer holds.
	//
	// What holds is who is acting: reject and cancel destroy the objects of a
	// submission that its own reviewer refused or its own author withdrew. A delist
	// cancels the request as a SIDE EFFECT — the author never abandoned it, is
	// expected to edit and republish, and can still open the canceled request to
	// see what they had submitted. Hard-deleting a third party's in-flight upload on
	// their behalf, from a transaction that was asked to do something else, is not
	// worth the bounded storage it saves; object-storage deletes do not come back.
	//
	// Residual, stated plainly: the canceled submission's spilled objects are
	// leaked until the plugin's own lifecycle collects them. Bounded by one
	// submission per takedown.
	m := Mutation{OperatorID: p.OperatorID, OperatorName: p.OperatorName, RequestID: p.RequestID}
	if p.Reason != "" {
		m.Remark = strPtr("reason=" + p.Reason)
	}
	if err := insertAudit(ctx, tx, r.id(), now, *current, "delist", m, current.PluginHash, current.PluginHash); err != nil {
		return nil, err
	}

	refreshed, err := getReviewedPluginForUpdate(ctx, tx, scope.SpaceID, p.PluginID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return refreshed, nil
}

// LatestReviewForPlugin returns the review request whose status the displayed
// listing status should reflect: the open one when there is one, otherwise the
// most recently submitted. The single-pending invariant guarantees at most one
// pending row, so ordering pending first is deterministic.
//
// Scoped by Space and used for the caller's own rows; the plugin read that
// precedes it already applied the catalog predicate.
func (r *Repo) LatestReviewForPlugin(ctx context.Context, scope Scope, pluginID string) (string, model.ReviewStatus, error) {
	var id, status string
	err := r.db.QueryRowContext(ctx,
		`SELECT rr.review_id, rr.status FROM plugin_review_requests rr
		   JOIN plugins p ON p.plugin_id=rr.plugin_id
		  WHERE rr.plugin_id=? AND rr.deleted_at IS NULL AND p.space_id=? AND p.deleted_at IS NULL
		  ORDER BY (rr.status='pending') DESC, rr.submitted_at DESC, rr.review_id DESC
		  LIMIT 1`,
		pluginID, scope.SpaceID).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", wrapped("load latest review", err)
	}
	return id, model.ReviewStatus(status), nil
}
