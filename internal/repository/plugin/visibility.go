package plugin

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

var ErrAudienceConfirmation = errors.New("visibility impact confirmation required")

type VisibilityParams struct {
	ExpectedRevision                    string
	PlanRevision                        string
	Confirm                             bool
	PluginID                            string
	ExpectedPublishedVersionID          string
	Visibility                          model.PluginVisibility
	OperatorID, OperatorName, RequestID string
}

type VisibilityResult struct {
	GroupID            string
	PlanRevision       string
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
	root, err := getOwnedForUpdate(ctx, tx, scope, p.PluginID)
	if err != nil {
		return nil, err
	}
	plan, err := r.buildVisibilityPlan(ctx, tx, scope, root, p.Visibility)
	if err != nil {
		return nil, err
	}
	if versioned(root) {
		if p.ExpectedPublishedVersionID != publishedID(root) {
			return nil, ErrConflict
		}
	} else if p.ExpectedRevision == "" || p.ExpectedRevision != model.PluginDraftRevision(root) {
		return nil, ErrConflict
	}
	out := &VisibilityResult{Plugin: root, PublishedVersionID: publishedID(root), PlanRevision: plan.PlanRevision}
	var pending string
	err = tx.QueryRowContext(ctx, `SELECT review_id FROM plugin_review_requests WHERE plugin_id=? AND status='pending' AND deleted_at IS NULL LIMIT 1 FOR UPDATE`, root.ID).Scan(&pending)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if pending != "" {
		var to string
		var version, revision string
		if e := tx.QueryRowContext(ctx, `SELECT target_visibility,published_version_id,COALESCE(expected_revision,'') FROM plugin_visibility_reviews WHERE review_id=?`, pending).Scan(&to, &version, &revision); e == nil && to == string(p.Visibility) && version == publishedID(root) && (revision == "" || revision == model.PluginDraftRevision(root)) {
			out.ReviewID = pending
			out.GroupID, err = audienceGroupID(ctx, tx, pending)
			if err != nil {
				return nil, err
			}
			out.Pending = true
			return out, tx.Commit()
		}
		return nil, ErrReviewPending
	}
	if root.Visibility == p.Visibility {
		return out, tx.Commit()
	}
	if p.PlanRevision != "" && p.PlanRevision != plan.PlanRevision {
		return nil, ErrConflict
	}
	if len(plan.members) > 1 || len(plan.impacts) > 0 {
		if !p.Confirm || p.PlanRevision == "" {
			return nil, ErrAudienceConfirmation
		}
	}
	// Lock/recheck every resource selected by the preview before applying changes.
	// Existing write paths also lock resources; deadlock victims are safely retried.
	for _, member := range plan.members {
		locked, e := getOwnedForUpdate(ctx, tx, scope, member.ID)
		if e != nil {
			return nil, e
		}
		if e = verifyAudienceBaseline(ctx, tx, locked, member); e != nil {
			return nil, e
		}
		if model.PluginDraftRevision(locked) != model.PluginDraftRevision(member) {
			return nil, ErrConflict
		}
		if member.ID != root.ID {
			var n int
			if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_review_requests WHERE plugin_id=? AND status='pending' AND deleted_at IS NULL`, member.ID).Scan(&n); e != nil {
				return nil, e
			}
			if n > 0 {
				return nil, ErrReviewPending
			}
		}
	}
	if p.Visibility == model.PluginVisibilityPrivate {
		if err = r.applyAudience(ctx, tx, scope, root, p.Visibility, p.OperatorID, p.OperatorName, p.RequestID, "visibility_change", r.now()); err != nil {
			return nil, err
		}
		for _, source := range plan.impacts {
			locked, e := getReviewedPluginForUpdate(ctx, tx, scope.SpaceID, source.ID)
			if e != nil {
				return nil, e
			}
			if e = verifyAudienceBaseline(ctx, tx, locked, source); e != nil {
				return nil, e
			}
			if model.PluginDraftRevision(locked) != model.PluginDraftRevision(source) {
				return nil, ErrConflict
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_dependency_suspensions(plugin_id,space_id,published_version_id,suspended_at) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE published_version_id=VALUES(published_version_id),suspended_at=VALUES(suspended_at)`, source.ID, scope.SpaceID, source.PublishedVersionID, r.now()); e != nil {
				return nil, e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE plugins SET listing_state='delisted',updated_at=? WHERE plugin_id=? AND space_id=?`, r.now(), source.ID, scope.SpaceID); e != nil {
				return nil, e
			}
			if e = demoteEmbeddedChildren(ctx, tx, source.ID, source.Type, scope.SpaceID, r.now()); e != nil {
				return nil, e
			}
			m := Mutation{OperatorID: p.OperatorID, OperatorName: p.OperatorName, RequestID: p.RequestID, Remark: strPtr("dependency_visibility_changed")}
			if e = insertAudit(ctx, tx, r.id(), r.now(), *source, "dependency_suspend", m, source.PluginHash, source.PluginHash); e != nil {
				return nil, e
			}
		}
		root.Visibility = p.Visibility
		return out, tx.Commit()
	}
	auto := true
	err = tx.QueryRowContext(ctx, `SELECT is_auto_approve_enabled FROM plugin_review_policies WHERE space_id=? FOR UPDATE`, scope.SpaceID).Scan(&auto)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	group := r.id()
	out.GroupID = group
	out.Pending = !auto
	out.ReviewCreated = true
	for _, member := range plan.members {
		frozen, e := loadAudienceRelease(ctx, tx, member)
		if e != nil {
			return nil, e
		}
		id := r.id()
		if member.ID == root.ID {
			out.ReviewID = id
		}
		now := r.now()
		label := "1.0.0"
		if frozen.CurrentVersion != nil {
			label = *frozen.CurrentVersion
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_review_requests (review_id,plugin_id,space_id,target_scope,status,kind,version,manifest_json,plugin_json,attachment_keys_json,relations_json,manifest_hash,plugin_hash,applicant_uid,applicant_name,submitted_at,created_at,updated_at) VALUES(?,?,?,'space','pending','visibility',?,?,?,?,?,?,?,?,?,?,?,?)`, id, member.ID, scope.SpaceID, label, string(frozen.Manifest), string(frozen.Package), jsonColumn(frozen.AttachmentKeys), string(frozen.Relations), frozen.ManifestHash, frozen.PluginHash, p.OperatorID, p.OperatorName, now, now, now); e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_visibility_reviews(review_id,published_version_id,source_visibility,target_visibility,group_id,expected_revision) VALUES(?,?,'private','space',?,?)`, id, publishedID(member), group, model.PluginDraftRevision(member)); e != nil {
			return nil, e
		}
	}
	if auto {
		if _, err = r.applyAudienceGroup(ctx, tx, scope, out.ReviewID, p.OperatorID, p.OperatorName, p.RequestID, model.ReviewDecisionSourcePolicy, r.now()); err != nil {
			return nil, err
		}
		root.Visibility = model.PluginVisibilitySpace
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
func (r *Repo) validateVisibilityGraph(ctx context.Context, tx *sql.Tx, p *visibilityRelease) error {
	// Use the same rollout-aware validator and immutable version as publication.
	if r.references {
		return r.validateReleaseReferences(ctx, tx, p.Plugin, *p.CurrentVersionID)
	}
	rels, err := model.DecodeSnapshotRelations(p.Relations)
	if err != nil {
		return ErrInvalidRelation
	}
	retained := map[string]struct{}{}
	owner := Scope{CallerUID: p.OwnerUID, SpaceID: *p.SpaceID}
	for _, edge := range rels {
		child, err := getOwnedForUpdate(ctx, tx, owner, edge.TargetPluginID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if child.IsEmbedded && validPersistedRelation(edge.Type, p.Type, child.Type) {
			retained[child.ID] = struct{}{}
		}
	}
	return r.validateReleaseReferences(ctx, tx, p.Plugin, *p.CurrentVersionID, retained)
}
func (r *Repo) applyAudience(ctx context.Context, tx *sql.Tx, scope Scope, p *model.Plugin, target model.PluginVisibility, uid, name, requestID, action string, now time.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE plugins SET visibility=?,updated_at=? WHERE plugin_id=? AND space_id=? AND deleted_at IS NULL`, target, now, p.ID, scope.SpaceID)
	if err != nil {
		return err
	}
	if err = mustAffect(res); err != nil {
		return err
	}
	if err = syncVisibilityEmbeddedAudience(ctx, tx, p, target, now); err != nil {
		return err
	}
	if err = ensureVisibleDefaultPlacement(ctx, tx, r.id, now, p.ID, p.CategoryID); err != nil {
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
	group, err := audienceGroupID(ctx, tx, p.ReviewID)
	if err != nil {
		return nil, err
	}
	if group != "" {
		now := p.Now
		if now.IsZero() {
			now = r.now()
		}
		selected, err := r.applyAudienceGroup(ctx, tx, scope, p.ReviewID, p.ReviewerUID, p.ReviewerName, p.RequestID, p.DecisionSource, now)
		if err != nil {
			return nil, err
		}
		return selected, tx.Commit()
	}
	if !versioned(current) {
		return nil, ErrConflict
	}
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
	if err = r.validateVisibilityGraph(ctx, tx, frozen); err != nil {
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

// Connector audience changes freeze current configuration metadata for review,
// without inventing an immutable Connector version.
func loadAudienceRelease(ctx context.Context, tx *sql.Tx, p *model.Plugin) (*visibilityRelease, error) {
	if !versioned(p) {
		return &visibilityRelease{Plugin: *p, Relations: []byte(`[]`)}, nil
	}
	return loadVisibilityRelease(ctx, tx, p, publishedID(p))
}
