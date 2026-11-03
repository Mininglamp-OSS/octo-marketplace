package plugin

import (
	"context"
	"database/sql"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func (r *Repo) suspendIncomingDependents(ctx context.Context, tx *sql.Tx, space, target, uid, name, requestID string) error {
	queue := []string{target}
	seen := map[string]bool{target: true}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sources, err := r.incomingPublishedSources(ctx, tx, space, id)
		if err != nil {
			return err
		}
		for _, source := range sources {
			if seen[source] {
				continue
			}
			seen[source] = true
			if len(seen) > maxGraphNodes {
				return ErrGraphTooLarge
			}
			queue = append(queue, source)
			p, err := getReviewedPluginForUpdate(ctx, tx, space, source)
			if err != nil {
				return err
			}
			var published sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=?`, source).Scan(&published); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_dependency_suspensions(plugin_id,space_id,published_version_id,suspended_at) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE published_version_id=VALUES(published_version_id),suspended_at=VALUES(suspended_at)`, source, space, nullString(published), r.now()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE plugins SET listing_state='delisted',updated_at=? WHERE plugin_id=? AND space_id=?`, r.now(), source, space); err != nil {
				return err
			}
			if err := demoteEmbeddedChildren(ctx, tx, source, p.Type, space, r.now()); err != nil {
				return err
			}
			audit := Mutation{OperatorID: uid, OperatorName: name, RequestID: requestID, Remark: strPtr("dependency_unavailable")}
			if err := insertAudit(ctx, tx, r.id(), r.now(), *p, "dependency_suspend", audit, p.PluginHash, p.PluginHash); err != nil {
				return err
			}
		}
	}
	return nil
}

// DependencyStatuses is owner/admin only. A persisted suspension survives
// recovery; the release path explicitly clears it after successful validation.
func (r *Repo) DependencyStatuses(ctx context.Context, scope Scope, items []model.Plugin) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range items {
		if !scope.Admin && (p.OwnerUID != scope.CallerUID || p.SpaceID == nil || *p.SpaceID != scope.SpaceID) {
			continue
		}
		var suspended, unavailable bool
		err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_dependency_suspensions s WHERE s.plugin_id=p.plugin_id AND s.space_id=p.space_id),`+unavailableDependencySQL(availabilityRoot)+` FROM plugins p WHERE p.plugin_id=? AND p.space_id=? AND p.status=1 AND p.deleted_at IS NULL`, p.ID, scope.SpaceID).Scan(&suspended, &unavailable)
		if err != nil {
			return nil, err
		}
		if suspended {
			out[p.ID] = "restored"
			if unavailable {
				out[p.ID] = "unavailable"
			}
		}
	}
	return out, nil
}
