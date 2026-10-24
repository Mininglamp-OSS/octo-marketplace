package plugin

import (
	"context"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// InstallVersionKey identifies an immutable released dependency, never a label.
type InstallVersionKey struct{ PluginID, VersionID string }

// installAccess deliberately ignores placement and listing for existing pins.
// The resolver separately requires the ROOT to be listed. Delisting stops new
// references, but does not revoke an already-released dependency. Private access
// still requires both the owning user and owning Space, even for administrators.
const installAccess = `(p.visibility IN ('system','public') OR (p.space_id=? AND (p.visibility='space' OR (p.visibility='private' AND p.owner_uid=?))))`

// GetInstallPlugins batches one dependency wave. Scope.Admin never bypasses the
// caller's audience: installation distributes actual content into a workspace.
func (r *Repo) GetInstallPlugins(ctx context.Context, sc Scope, ids []string) (map[string]*model.Plugin, error) {
	out := map[string]*model.Plugin{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 631 {
		return nil, ErrGraphTooLarge
	}
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, sc.SpaceID, sc.CallerUID)
	rows, err := r.db.QueryContext(ctx, `SELECT `+pluginColumns+` FROM plugins p WHERE p.plugin_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) AND p.status=1 AND p.deleted_at IS NULL AND p.is_embedded=0 AND `+installAccess, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		out[p.ID] = p
	}
	return out, rows.Err()
}

// GetInstallVersions returns only formal releases joined to their own resource.
// Missing, foreign, unpublished and inaccessible versions all remain absent.
func (r *Repo) GetInstallVersions(ctx context.Context, sc Scope, keys []InstallVersionKey) (map[InstallVersionKey]*model.PluginVersion, error) {
	out := map[InstallVersionKey]*model.PluginVersion{}
	if len(keys) == 0 {
		return out, nil
	}
	if len(keys) > 631 {
		return nil, ErrGraphTooLarge
	}
	predicates := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)*2+2)
	for _, key := range keys {
		predicates = append(predicates, "(v.plugin_id=? AND v.version_id=?)")
		args = append(args, key.PluginID, key.VersionID)
	}
	args = append(args, sc.SpaceID, sc.CallerUID)
	rows, err := r.db.QueryContext(ctx, `SELECT `+managedVersionColumns+` FROM plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id WHERE (`+strings.Join(predicates, " OR ")+`) AND p.plugin_type<>'connector' AND p.status=1 AND p.deleted_at IS NULL AND p.is_embedded=0 AND `+installAccess, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanManagedVersion(rows)
		if err != nil {
			return nil, err
		}
		out[InstallVersionKey{v.PluginID, v.ID}] = v
	}
	return out, rows.Err()
}
