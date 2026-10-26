package plugin

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// LatestGraph contains only authorized, currently published resources from one
// database snapshot. Relations are resolved copies; immutable version bytes are
// retained unchanged for integrity verification after the transaction closes.
type LatestGraph struct {
	RootID    string
	Plugins   map[string]*model.Plugin
	Versions  map[InstallVersionKey]*model.PluginVersion
	Selected  map[string]string
	Relations map[string][]model.PluginRelation
}

// ResolveLatestGraph never substitutes an older release for an unavailable head.
// Admin privileges do not widen the audience of distributed content.
func (r *Repo) ResolveLatestGraph(ctx context.Context, sc Scope, id string) (*LatestGraph, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	g := &LatestGraph{RootID: id, Plugins: map[string]*model.Plugin{}, Versions: map[InstallVersionKey]*model.PluginVersion{}, Selected: map[string]string{}, Relations: map[string][]model.PluginRelation{}}
	visiting := map[string]bool{}
	edges := 0
	var visit func(string, *model.Plugin, int) error
	visit = func(target string, source *model.Plugin, depth int) error {
		if depth > 2 || visiting[target] {
			return ErrInvalidRelation
		}
		p := g.Plugins[target]
		if p != nil {
			if source != nil && !referenceAudience(*source, *p) {
				return ErrNotFound
			}
			return nil
		}
		if len(g.Plugins) >= 1500 {
			return ErrGraphTooLarge
		}
		p, err = scanPlugin(tx.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM plugins p WHERE p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL AND p.is_embedded=0 AND p.listing_state='published' AND `+installAccess, target, sc.SpaceID, sc.CallerUID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if source != nil && (!referenceAudience(*source, *p) || !referenceAudience(*g.Plugins[id], *p)) {
			return ErrNotFound
		}
		g.Plugins[target] = p
		if p.Type == model.PluginTypeConnector {
			return overlayConnectorEffective(ctx, tx, p)
		}
		v, err := scanManagedVersion(tx.QueryRowContext(ctx, `SELECT `+managedVersionColumns+` FROM plugins p JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND v.version_id=p.published_version_id JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id WHERE p.plugin_id=?`, target))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrVersionBaselineUnavailable
		}
		if err != nil {
			return err
		}
		g.Selected[target] = v.ID
		g.Versions[InstallVersionKey{target, v.ID}] = v
		rels, err := model.DecodeSnapshotRelations(v.Relations)
		if err != nil {
			return ErrInvalidRelation
		}
		edges += len(rels)
		if edges > 2000 {
			return ErrGraphTooLarge
		}
		visiting[target] = true
		defer delete(visiting, target)
		for i := range rels {
			edge := &rels[i]
			if edge.SourcePluginID != target || edge.Status != 1 || edge.DeletedAt != nil {
				return ErrInvalidRelation
			}
			if err = visit(edge.TargetPluginID, p, depth+1); err != nil {
				return err
			}
			child := g.Plugins[edge.TargetPluginID]
			if !validPersistedRelation(edge.Type, p.Type, child.Type) {
				return ErrInvalidRelation
			}
			edge.TargetPluginType = child.Type
			if child.Type == model.PluginTypeConnector {
				edge.TargetVersionID = nil
			} else {
				selected := g.Selected[child.ID]
				edge.TargetVersionID = &selected
			}
		}
		if len(rels) > 0 {
			if err = ValidateReferenceMetadata(p.Type, rels); err != nil {
				return err
			}
		}
		g.Relations[target] = rels
		return nil
	}
	if err = visit(id, nil, 0); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return g, nil
}

// These methods expose an already frozen graph to the content builder without
// opening further database reads or extending the caller's authorization.
func (g *LatestGraph) GetInstallPlugins(_ context.Context, _ Scope, ids []string) (map[string]*model.Plugin, error) {
	out := map[string]*model.Plugin{}
	for _, id := range ids {
		if p := g.Plugins[id]; p != nil {
			out[id] = p
		}
	}
	return out, nil
}
func (g *LatestGraph) GetInstallVersions(_ context.Context, _ Scope, keys []InstallVersionKey) (map[InstallVersionKey]*model.PluginVersion, error) {
	out := map[InstallVersionKey]*model.PluginVersion{}
	for _, key := range keys {
		if v := g.Versions[key]; v != nil {
			out[key] = v
		}
	}
	return out, nil
}
