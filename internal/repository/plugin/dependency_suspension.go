package plugin

import (
	"context"
	"database/sql"
	"errors"

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

// Clear an existing marker only when the candidate snapshot would pass the
// catalog availability check. The conditional write and publication share a
// transaction, so a failed recovery cannot advance the release or relist it.
//
// The suspension cascade demoted the container's embedded children together with
// the container itself (see demoteEmbeddedChildren), and the availability check
// reads the candidate snapshot, where those children are still delisted. Every
// recovery attempt would therefore fail with target_not_published and roll back,
// and the children cannot be published on their own to break the loop. Restore
// the embedded subtree the candidate release actually declares — only rows owned
// by this container; promoteFrozenEmbeddedChildren never touches a standalone
// target — before judging the release. Promotion shares this transaction, so a
// genuinely unavailable independent dependency still fails the check and rolls
// the promotion back with the rest of the publication.
func clearRecoveredSuspension(ctx context.Context, tx *sql.Tx, p *model.Plugin, versionID string, now any) error {
	var marker string
	err := tx.QueryRowContext(ctx, `SELECT plugin_id FROM plugin_dependency_suspensions WHERE plugin_id=? LIMIT 1 FOR UPDATE`, p.ID).Scan(&marker)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	rels, err := candidateReleaseRelations(ctx, tx, p.ID, versionID)
	if err != nil {
		return err
	}
	spaceID := ""
	if p.SpaceID != nil {
		spaceID = *p.SpaceID
	}
	if err = promoteFrozenEmbeddedChildren(ctx, tx, p.ID, p.Type, rels, spaceID, p.Visibility, now); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE s FROM plugin_dependency_suspensions s JOIN plugins p ON p.plugin_id=s.plugin_id WHERE s.plugin_id=? AND NOT `+unavailableSnapshotSQL("p.plugin_id", "?", "p"), p.ID, versionID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return &ReferenceValidationError{Field: "relations", Reason: "target_not_published"}
	}
	return nil
}

// candidateReleaseRelations reads the immutable topology of the version being
// released. recordRelease has already established that the row exists; an
// absent or empty snapshot is treated as an empty topology so recovery stays a
// no-op instead of failing on a second lookup.
func candidateReleaseRelations(ctx context.Context, tx *sql.Tx, id, versionID string) ([]model.PluginRelation, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`, id, versionID).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return model.DecodeSnapshotRelations(raw)
}
func usesTargetSnapshotSQL(sourceID, version string) string {
	direct := `EXISTS(SELECT 1 FROM plugin_versions sv JOIN JSON_TABLE(sv.relations_json,'$[*]' COLUMNS (` + snapshotRelationColumns + `)) e WHERE sv.plugin_id=` + sourceID + ` AND sv.version_id=` + version + ` AND ` + snapshotField("e", "target_plugin_id") + `=? AND COALESCE(e.status,e.status_legacy,1)=1 AND COALESCE(e.deleted_at,e.deleted_at_legacy) IS NULL)`
	nested := `EXISTS(SELECT 1 FROM plugin_versions sv JOIN JSON_TABLE(sv.relations_json,'$[*]' COLUMNS (` + snapshotRelationColumns + `)) e JOIN plugins member ON member.plugin_id=` + snapshotField("e", "target_plugin_id") + ` WHERE sv.plugin_id=` + sourceID + ` AND sv.version_id=` + version + ` AND ` + snapshotField("e", "relation_type") + `='expert_team_expert' AND COALESCE(e.status,e.status_legacy,1)=1 AND COALESCE(e.deleted_at,e.deleted_at_legacy) IS NULL AND ` + usesTargetLeafSQL("member.plugin_id", `COALESCE(`+snapshotField("e", "target_version_id")+`,member.published_version_id)`) + `)`
	return `(` + direct + ` OR ` + nested + `)`
}
func usesTargetLeafSQL(id, version string) string {
	return `EXISTS(SELECT 1 FROM plugin_versions mv JOIN JSON_TABLE(mv.relations_json,'$[*]' COLUMNS (` + snapshotRelationColumns + `)) me WHERE mv.plugin_id=` + id + ` AND mv.version_id=` + version + ` AND ` + snapshotField("me", "target_plugin_id") + `=? AND COALESCE(me.status,me.status_legacy,1)=1 AND COALESCE(me.deleted_at,me.deleted_at_legacy) IS NULL)`
}
func (r *Repo) incomingPublishedSources(ctx context.Context, tx *sql.Tx, space, id string) ([]string, error) {
	snapshot := usesTargetSnapshotSQL("p.plugin_id", "p.published_version_id")
	live := `EXISTS(SELECT 1 FROM plugin_relations l WHERE l.source_plugin_id=p.plugin_id AND l.status=1 AND l.deleted_at IS NULL AND (l.target_plugin_id=? OR (l.relation_type='expert_team_expert' AND (` + usesTargetLeafSQL("l.target_plugin_id", `COALESCE(l.target_version_id,(SELECT published_version_id FROM plugins WHERE plugin_id=l.target_plugin_id))`) + ` OR EXISTS(SELECT 1 FROM plugin_relations ml WHERE l.target_version_id IS NULL AND (SELECT published_version_id FROM plugins WHERE plugin_id=l.target_plugin_id) IS NULL AND ml.source_plugin_id=l.target_plugin_id AND ml.target_plugin_id=? AND ml.status=1 AND ml.deleted_at IS NULL)))))`
	rows, err := tx.QueryContext(ctx, `SELECT p.plugin_id FROM plugins p WHERE p.space_id=? AND p.status=1 AND p.deleted_at IS NULL AND p.listing_state='published' AND p.plugin_type IN ('expert','expert_team') AND ((p.published_version_id IS NOT NULL AND `+snapshot+`) OR (p.published_version_id IS NULL AND `+live+`)) ORDER BY p.plugin_id LIMIT ?`, space, id, id, id, id, id, maxGraphNodes+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) > maxGraphNodes {
		return nil, ErrGraphTooLarge
	}
	return ids, rows.Err()
}

func (r *Repo) suspendDelistedDependents(ctx context.Context, tx *sql.Tx, space, target string, now any) error {
	queue := []string{target}
	seen := map[string]bool{target: true}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		ids, err := r.incomingPublishedSources(ctx, tx, space, id)
		if err != nil {
			return err
		}
		for _, source := range ids {
			if seen[source] {
				continue
			}
			if len(seen) > maxGraphNodes {
				return ErrGraphTooLarge
			}
			seen[source] = true
			p, e := getReviewedPluginForUpdate(ctx, tx, space, source)
			if e != nil {
				return e
			}
			if e = suspendDependency(ctx, tx, source, id, now); e != nil {
				return e
			}
			if e = demoteEmbeddedChildren(ctx, tx, source, p.Type, space, now); e != nil {
				return e
			}
			queue = append(queue, source)
		}
	}
	return nil
}
