package plugin

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// PinnedGraph is the authorized dependency closure of one published source
// version under the P model. Unlike LatestGraph it never follows a target's
// current published_version_id: every Skill/Expert target is the exact version
// pinned in the importing source snapshot. Connectors keep their existing
// ID + effective semantics and therefore carry no version pin.
//
// The fields intentionally mirror LatestGraph so A34 and install consumers can
// share the same shape.
type PinnedGraph struct {
	RootID    string
	Plugins   map[string]*model.Plugin
	Versions  map[InstallVersionKey]*model.PluginVersion
	Selected  map[string]string
	Relations map[string][]model.PluginRelation
}

// ResolvePinnedGraph fails closed when a released snapshot is missing a
// Skill/Expert pin or when a pinned version no longer exists as a released,
// visible target. It never substitutes the target's current head, a newer
// release, or an older release: the pin is the contract the importing
// publisher froze, and an unavailable pin is a dependency error.
func (r *Repo) ResolvePinnedGraph(ctx context.Context, sc Scope, id string) (*PinnedGraph, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	g := &PinnedGraph{RootID: id, Plugins: map[string]*model.Plugin{}, Versions: map[InstallVersionKey]*model.PluginVersion{}, Selected: map[string]string{}, Relations: map[string][]model.PluginRelation{}}
	visiting := map[string]bool{}
	edges := 0
	// The graph is keyed by plugin id, exactly like LatestGraph. That shape can
	// carry at most one version per target, so two different pins for the same
	// target in one closure are refused instead of silently picking one.
	var visit func(string, *string, *model.Plugin, int) (*model.Plugin, error)
	visit = func(target string, pin *string, source *model.Plugin, depth int) (*model.Plugin, error) {
		if depth > 2 || visiting[target] {
			return nil, ErrInvalidRelation
		}
		if p := g.Plugins[target]; p != nil {
			if source != nil && !referenceAudience(*source, *p) {
				return nil, ErrNotFound
			}
			if p.Type == model.PluginTypeConnector {
				if pin != nil {
					return nil, ErrInvalidRelation
				}
				return p, nil
			}
			if pin == nil || g.Selected[target] != strings.TrimSpace(*pin) {
				return nil, ErrInvalidRelation
			}
			return p, nil
		}
		if len(g.Plugins) >= 1500 {
			return nil, ErrGraphTooLarge
		}
		p, err := scanPlugin(tx.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM plugins p WHERE p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL AND p.is_embedded=0 AND p.listing_state='published' AND `+installAccess, target, sc.SpaceID, sc.CallerUID))
		if errors.Is(err, sql.ErrNoRows) {
			if source == nil {
				return nil, ErrNotFound
			}
			return nil, ErrDependencyUnavailable
		}
		if err != nil {
			return nil, err
		}
		if source != nil && (!referenceAudience(*source, *p) || !referenceAudience(*g.Plugins[id], *p)) {
			return nil, ErrDependencyUnavailable
		}
		g.Plugins[target] = p
		if p.Type == model.PluginTypeConnector {
			// Connector resolution is ID + effective. A stored pin is a corrupt
			// snapshot, not an optional hint, so it fails closed.
			if pin != nil {
				return nil, ErrInvalidRelation
			}
			if err := overlayConnectorEffective(ctx, tx, p); err != nil {
				if source == nil {
					return nil, err
				}
				return nil, ErrDependencyUnavailable
			}
			return p, nil
		}
		var v *model.PluginVersion
		if pin == nil {
			// Root selection is unchanged: the source's published head.
			if source != nil {
				return nil, ErrInvalidRelation
			}
			v, err = scanManagedVersion(tx.QueryRowContext(ctx, `SELECT `+managedVersionColumns+` FROM plugins p JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND v.version_id=p.published_version_id JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id WHERE p.plugin_id=?`, target))
		} else {
			versionID := strings.TrimSpace(*pin)
			if versionID == "" {
				return nil, ErrInvalidRelation
			}
			// The join on v.plugin_id=p.plugin_id proves the version belongs to
			// the target; the release join proves it is still a formal release.
			v, err = scanManagedVersion(tx.QueryRowContext(ctx, `SELECT `+managedVersionColumns+` FROM plugins p JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND v.version_id=? JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id WHERE p.plugin_id=?`, versionID, target))
		}
		if errors.Is(err, sql.ErrNoRows) {
			if source == nil {
				return nil, ErrVersionBaselineUnavailable
			}
			return nil, ErrDependencyUnavailable
		}
		if err != nil {
			return nil, err
		}
		g.Selected[target] = v.ID
		g.Versions[InstallVersionKey{target, v.ID}] = v
		rels, err := model.DecodeSnapshotRelations(v.Relations)
		if err != nil {
			return nil, ErrInvalidRelation
		}
		edges += len(rels)
		if edges > 2000 {
			return nil, ErrGraphTooLarge
		}
		visiting[target] = true
		defer delete(visiting, target)
		for i := range rels {
			edge := &rels[i]
			if edge.SourcePluginID != target || edge.Status != 1 || edge.DeletedAt != nil {
				return nil, ErrInvalidRelation
			}
			child, err := visit(edge.TargetPluginID, edge.TargetVersionID, p, depth+1)
			if err != nil {
				return nil, err
			}
			if !validPersistedRelation(edge.Type, p.Type, child.Type) {
				return nil, ErrInvalidRelation
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
				return nil, err
			}
		}
		g.Relations[target] = rels
		return p, nil
	}
	if _, err = visit(id, nil, nil, 0); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return g, nil
}

// These methods expose an already frozen graph to the install builder without
// opening further database reads or extending the caller's authorization.
func (g *PinnedGraph) GetInstallPlugins(_ context.Context, _ Scope, ids []string) (map[string]*model.Plugin, error) {
	out := map[string]*model.Plugin{}
	for _, id := range ids {
		if p := g.Plugins[id]; p != nil {
			out[id] = p
		}
	}
	return out, nil
}
func (g *PinnedGraph) GetInstallVersions(_ context.Context, _ Scope, keys []InstallVersionKey) (map[InstallVersionKey]*model.PluginVersion, error) {
	out := map[InstallVersionKey]*model.PluginVersion{}
	for _, key := range keys {
		if v := g.Versions[key]; v != nil {
			out[key] = v
		}
	}
	return out, nil
}
