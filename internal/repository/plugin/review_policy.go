package plugin

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// GetReviewPolicy returns the effective policy. Absence is deliberately the
// enabled default; database failures remain errors so callers fail closed.
func (r *Repo) GetReviewPolicy(ctx context.Context, scope Scope) (model.PluginReviewPolicy, error) {
	var enabled bool
	var updated sql.NullTime
	err := r.db.QueryRowContext(ctx,
		`SELECT is_auto_approve_enabled,updated_at FROM plugin_review_policies WHERE space_id=?`,
		scope.SpaceID).Scan(&enabled, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.PluginReviewPolicy{IsAutoApproveEnabled: true}, nil
	}
	if err != nil {
		return model.PluginReviewPolicy{}, wrapped("get plugin review policy", err)
	}
	out := model.PluginReviewPolicy{IsAutoApproveEnabled: enabled}
	if updated.Valid {
		out.UpdatedAt = &updated.Time
	}
	return out, nil
}

func (r *Repo) UpsertReviewPolicy(ctx context.Context, scope Scope, enabled bool, uid, name string) (model.PluginReviewPolicy, error) {
	now := r.now()
	_, err := r.db.ExecContext(ctx, `INSERT INTO plugin_review_policies
		(space_id,is_auto_approve_enabled,updated_by,updated_by_name,created_at,updated_at)
		VALUES (?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE is_auto_approve_enabled=VALUES(is_auto_approve_enabled),
		updated_by=VALUES(updated_by),updated_by_name=VALUES(updated_by_name),updated_at=VALUES(updated_at)`,
		scope.SpaceID, enabled, uid, name, now, now)
	if err != nil {
		return model.PluginReviewPolicy{}, wrapped("upsert plugin review policy", err)
	}
	return model.PluginReviewPolicy{IsAutoApproveEnabled: enabled, UpdatedAt: &now}, nil
}
