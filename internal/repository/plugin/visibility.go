package plugin

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type VisibilityParams struct {
	PluginID                            string
	ExpectedPublishedVersionID          string
	Visibility                          model.PluginVisibility
	OperatorID, OperatorName, RequestID string
}

type VisibilityResult struct {
	Plugin             *model.Plugin
	PublishedVersionID string
	ReviewID           string
	Pending            bool
	ReviewCreated      bool
}

// ChangeVisibility updates only effective audience, never content, labels,
// published/current pointers, relationships or release rows. The owner lock
// serializes this operation with publication, reference writes and reviews.
func (r *Repo) ChangeVisibility(ctx context.Context, scope Scope, p VisibilityParams) (_ *VisibilityResult, err error) {
	defer func() { err = classifyDeadlock(err) }()
	if !r.versionManagement {
		return nil, ErrVersionBaselineUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := getOwnedForUpdate(ctx, tx, scope, p.PluginID)
	if err != nil {
		return nil, err
	}
	if !versioned(current) || current.IsEmbedded || (current.Visibility != model.PluginVisibilityPrivate && current.Visibility != model.PluginVisibilitySpace) {
		return nil, ErrNotFound
	}
	if current.ListingState != model.PluginListingStatePublished {
		return nil, ErrConflict
	}
	var published sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=? AND space_id=?`, current.ID, scope.SpaceID).Scan(&published); err != nil {
		return nil, err
	}
	if !published.Valid {
		return nil, ErrVersionBaselineUnavailable
	}
	if published.String != p.ExpectedPublishedVersionID {
		return nil, ErrConflict
	}
	out := &VisibilityResult{Plugin: current, PublishedVersionID: published.String}
	// Same-scope retries are harmless, including a response lost after commit.
	if current.Visibility == p.Visibility {
		return out, tx.Commit()
	}
	if p.Visibility != model.PluginVisibilityPrivate && p.Visibility != model.PluginVisibilitySpace {
		return nil, ErrConflict
	}
	var pending string
	err = tx.QueryRowContext(ctx, `SELECT review_id FROM plugin_review_requests WHERE plugin_id=? AND status='pending' AND deleted_at IS NULL LIMIT 1 FOR UPDATE`, current.ID).Scan(&pending)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if pending != "" {
		var versionID, from, to string
		e := tx.QueryRowContext(ctx, `SELECT published_version_id,source_visibility,target_visibility FROM plugin_visibility_reviews WHERE review_id=?`, pending).Scan(&versionID, &from, &to)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		if e == nil && versionID == published.String && from == string(current.Visibility) && to == string(p.Visibility) {
			out.ReviewID = pending
			out.Pending = true
			return out, tx.Commit()
		}
		return nil, ErrReviewPending
	}
	if p.Visibility == model.PluginVisibilityPrivate {
		if err = r.rejectPublishedIncomingRelations(ctx, tx, current.ID); err != nil {
			return nil, err
		}
		if err = r.applyAudience(ctx, tx, scope, current, p.Visibility, p.OperatorID, p.OperatorName, p.RequestID, "visibility_change", r.now()); err != nil {
			return nil, err
		}
		current.Visibility = p.Visibility
		return out, tx.Commit()
	}
	frozen, err := loadVisibilityRelease(ctx, tx, current, published.String)
	if err != nil {
		return nil, err
	}
	frozen.Visibility = model.PluginVisibilitySpace
	if err = validateVisibilityGraph(ctx, tx, frozen); err != nil {
		return nil, err
	}
	auto := true
	err = tx.QueryRowContext(ctx, `SELECT is_auto_approve_enabled FROM plugin_review_policies WHERE space_id=? FOR UPDATE`, scope.SpaceID).Scan(&auto)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	now, id := r.now(), r.id()
	// Snapshot only the existing released content; editable draft is never reviewed.
	_, err = tx.ExecContext(ctx, `INSERT INTO plugin_review_requests (review_id,plugin_id,space_id,target_scope,status,kind,version,changelog,manifest_json,plugin_json,attachment_keys_json,relations_json,manifest_hash,plugin_hash,applicant_uid,applicant_name,submitted_at,created_at,updated_at) VALUES (?,?,?,'space','pending','visibility',?,NULL,?,?,?,?,?,?,?,?,?,?,?)`, id, current.ID, scope.SpaceID, *frozen.CurrentVersion, string(frozen.Manifest), string(frozen.Package), jsonColumn(frozen.AttachmentKeys), string(frozen.Relations), frozen.ManifestHash, frozen.PluginHash, p.OperatorID, p.OperatorName, now, now, now)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_visibility_reviews (review_id,published_version_id,source_visibility,target_visibility) VALUES (?,?,?,?)`, id, published.String, current.Visibility, p.Visibility); err != nil {
		return nil, err
	}
	out.ReviewCreated = true
	out.ReviewID = id
	out.Pending = !auto
	if auto {
		if err = r.applyAudience(ctx, tx, scope, current, p.Visibility, p.OperatorID, p.OperatorName, p.RequestID, "visibility_change", now); err != nil {
			return nil, err
		}
		if err = settleVisibilityReview(ctx, tx, id, p.OperatorID, p.OperatorName, model.ReviewDecisionSourcePolicy, now); err != nil {
			return nil, err
		}
		current.Visibility = p.Visibility
	}
	return out, tx.Commit()
}

type visibilityRelease struct {
	model.Plugin
	Relations []byte
}

func loadVisibilityRelease(ctx context.Context, tx *sql.Tx, p *model.Plugin, versionID string) (*visibilityRelease, error) {
	out := &visibilityRelease{Plugin: *p}
	var label string
	var manifest, pkg, keys []byte
	err := tx.QueryRowContext(ctx, `SELECT v.manifest_json,v.plugin_json,v.attachment_keys_json,v.relations_json,v.manifest_hash,v.plugin_hash,rel.version_label FROM plugin_versions v JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id WHERE v.plugin_id=? AND v.version_id=? FOR UPDATE`, p.ID, versionID).Scan(&manifest, &pkg, &keys, &out.Relations, &out.ManifestHash, &out.PluginHash, &label)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrVersionBaselineUnavailable
	}
	if err != nil {
		return nil, err
	}
	out.Manifest = cloneJSON(manifest)
	out.Package = cloneJSON(pkg)
	out.AttachmentKeys = cloneJSON(keys)
	out.CurrentVersion = &label
	out.CurrentVersionID = &versionID
	out.Name, out.Tags = manifestNameAndTags(out.Manifest)
	return out, nil
}
func validateVisibilityGraph(ctx context.Context, tx *sql.Tx, p *visibilityRelease) error {
	rels, err := model.DecodeSnapshotRelations(p.Relations)
	if err != nil {
		return ErrInvalidRelation
	}
	return validateReferenceTree(ctx, tx, p.Plugin, p.Plugin, rels, true, map[string]bool{}, 0)
}
func (r *Repo) applyAudience(ctx context.Context, tx *sql.Tx, scope Scope, p *model.Plugin, target model.PluginVisibility, uid, name, requestID, action string, now time.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE plugins SET visibility=?,updated_at=? WHERE plugin_id=? AND space_id=? AND deleted_at IS NULL`, target, now, p.ID, scope.SpaceID)
	if err != nil {
		return err
	}
	if err = mustAffect(res); err != nil {
		return err
	}
	remark := "visibility=" + string(p.Visibility) + "->" + string(target)
	return insertAudit(ctx, tx, r.id(), now, *p, action, Mutation{OperatorID: uid, OperatorName: name, RequestID: requestID, Remark: &remark}, p.PluginHash, p.PluginHash)
}
func settleVisibilityReview(ctx context.Context, tx *sql.Tx, id, uid, name string, source model.ReviewDecisionSource, now time.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE plugin_review_requests SET status='approved',reviewer_uid=?,reviewer_name=?,decision_source=?,reviewed_at=?,updated_at=? WHERE review_id=? AND status='pending'`, uid, name, source, now, now, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}
func (r *Repo) approveVisibilityReview(ctx context.Context, tx *sql.Tx, scope Scope, p ApproveReviewParams, current *model.Plugin) (*model.Plugin, error) {
	var versionID string
	var from, to model.PluginVisibility
	if err := tx.QueryRowContext(ctx, `SELECT published_version_id,source_visibility,target_visibility FROM plugin_visibility_reviews WHERE review_id=?`, p.ReviewID).Scan(&versionID, &from, &to); err != nil {
		return nil, err
	}
	var published sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=? AND space_id=?`, current.ID, scope.SpaceID).Scan(&published); err != nil {
		return nil, err
	}
	if current.ListingState != model.PluginListingStatePublished || current.Visibility != from || !published.Valid || published.String != versionID || from != model.PluginVisibilityPrivate || to != model.PluginVisibilitySpace {
		return nil, ErrConflict
	}
	frozen, err := loadVisibilityRelease(ctx, tx, current, versionID)
	if err != nil {
		return nil, err
	}
	frozen.Visibility = to
	if err = validateVisibilityGraph(ctx, tx, frozen); err != nil {
		return nil, err
	}
	now := p.Now
	if now.IsZero() {
		now = r.now()
	}
	source := p.DecisionSource
	if source == "" {
		source = model.ReviewDecisionSourceWeb
	}
	if err = r.applyAudience(ctx, tx, scope, current, to, p.ReviewerUID, p.ReviewerName, p.RequestID, "visibility_approve", now); err != nil {
		return nil, err
	}
	if err = settleVisibilityReview(ctx, tx, p.ReviewID, p.ReviewerUID, p.ReviewerName, source, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	current.Visibility = to
	return current, nil
}
