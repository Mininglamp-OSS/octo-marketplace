package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// changeVisibilityPlan is the confirmed, transactional audience-change path.
// Participant review rows deliberately occupy each existing pending slot: all
// legacy mutation gates see the group, including connector paths.
func (r *Repo) changeVisibilityPlan(ctx context.Context, sc Scope, p VisibilityParams) (out *VisibilityResult, err error) {
	defer func() { err = classifyDeadlock(err) }()
	preview, err := r.VisibilityPlan(ctx, sc, p.PluginID, p.Visibility)
	if err != nil {
		return nil, err
	}
	if preview.Revision != p.ExpectedPlanRevision || len(preview.Blockers) > 0 {
		return nil, ErrConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Lock every changed and affected resource in one global order. The plan is
	// rebuilt after locks; new references/publications invalidate confirmation.
	ids := []string{}
	for _, item := range preview.Changes {
		ids = append(ids, item.PluginID)
	}
	for _, item := range preview.Affected {
		ids = append(ids, item.PluginID)
	}
	sortStringsUnique(&ids)
	for _, id := range ids {
		if _, err = getReviewedPluginForUpdate(ctx, tx, sc.SpaceID, id); err != nil {
			return nil, err
		}
	}
	root, err := readVisibilityNode(ctx, tx, p.PluginID)
	if err != nil {
		return nil, err
	}
	if root.Plugin.OwnerUID != sc.CallerUID || root.Plugin.SpaceID == nil || *root.Plugin.SpaceID != sc.SpaceID {
		return nil, ErrNotFound
	}
	locked, err := visibilityPlanWithImpact(ctx, sc, root, p.Visibility, tx)
	if err != nil {
		return nil, err
	}
	if !sameVisibilityPlan(preview, locked) || root.Pending {
		return nil, ErrConflict
	}
	out = &VisibilityResult{Plugin: &root.Plugin, PublishedVersionID: root.PublishedID}
	if root.Plugin.Visibility == p.Visibility {
		return out, tx.Commit()
	}
	if p.Visibility == model.PluginVisibilityPrivate {
		if err = r.applyAudience(ctx, tx, sc, &root.Plugin, p.Visibility, p.OperatorID, p.OperatorName, p.RequestID, "visibility_change", r.now()); err != nil {
			return nil, err
		}
		for _, item := range locked.Affected {
			if err = suspendDependency(ctx, tx, item.PluginID, root.Plugin.ID, r.now()); err != nil {
				return nil, err
			}
		}
		root.Plugin.Visibility = p.Visibility
		return out, tx.Commit()
	}
	var auto bool = true
	err = tx.QueryRowContext(ctx, `SELECT is_auto_approve_enabled FROM plugin_review_policies WHERE space_id=? FOR UPDATE`, sc.SpaceID).Scan(&auto)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	now, rootReview := r.now(), r.id()
	raw, _ := json.Marshal(locked)
	for _, item := range locked.Changes {
		n, err := readVisibilityNode(ctx, tx, item.PluginID)
		if err != nil {
			return nil, err
		}
		id := r.id()
		if item.PluginID == p.PluginID {
			id = rootReview
		}
		frozen := &visibilityRelease{Plugin: n.Plugin, Relations: []byte(`[]`)}
		if n.Plugin.Type != model.PluginTypeConnector {
			frozen, err = loadVisibilityRelease(ctx, tx, &n.Plugin, n.PublishedID)
			if err != nil {
				return nil, err
			}
		}
		label := ""
		if frozen.CurrentVersion != nil {
			label = *frozen.CurrentVersion
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO plugin_review_requests (review_id,plugin_id,space_id,target_scope,status,kind,version,changelog,manifest_json,plugin_json,attachment_keys_json,relations_json,manifest_hash,plugin_hash,applicant_uid,applicant_name,submitted_at,created_at,updated_at) VALUES (?,?,?,'space','pending','visibility',?,NULL,?,?,?,?,?,?,?,?,?,?,?)`, id, item.PluginID, sc.SpaceID, label, string(frozen.Manifest), string(frozen.Package), jsonColumn(frozen.AttachmentKeys), string(frozen.Relations), frozen.ManifestHash, frozen.PluginHash, p.OperatorID, p.OperatorName, now, now, now)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO plugin_visibility_reviews (review_id,published_version_id,source_visibility,target_visibility) VALUES (?,?,?,?)`, id, n.PublishedID, n.Plugin.Visibility, p.Visibility)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_visibility_review_groups (review_id,root_review_id,plugin_id,plan_json) VALUES (?,?,?,?)`, id, rootReview, item.PluginID, string(raw)); err != nil {
			return nil, err
		}
	}
	out.ReviewID = rootReview
	out.ReviewCreated = true
	out.Pending = !auto
	if auto {
		if err = r.applyVisibilityGroup(ctx, tx, sc, rootReview, p.OperatorID, p.OperatorName, p.RequestID, model.ReviewDecisionSourcePolicy); err != nil {
			return nil, err
		}
		root.Plugin.Visibility = p.Visibility
	}
	return out, tx.Commit()
}

type visibilityGroupMember struct{ ReviewID, PluginID string }

func visibilityGroup(ctx context.Context, tx *sql.Tx, reviewID string) (string, []visibilityGroupMember, *model.VisibilityPlan, error) {
	var root string
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT root_review_id,plan_json FROM plugin_visibility_review_groups WHERE review_id=?`, reviewID).Scan(&root, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil, nil
	}
	if err != nil {
		return "", nil, nil, err
	}
	var plan model.VisibilityPlan
	if err = json.Unmarshal(raw, &plan); err != nil {
		return "", nil, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT g.review_id,g.plugin_id FROM plugin_visibility_review_groups g JOIN plugin_review_requests rr ON rr.review_id=g.review_id WHERE g.root_review_id=? ORDER BY g.plugin_id`, root)
	if err != nil {
		return "", nil, nil, err
	}
	members := []visibilityGroupMember{}
	for rows.Next() {
		var m visibilityGroupMember
		if err = rows.Scan(&m.ReviewID, &m.PluginID); err != nil {
			rows.Close()
			return "", nil, nil, err
		}
		members = append(members, m)
	}
	err = rows.Err()
	rows.Close()
	return root, members, &plan, err
}
func (r *Repo) applyVisibilityGroup(ctx context.Context, tx *sql.Tx, sc Scope, reviewID, uid, name, requestID string, source model.ReviewDecisionSource) error {
	_, members, plan, err := visibilityGroup(ctx, tx, reviewID)
	if err != nil {
		return err
	}
	if plan == nil {
		return ErrConflict
	}
	nodes := map[string]*visibilityNode{}
	for _, m := range members {
		p, err := getReviewedPluginForUpdate(ctx, tx, sc.SpaceID, m.PluginID)
		if err != nil {
			return err
		}
		var status string
		if err = tx.QueryRowContext(ctx, `SELECT status FROM plugin_review_requests WHERE review_id=? AND space_id=? FOR UPDATE`, m.ReviewID, sc.SpaceID).Scan(&status); err != nil {
			return err
		}
		if status != "pending" {
			return ErrConflict
		}
		n, err := readVisibilityNode(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		n.Pending = false
		nodes[p.ID] = n
	}
	root := nodes[plan.PluginID]
	if root == nil {
		return ErrConflict
	}
	owner := Scope{CallerUID: root.Plugin.OwnerUID, SpaceID: sc.SpaceID}
	read := func(ctx context.Context, id string) (*visibilityNode, error) {
		if n := nodes[id]; n != nil {
			return n, nil
		}
		return readVisibilityNode(ctx, tx, id)
	}
	current, err := buildVisibilityPlan(ctx, owner, root, model.PluginVisibilitySpace, read)
	if err != nil {
		return err
	}
	if current.Revision != plan.Revision || len(current.Blockers) > 0 {
		return ErrConflict
	}
	// Scope swaps first, then validate frozen references against the transaction's
	// complete new audience. Any invalid graph rolls the entire group back.
	for _, item := range plan.Changes {
		n := nodes[item.PluginID]
		if n == nil {
			return ErrConflict
		}
		if err = r.applyAudience(ctx, tx, owner, &n.Plugin, item.To, uid, name, requestID, "visibility_approve", r.now()); err != nil {
			return err
		}
	}
	for _, item := range plan.Changes {
		n := nodes[item.PluginID]
		if n.Plugin.Type != model.PluginTypeConnector {
			f, err := loadVisibilityRelease(ctx, tx, &n.Plugin, n.PublishedID)
			if err != nil {
				return err
			}
			f.Visibility = item.To
			if err = r.validateVisibilityGraph(ctx, tx, f); err != nil {
				return err
			}
		}
	}
	for _, m := range members {
		if err = settleVisibilityReview(ctx, tx, m.ReviewID, uid, name, source, r.now()); err != nil {
			return err
		}
	}
	return nil
}

func finishVisibilityGroup(ctx context.Context, tx *sql.Tx, reviewID, spaceID, status, uid, name, reason string, now any) error {
	_, members, _, err := visibilityGroup(ctx, tx, reviewID)
	if err != nil {
		return err
	}
	for _, m := range members {
		res, err := tx.ExecContext(ctx, `UPDATE plugin_review_requests SET status=?,reviewer_uid=?,reviewer_name=?,reason=?,reviewed_at=?,updated_at=? WHERE review_id=? AND space_id=? AND status='pending'`, status, uid, name, reason, now, now, m.ReviewID, spaceID)
		if err != nil {
			return err
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 && m.ReviewID != reviewID {
			return ErrConflict
		}
	}
	return nil
}

func rejectPendingVisibilityMutation(ctx context.Context, tx *sql.Tx, id string) error {
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_visibility_review_groups g JOIN plugin_review_requests rr ON rr.review_id=g.review_id WHERE g.plugin_id=? AND rr.status='pending' AND rr.deleted_at IS NULL)`, id).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return ErrReviewPending
	}
	return nil
}
