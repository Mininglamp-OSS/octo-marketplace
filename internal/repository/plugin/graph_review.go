package plugin

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Graphs mix the caller's own rows with other authors' and global rows. Resolve
// review state in the existing root/node SELECTs, but expose it only for rows
// owned by this caller in this Space, just like Service.Detail. Unlike the mine
// listing, the graph cannot assume that every returned row belongs to its caller.
// Pending takes precedence over newer terminal reviews, matching
// LatestReviewForPlugin and pluginReviewStateColumns. The binary owner comparison
// matches Detail's exact Go string equality even under a case-insensitive collation.
const graphOwnerReviewColumns = `,EXISTS(SELECT 1 FROM plugin_review_requests rr
  WHERE rr.plugin_id=p.plugin_id AND rr.status='pending' AND rr.deleted_at IS NULL
    AND CAST(p.owner_uid AS BINARY)=CAST(? AS BINARY) AND p.space_id=?)
,(SELECT rr2.review_id FROM plugin_review_requests rr2
  WHERE rr2.plugin_id=p.plugin_id AND rr2.deleted_at IS NULL
    AND CAST(p.owner_uid AS BINARY)=CAST(? AS BINARY) AND p.space_id=?
  ORDER BY (rr2.status='pending') DESC, rr2.submitted_at DESC, rr2.review_id DESC LIMIT 1)
,(SELECT rr3.status FROM plugin_review_requests rr3
  WHERE rr3.plugin_id=p.plugin_id AND rr3.deleted_at IS NULL
    AND CAST(p.owner_uid AS BINARY)=CAST(? AS BINARY) AND p.space_id=?
  ORDER BY (rr3.status='pending') DESC, rr3.submitted_at DESC, rr3.review_id DESC LIMIT 1)`

func graphOwnerReviewArgs(scope Scope) []any {
	return []any{scope.CallerUID, scope.SpaceID, scope.CallerUID, scope.SpaceID, scope.CallerUID, scope.SpaceID}
}

// Keep the root's review enrichment in its single query, including for leaves.
// Get remains unchanged for callers that do not consume review metadata.
func (r *Repo) getGraphRoot(ctx context.Context, scope Scope, pluginID string) (*model.Plugin, error) {
	q := `SELECT ` + pluginColumns + pluginMetricColumns + graphOwnerReviewColumns + ` FROM plugins p
WHERE p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL`
	args := append(graphOwnerReviewArgs(scope), pluginID)
	if !scope.Admin {
		q += ` AND ` + visibilitySQL
		args = append(args, scope.SpaceID, scope.CallerUID)
	}
	p, err := scanPluginRow(r.db.QueryRowContext(ctx, q, args...), true, true, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}
