package plugin

import (
	"context"
	"database/sql"
	"errors"

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
	// A first listing must also enter the LIVE graph: the frozen edge set becomes
	// the published topology, and an expert's bundled skills / a squad's member
	// experts are promoted alongside it. Without this the container is listed while
	// its embedded children stay private drafts, so resolveInstallDetail resolves
	// fewer visible targets than CountDeclaredRelations and every non-owner install
	// fails with ErrDependencyHidden. The same steps run for the legacy approval
	// path; a managed UPGRADE deliberately leaves the live graph alone because the
	// author may have saved newer relations after submit, and the release row holds
	// the frozen topology the market reads.
	if current.ListingState != model.PluginListingStatePublished {
		reconciled, err := reconcileFrozenRelations(ctx, tx, current.ID, rels, now)
		if err != nil {
			return nil, err
		}
		if _, err = syncRelations(ctx, tx, r.id, now, current.ID, current.OwnerUID, reconciled); err != nil {
			return nil, err
		}
		if err = promoteEmbeddedChildren(ctx, tx, current.ID, current.Type, scope.SpaceID, now); err != nil {
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

// Guard a draft restore and its provenance in the same locked update. Neither
// source version nor expected head can be supplied across resource boundaries.
func guardVersionRestore(ctx context.Context, tx *sql.Tx, p *model.Plugin, m Mutation) error {
	if m.RestoreSourceVersionID == "" {
		return nil
	}
	if p.CurrentVersionID == nil || *p.CurrentVersionID != m.ExpectedCurrentVersionID {
		return ErrConflict
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT version_id FROM plugin_versions WHERE plugin_id=? AND version_id=?`, p.ID, m.RestoreSourceVersionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
