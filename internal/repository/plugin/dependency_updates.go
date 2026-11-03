package plugin

import (
	"context"
	"database/sql"
	"errors"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"sort"
	"strings"
)

func directDependencyUpdates(root *visibilityNode, targets map[string]*visibilityNode, skipped map[string]bool) []model.DependencyUpdate {
	out := []model.DependencyUpdate{}
	var refs []model.PluginRelation
	for _, v := range root.Releases {
		if v.ID == root.PublishedID {
			refs = v.Relations
			break
		}
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.Status != 1 || ref.DeletedAt != nil || ref.TargetVersionID == nil || seen[ref.TargetPluginID] {
			continue
		}
		if root.Plugin.Type == model.PluginTypeExpert && ref.Type != "expert_skill" || root.Plugin.Type == model.PluginTypeExpertTeam && ref.Type != "expert_team_expert" {
			continue
		}
		n := targets[ref.TargetPluginID]
		if n == nil || n.Plugin.ListingState != model.PluginListingStatePublished {
			continue
		}
		var from, latest visibilityReleaseEdges
		for _, v := range n.Releases {
			if v.ID == *ref.TargetVersionID {
				from = v
			}
			if latest.ID == "" || model.VersionAdvanced(latest.Label, v.Label) {
				latest = v
			}
		}
		if from.ID == "" || !model.VersionAdvanced(from.Label, latest.Label) {
			continue
		}
		seen[ref.TargetPluginID] = true
		out = append(out, model.DependencyUpdate{PluginID: n.Plugin.ID, Name: latest.Name, Type: n.Plugin.Type, FromVersionID: from.ID, FromVersion: from.Label, TargetVersionID: latest.ID, TargetVersion: latest.Label, IsSkipped: skipped[n.Plugin.ID+"\x00"+latest.ID]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PluginID < out[j].PluginID })
	return out
}

// DependencyUpdateSummaries enriches an entire owner-list page with one query.
// Only confirmed formal direct references and readable available targets join.
func (r *Repo) DependencyUpdateSummaries(ctx context.Context, sc Scope, items []model.Plugin) (map[string][]model.DependencyUpdate, error) {
	out := map[string][]model.DependencyUpdate{}
	ids := []any{sc.SpaceID, sc.CallerUID}
	marks := []string{}
	for _, p := range items {
		if p.Type == model.PluginTypeExpert || p.Type == model.PluginTypeExpertTeam {
			marks = append(marks, "?")
			ids = append(ids, p.ID)
		}
	}
	if !r.versionManagement || len(marks) == 0 {
		return out, nil
	}
	query := `SELECT root.plugin_id,p.plugin_id,COALESCE(JSON_UNQUOTE(JSON_EXTRACT(target_snapshot.manifest_json,'$.plugin_name')),''),p.plugin_type,old.version_id,old.version_label,new.version_id,new.version_label,
 EXISTS(SELECT 1 FROM plugin_dependency_reminder_skips skipped WHERE skipped.space_id=root.space_id AND skipped.uid=root.owner_uid AND skipped.plugin_id=root.plugin_id AND skipped.dependency_plugin_id=p.plugin_id AND skipped.target_version_id=new.version_id)
 FROM plugins root JOIN plugin_versions source ON source.plugin_id=root.plugin_id AND source.version_id=root.published_version_id
 JOIN JSON_TABLE(source.relations_json,'$[*]' COLUMNS (` + snapshotRelationColumns + `)) edge
 JOIN plugins p ON p.plugin_id=` + snapshotField("edge", "target_plugin_id") + `
 JOIN plugin_version_releases old ON old.plugin_id=p.plugin_id AND old.version_id=` + snapshotField("edge", "target_version_id") + `
 JOIN plugin_version_releases new ON new.plugin_id=p.plugin_id
 JOIN plugin_versions target_snapshot ON target_snapshot.plugin_id=p.plugin_id AND target_snapshot.version_id=new.version_id
 WHERE root.space_id=? AND root.owner_uid=? AND root.plugin_id IN (` + strings.Join(marks, ",") + `) AND root.status=1 AND root.deleted_at IS NULL
 AND COALESCE(edge.status,edge.status_legacy,1)=1 AND COALESCE(edge.deleted_at,edge.deleted_at_legacy) IS NULL
 AND ((root.plugin_type='expert' AND ` + snapshotField("edge", "relation_type") + `='expert_skill') OR (root.plugin_type='expert_team' AND ` + snapshotField("edge", "relation_type") + `='expert_team_expert'))
 AND p.status=1 AND p.deleted_at IS NULL AND p.listing_state='published'
 AND (p.visibility IN ('public','system') OR (p.space_id=root.space_id AND (p.visibility='space' OR p.owner_uid=root.owner_uid)))
 AND NOT ` + unavailableDependencySQL(availabilityRoot)
	rows, err := r.db.QueryContext(ctx, query, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	latest := map[string]map[string]model.DependencyUpdate{}
	for rows.Next() {
		var root string
		var u model.DependencyUpdate
		if err = rows.Scan(&root, &u.PluginID, &u.Name, &u.Type, &u.FromVersionID, &u.FromVersion, &u.TargetVersionID, &u.TargetVersion, &u.IsSkipped); err != nil {
			return nil, err
		}
		if !model.VersionAdvanced(u.FromVersion, u.TargetVersion) {
			continue
		}
		if latest[root] == nil {
			latest[root] = map[string]model.DependencyUpdate{}
		}
		old, ok := latest[root][u.PluginID]
		if !ok || model.VersionAdvanced(old.TargetVersion, u.TargetVersion) {
			latest[root][u.PluginID] = u
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for root, deps := range latest {
		for _, u := range deps {
			out[root] = append(out[root], u)
		}
		sort.Slice(out[root], func(i, j int) bool { return out[root][i].PluginID < out[root][j].PluginID })
	}
	return out, nil
}

func (r *Repo) DependencyUpdates(ctx context.Context, sc Scope, id string) (*model.DependencyUpdates, error) {
	if !r.versionManagement {
		return nil, ErrVersionBaselineUnavailable
	}
	root, err := readVisibilityNode(ctx, r.db, id)
	if err != nil {
		return nil, err
	}
	if root.Plugin.OwnerUID != sc.CallerUID || root.Plugin.SpaceID == nil || *root.Plugin.SpaceID != sc.SpaceID || root.Plugin.IsEmbedded || (root.Plugin.Type != model.PluginTypeExpert && root.Plugin.Type != model.PluginTypeExpertTeam) {
		return nil, ErrNotFound
	}
	targets := map[string]*visibilityNode{}
	for _, v := range root.Releases {
		if v.ID != root.PublishedID {
			continue
		}
		for _, ref := range v.Relations {
			if ref.Type == "expert_connector" {
				continue
			}
			n, e := readVisibilityNode(ctx, r.db, ref.TargetPluginID)
			if errors.Is(e, ErrNotFound) {
				continue
			}
			if e != nil {
				return nil, e
			}
			p := n.Plugin
			if p.Visibility == model.PluginVisibilitySystem || p.Visibility == model.PluginVisibilityPublic || (p.SpaceID != nil && *p.SpaceID == sc.SpaceID && (p.Visibility == model.PluginVisibilitySpace || p.OwnerUID == sc.CallerUID)) {
				if p.Type == model.PluginTypeExpert {
					blocked, e := r.HasUnavailableDependencies(ctx, sc, p.ID)
					if e != nil {
						return nil, e
					}
					if blocked {
						continue
					}
				}
				targets[p.ID] = n
			}
		}
	}
	rows, err := r.db.QueryContext(ctx, `SELECT dependency_plugin_id,target_version_id FROM plugin_dependency_reminder_skips WHERE space_id=? AND uid=? AND plugin_id=?`, sc.SpaceID, sc.CallerUID, id)
	if err != nil {
		return nil, err
	}
	skipped := map[string]bool{}
	for rows.Next() {
		var dep, v string
		if err = rows.Scan(&dep, &v); err != nil {
			rows.Close()
			return nil, err
		}
		skipped[dep+"\x00"+v] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return &model.DependencyUpdates{PluginID: id, PublishedVersionID: root.PublishedID, DraftRevision: model.PluginDraftRevision(&root.Plugin), Updates: directDependencyUpdates(root, targets, skipped)}, nil
}

func (r *Repo) SkipDependencyUpdates(ctx context.Context, sc Scope, id string, targets []model.DependencyUpdateTarget) error {
	if len(targets) == 0 || len(targets) > maxGraphEdges {
		return ErrConflict
	}
	updates, err := r.DependencyUpdates(ctx, sc, id)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, u := range updates.Updates {
		allowed[u.PluginID+"\x00"+u.TargetVersionID] = true
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = getOwnedForUpdate(ctx, tx, sc, id); err != nil {
		return err
	}
	for _, target := range targets {
		if !allowed[target.PluginID+"\x00"+target.TargetVersionID] {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT IGNORE INTO plugin_dependency_reminder_skips (space_id,uid,plugin_id,dependency_plugin_id,target_version_id,created_at) VALUES (?,?,?,?,?,?)`, sc.SpaceID, sc.CallerUID, id, target.PluginID, target.TargetVersionID, r.now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Used only under a caller-owned root lock when conditionally adopting pins.
func checkUpdateTargets(ctx context.Context, tx *sql.Tx, sc Scope, targets []model.DependencyUpdateTarget) error {
	for _, target := range targets {
		n, err := readVisibilityNode(ctx, tx, target.PluginID)
		if err != nil {
			return err
		}
		if n.Plugin.Type == model.PluginTypeConnector || n.Plugin.ListingState != model.PluginListingStatePublished {
			return ErrConflict
		}
		if n.Plugin.Visibility != model.PluginVisibilitySystem && n.Plugin.Visibility != model.PluginVisibilityPublic && (n.Plugin.SpaceID == nil || *n.Plugin.SpaceID != sc.SpaceID || (n.Plugin.Visibility == model.PluginVisibilityPrivate && n.Plugin.OwnerUID != sc.CallerUID)) {
			return ErrNotFound
		}
		latest := visibilityReleaseEdges{}
		for _, v := range n.Releases {
			if latest.ID == "" || model.VersionAdvanced(latest.Label, v.Label) {
				latest = v
			}
		}
		if latest.ID != target.TargetVersionID {
			return ErrConflict
		}
		found := false
		for _, v := range n.Releases {
			if v.ID == target.TargetVersionID {
				found = true
			}
		}
		if !found {
			return ErrConflict
		}
	}
	return nil
}
