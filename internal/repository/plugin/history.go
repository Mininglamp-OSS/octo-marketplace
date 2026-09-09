package plugin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func (r *Repo) ListVersions(ctx context.Context, scope Scope, pluginID string, limit, offset int) ([]model.PluginVersion, int64, error) {
	if _, err := r.Get(ctx, scope, pluginID); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id WHERE v.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL AND `+visibilitySQL, pluginID, scope.SpaceID, scope.CallerUID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	// Project metadata columns only: the versions LIST redacts manifest/package to
	// keep intermediate/rolled-back content out of the response, so materializing
	// those (up to 1 MiB each × the page) server-side just to discard them would
	// compound the row count into hundreds of MB per request.
	rows, err := r.db.QueryContext(ctx, `SELECT v.version_id,v.plugin_id,v.version,v.manifest_hash,v.plugin_hash,v.relations_json,v.changelog,v.created_by,v.created_at
FROM plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id WHERE v.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL AND `+visibilitySQL+` ORDER BY v.created_at DESC,v.version_id DESC LIMIT ? OFFSET ?`, pluginID, scope.SpaceID, scope.CallerUID, limit, max(offset, 0))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.PluginVersion
	for rows.Next() {
		v, err := scanPluginVersion(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := r.redactVersionRelations(ctx, scope, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// redactVersionRelations drops, from each immutable version snapshot, any
// relation whose target the reading caller cannot currently see. The snapshot is
// denormalized JSON captured at the PUBLISHER's visibility, so a public plugin
// relating to a same-Space private target would otherwise leak that target's id,
// relation type, sort order and data to a cross-Space caller — the current-state
// read (GetWithRelations) already filters this way. An unparseable snapshot is
// redacted to empty (fail closed).
func (r *Repo) redactVersionRelations(ctx context.Context, scope Scope, versions []model.PluginVersion) error {
	parsed := make([][]map[string]any, len(versions))
	targets := map[string]struct{}{}
	for i := range versions {
		if len(bytes.TrimSpace(versions[i].Relations)) == 0 {
			continue
		}
		var rels []map[string]any
		if err := json.Unmarshal(versions[i].Relations, &rels); err != nil {
			versions[i].Relations = json.RawMessage("[]")
			continue
		}
		parsed[i] = rels
		for _, rel := range rels {
			if id, ok := rel["target_plugin_id"].(string); ok && id != "" {
				targets[id] = struct{}{}
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	visible, err := r.visibleTargetIDs(ctx, scope, targets)
	if err != nil {
		return err
	}
	for i := range versions {
		if parsed[i] == nil {
			continue
		}
		kept := make([]map[string]any, 0, len(parsed[i]))
		for _, rel := range parsed[i] {
			id, _ := rel["target_plugin_id"].(string)
			if _, ok := visible[id]; ok {
				kept = append(kept, rel)
			}
		}
		raw, err := json.Marshal(kept)
		if err != nil {
			return err
		}
		versions[i].Relations = raw
	}
	return nil
}

// visibleTargetIDs returns the subset of ids the caller may currently see, under
// the same visibility predicate as the catalog read (admin scope sees all).
func (r *Repo) visibleTargetIDs(ctx context.Context, scope Scope, ids map[string]struct{}) (map[string]struct{}, error) {
	idList := make([]any, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(idList)), ",")
	where := visibilitySQL
	args := append([]any{}, idList...)
	if scope.Admin {
		where = "1=1"
	} else {
		args = append(args, scope.SpaceID, scope.CallerUID)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT p.plugin_id FROM plugins p WHERE p.plugin_id IN (`+placeholders+`) AND p.status=1 AND p.deleted_at IS NULL AND `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	visible := make(map[string]struct{}, len(idList))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		visible[id] = struct{}{}
	}
	return visible, rows.Err()
}

func scanPluginVersion(s interface{ Scan(...any) error }) (*model.PluginVersion, error) {
	var v model.PluginVersion
	var rels []byte
	var changelog sql.NullString
	// Metadata projection only — manifest/package/sidecar are intentionally not
	// selected (see ListVersions); the DTO redacts them.
	if err := s.Scan(&v.ID, &v.PluginID, &v.Version, &v.ManifestHash, &v.PluginHash, &rels, &changelog, &v.CreatedBy, &v.CreatedAt); err != nil {
		return nil, err
	}
	v.Relations = cloneJSON(rels)
	v.Changelog = nullString(changelog)
	return &v, nil
}

func (r *Repo) ListPlacementCategories(ctx context.Context, scope Scope, placementCode string, typ model.PluginType) ([]model.PluginCategory, error) {
	// Categories are placement configuration: every active category registered
	// for this placement and plugin type is returned, backed by plugins or not.
	// Scope only shapes plugin_count, which tallies the plugins this caller/Space
	// would actually see in the grid — visible AND listed (listedSQL) — so counts
	// neither leak cross-Space existence nor overstate the page by including the
	// caller's own drafts. There is no "mine" variant of this read.
	rows, err := r.db.QueryContext(ctx, `SELECT c.category_id,c.name,c.icon_key,c.plugin_types_json,cp.sort_order,c.status,c.created_at,c.updated_at,
(SELECT COUNT(DISTINCT p.plugin_id) FROM plugin_placements pp JOIN plugins p ON p.plugin_id=pp.plugin_id
 WHERE pp.placement_code=cp.placement_code AND pp.category_id=c.category_id AND pp.visible=1
 AND p.plugin_type=? AND p.status=1 AND p.deleted_at IS NULL AND `+visibilitySQL+listedSQL+`) AS plugin_count
FROM plugin_category_placements cp JOIN plugin_categories c ON c.category_id=cp.category_id
WHERE cp.placement_code=? AND cp.plugin_type=? AND cp.visible=1 AND c.status=1 AND c.deleted_at IS NULL ORDER BY cp.sort_order,c.category_id`, typ, scope.SpaceID, scope.CallerUID, placementCode, typ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PluginCategory
	for rows.Next() {
		var c model.PluginCategory
		var types []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.IconKey, &types, &c.SortOrder, &c.Status, &c.CreatedAt, &c.UpdatedAt, &c.PluginCount); err != nil {
			return nil, err
		}
		c.PluginTypes = cloneJSON(types)
		out = append(out, c)
	}
	return out, rows.Err()
}
