package plugin

import (
	"context"
	"database/sql"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func suspendDependency(ctx context.Context, tx *sql.Tx, id, source string, now any) error {
	// Keep the independent cause for owner feedback after the target recovers.
	// listing_state itself gates every catalog/install/reference read already.
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO plugin_dependency_suspensions (plugin_id,source_plugin_id,created_at) VALUES (?,?,?)`, id, source, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE plugins SET listing_state='delisted',updated_at=? WHERE plugin_id=? AND listing_state='published'`, now, id)
	return err
}

func (r *Repo) SuspensionDiagnostics(ctx context.Context, sc Scope, items []model.Plugin) (map[string]bool, map[string][]model.DependencyIssue, error) {
	flags := map[string]bool{}
	issues := map[string][]model.DependencyIssue{}
	args := []any{sc.SpaceID, sc.CallerUID}
	ids := []string{}
	for _, p := range items {
		ids = append(ids, p.ID)
		args = append(args, p.ID)
	}
	if len(ids) == 0 {
		return flags, issues, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT root.plugin_id,s.source_plugin_id,p.plugin_id,p.visibility,p.listing_state,p.status,p.deleted_at,p.owner_uid,root.visibility FROM plugin_dependency_suspensions s JOIN plugins root ON root.plugin_id=s.plugin_id LEFT JOIN plugins p ON p.plugin_id=s.source_plugin_id WHERE root.space_id=? AND root.owner_uid=? AND root.plugin_id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var root, source string
		var id, visibility, listing, sourceOwner sql.NullString
		var rootVisibility string
		var active sql.NullInt64
		var deleted sql.NullTime
		if err = rows.Scan(&root, &source, &id, &visibility, &listing, &active, &deleted, &sourceOwner, &rootVisibility); err != nil {
			return nil, nil, err
		}
		flags[root] = true
		reason := ""
		switch {
		case !id.Valid || active.Int64 != 1 || deleted.Valid:
			reason = "deleted"
		case listing.String != "published":
			reason = "delisted"
		case visibility.String == "private" && (sourceOwner.String != sc.CallerUID || rootVisibility != "private"):
			reason = "visibility_restricted"
		}
		if reason != "" {
			issues[root] = append(issues[root], model.DependencyIssue{TargetPluginID: source, Reason: reason, Via: []string{}})
		}
	}
	return flags, issues, rows.Err()
}
