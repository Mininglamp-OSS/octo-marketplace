package plugin

import (
	"context"
	"database/sql"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type audienceDecisionMember struct {
	reviewID, pluginID, versionID, revision, applicant string
	plugin                                             *model.Plugin
}

func audienceGroupID(ctx context.Context, tx *sql.Tx, reviewID string) (string, error) {
	var id sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT group_id FROM plugin_visibility_reviews WHERE review_id=?`, reviewID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id.String, err
}
func loadAudienceGroup(ctx context.Context, tx *sql.Tx, space, group string) ([]audienceDecisionMember, error) {
	rows, err := tx.QueryContext(ctx, `SELECT rr.review_id,rr.plugin_id,vr.published_version_id,vr.expected_revision,rr.applicant_uid,rr.status FROM plugin_review_requests rr JOIN plugin_visibility_reviews vr ON vr.review_id=rr.review_id WHERE vr.group_id=? AND rr.space_id=? AND rr.deleted_at IS NULL ORDER BY rr.plugin_id FOR UPDATE`, group, space)
	if err != nil {
		return nil, err
	}
	members := []audienceDecisionMember{}
	for rows.Next() {
		var m audienceDecisionMember
		var status string
		if err := rows.Scan(&m.reviewID, &m.pluginID, &m.versionID, &m.revision, &m.applicant, &status); err != nil {
			rows.Close()
			return nil, err
		}
		if status != "pending" {
			rows.Close()
			return nil, ErrConflict
		}
		members = append(members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, ErrNotFound
	}
	for i := range members {
		p, err := getReviewedPluginForUpdate(ctx, tx, space, members[i].pluginID)
		if err != nil {
			return nil, err
		}
		members[i].plugin = p
	}
	return members, nil
}
func (r *Repo) applyAudienceGroup(ctx context.Context, tx *sql.Tx, scope Scope, reviewID, uid, name, requestID string, source model.ReviewDecisionSource, now time.Time) (*model.Plugin, error) {
	if source == "" {
		source = model.ReviewDecisionSourceWeb
	}
	group, err := audienceGroupID(ctx, tx, reviewID)
	if err != nil {
		return nil, err
	}
	if group == "" {
		return nil, ErrConflict
	}
	members, err := loadAudienceGroup(ctx, tx, scope.SpaceID, group)
	if err != nil {
		return nil, err
	}
	var selected *model.Plugin
	for _, m := range members {
		p := m.plugin
		if p.IsEmbedded || p.OwnerUID != m.applicant || p.ListingState != model.PluginListingStatePublished || p.Visibility != model.PluginVisibilityPrivate || model.PluginDraftRevision(p) != m.revision {
			return nil, ErrConflict
		}
		var published sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=? AND space_id=?`, p.ID, scope.SpaceID).Scan(&published); err != nil {
			return nil, err
		}
		if versioned(p) && (!published.Valid || published.String != m.versionID) {
			return nil, ErrConflict
		}
		p.PublishedVersionID = nullString(published)
		if err := r.applyAudience(ctx, tx, scope, p, model.PluginVisibilitySpace, uid, name, requestID, "visibility_approve", now); err != nil {
			return nil, err
		}
		p.Visibility = model.PluginVisibilitySpace
		if m.reviewID == reviewID {
			selected = p
		}
	}
	// All audiences are changed before graph checks, within the same transaction.
	// Any stale/unavailable member rolls back the whole group and every audit row.
	for _, m := range members {
		if versioned(m.plugin) {
			frozen, err := loadAudienceRelease(ctx, tx, m.plugin)
			if err != nil {
				return nil, err
			}
			frozen.Visibility = model.PluginVisibilitySpace
			if err := r.validateVisibilityGraph(ctx, tx, frozen); err != nil {
				return nil, err
			}
		}
		if err := settleVisibilityReview(ctx, tx, m.reviewID, uid, name, source, now); err != nil {
			return nil, err
		}
	}
	return selected, nil
}
func (r *Repo) settleAudienceGroup(ctx context.Context, tx *sql.Tx, space, reviewID, status, uid, name, reason, requestID string, source model.ReviewDecisionSource, now time.Time) (bool, error) {
	if source == "" {
		source = model.ReviewDecisionSourceWeb
	}
	group, err := audienceGroupID(ctx, tx, reviewID)
	if err != nil || group == "" {
		return false, err
	}
	members, err := loadAudienceGroup(ctx, tx, space, group)
	if err != nil {
		return true, err
	}
	for _, m := range members {
		if _, err := tx.ExecContext(ctx, `UPDATE plugin_review_requests SET status=?,reviewer_uid=?,reviewer_name=?,reason=?,decision_source=?,reviewed_at=?,updated_at=? WHERE review_id=? AND status='pending'`, status, uid, name, reason, string(source), now, now, m.reviewID); err != nil {
			return true, err
		}
		audit := Mutation{OperatorID: uid, OperatorName: name, RequestID: requestID, Remark: strPtr("audience_group=" + group)}
		if err := insertAudit(ctx, tx, r.id(), now, *m.plugin, "visibility_"+status, audit, m.plugin.PluginHash, m.plugin.PluginHash); err != nil {
			return true, err
		}
	}
	return true, nil
}

// Verify mutable content and release pointer after acquiring the resource lock.
func verifyAudienceBaseline(ctx context.Context, tx *sql.Tx, locked, planned *model.Plugin) error {
	var id sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=? AND space_id=?`, locked.ID, *locked.SpaceID).Scan(&id); err != nil {
		return err
	}
	if id.String != publishedID(planned) || model.PluginDraftRevision(locked) != model.PluginDraftRevision(planned) {
		return ErrConflict
	}
	return nil
}

// Hydrate only after the primary read has authorized the request's Space/applicant.
// A batch contains resources from that same Space and applicant exclusively.
func (r *Repo) fillAudienceReview(ctx context.Context, rr *model.PluginReviewRequest) error {
	if !r.versionManagement || rr == nil || rr.Kind != model.ReviewKindVisibility {
		return nil
	}
	var group sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT group_id FROM plugin_visibility_reviews WHERE review_id=?`, rr.ID).Scan(&group)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if !group.Valid {
		return nil
	}
	rr.ReviewGroupID = group.String
	rows, err := r.db.QueryContext(ctx, `SELECT rr.review_id,rr.plugin_id,COALESCE(JSON_UNQUOTE(JSON_EXTRACT(rr.manifest_json,'$.plugin_name')),'') FROM plugin_review_requests rr JOIN plugin_visibility_reviews vr ON vr.review_id=rr.review_id WHERE vr.group_id=? AND rr.space_id=? AND rr.applicant_uid=? AND rr.deleted_at IS NULL ORDER BY rr.plugin_id`, group.String, rr.SpaceID, rr.ApplicantUID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var m model.AudienceReviewMember
		if err := rows.Scan(&m.ReviewID, &m.PluginID, &m.Name); err != nil {
			return err
		}
		rr.AudienceMembers = append(rr.AudienceMembers, m)
	}
	return rows.Err()
}
