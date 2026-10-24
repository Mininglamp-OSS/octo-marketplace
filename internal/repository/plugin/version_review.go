package plugin

import (
	"context"
	"database/sql"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// approveManagedReview runs in the existing review transaction with both request
// and resource locked. The frozen release is append-only; live draft rows and
// draft relations are preserved even when the author saved again after submit.
func (r *Repo) approveManagedReview(ctx context.Context, tx *sql.Tx, scope Scope, p ApproveReviewParams, current *model.Plugin, version string, changelog sql.NullString, manifest, pkg, keys, relationBytes []byte, manifestHash, pluginHash string) (*model.Plugin, error) {
	if current.Visibility != model.PluginVisibilitySpace {
		return nil, ErrConflict
	}
	rels, err := model.DecodeSnapshotRelations(relationBytes)
	if err != nil {
		return nil, err
	}
	live, err := liveRelationTargetSet(ctx, tx, current.ID)
	if err != nil {
		return nil, err
	}
	owner := Scope{CallerUID: current.OwnerUID, SpaceID: scope.SpaceID}
	if err = lockRelationTargets(ctx, tx, owner, current.Type, rels, live); err != nil {
		return nil, err
	}
	now := p.Now
	if now.IsZero() {
		now = r.now()
	}
	frozen := *current
	frozen.Manifest = cloneJSON(manifest)
	frozen.Package = cloneJSON(pkg)
	frozen.AttachmentKeys = cloneJSON(keys)
	frozen.ManifestHash = manifestHash
	frozen.PluginHash = pluginHash
	frozen.CurrentVersion = &version
	frozen.Name, frozen.Tags = manifestNameAndTags(manifest)
	var note *string
	if changelog.Valid {
		note = &changelog.String
	}
	id, err := snapshotVersion(ctx, tx, r.id, now, frozen, rels, note, p.ReviewerUID)
	if err != nil {
		return nil, err
	}
	if err = r.recordRelease(ctx, tx, &frozen, id, version, p.ReviewerUID, now); err != nil {
		return nil, err
	}
	// snapshotVersion supplies an immutable row, but its editable-head update is
	// undone in this transaction: approval never replaces a subsequently saved draft.
	if _, err = tx.ExecContext(ctx, `UPDATE plugins SET current_version_id=?,current_version=?,listing_state='published',updated_at=? WHERE plugin_id=? AND space_id=? AND deleted_at IS NULL`, current.CurrentVersionID, current.CurrentVersion, now, current.ID, scope.SpaceID); err != nil {
		return nil, err
	}
	if err = ensureVisibleDefaultPlacement(ctx, tx, r.id, now, current.ID, current.CategoryID); err != nil {
		return nil, err
	}
	// A first listing must promote the FROZEN topology's embedded children, not
	// the live editable graph. The author may have saved a newer draft after
	// submitting; approval deliberately keeps that draft (content, pointer and
	// relations), so rewriting the live edges from the older submission would make
	// CurrentDetail report the newer version with the older relations. The release
	// row already carries the frozen topology the market and install read, so only
	// the embedded children of the frozen targets need to be listed alongside it.
	if current.ListingState != model.PluginListingStatePublished {
		if err = promoteFrozenEmbeddedChildren(ctx, tx, current.ID, current.Type, rels, scope.SpaceID, now); err != nil {
			return nil, err
		}
	}
	source := p.DecisionSource
	if source == "" {
		source = model.ReviewDecisionSourceWeb
	}
	res, err := tx.ExecContext(ctx, `UPDATE plugin_review_requests SET status='approved',reviewer_uid=?,reviewer_name=?,decision_source=?,reviewed_at=?,updated_at=? WHERE review_id=? AND status='pending'`, p.ReviewerUID, p.ReviewerName, string(source), now, now, p.ReviewID)
	if err != nil {
		return nil, err
	}
	if err = mustAffect(res); err != nil {
		return nil, err
	}
	m := Mutation{OperatorID: p.ReviewerUID, OperatorName: p.ReviewerName, RequestID: p.RequestID}
	if err = insertAudit(ctx, tx, r.id(), now, frozen, "review_approve", m, current.PluginHash, pluginHash); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	frozen.CurrentVersionID = &id
	frozen.ListingState = model.PluginListingStatePublished
	return &frozen, nil
}

// managedLabels records only actual releases, plus approved legacy review
// evidence. A live editable label is never itself evidence of publication.
func managedLabels(ctx context.Context, tx *sql.Tx, pluginID string) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version_label FROM plugin_version_releases WHERE plugin_id=? UNION SELECT version FROM plugin_review_requests WHERE plugin_id=? AND status='approved' AND deleted_at IS NULL`, pluginID, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			return nil, err
		}
		out[s] = struct{}{}
	}
	return out, rows.Err()
}
