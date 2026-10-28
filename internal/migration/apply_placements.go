package migration

import (
	"context"
	"database/sql"
	"sort"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// A unique (scene,plugin) key cannot represent conflicting category/order/visible
// memberships. Refuse that merge instead of silently broadening discoverability.
func mergeMigrationPlacements(existing, incoming []model.PluginPlacement) ([]model.PluginPlacement, error) {
	out := append([]model.PluginPlacement{}, existing...)
	seen := map[string]model.PluginPlacement{}
	for _, v := range existing {
		seen[v.PlacementCode] = v
	}
	for _, v := range incoming {
		if old, ok := seen[v.PlacementCode]; ok {
			if old.Visible != v.Visible || old.SortOrder != v.SortOrder || !equalCategory(old.CategoryID, v.CategoryID) {
				return nil, errApply
			}
			continue
		}
		seen[v.PlacementCode] = v
		out = append(out, v)
	}
	return out, nil
}
func equalCategory(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func readMigrationPlacements(ctx context.Context, tx *sql.Tx, id string) ([]model.PluginPlacement, error) {
	rows, e := tx.QueryContext(ctx, `SELECT placement_code,category_id,visible,sort_order,created_at,updated_at FROM plugin_placements WHERE plugin_id=? ORDER BY placement_code FOR UPDATE`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.PluginPlacement{}
	for rows.Next() {
		var v model.PluginPlacement
		if e = rows.Scan(&v.PlacementCode, &v.CategoryID, &v.Visible, &v.SortOrder, &v.CreatedAt, &v.UpdatedAt); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func transferMigrationPlacements(ctx context.Context, tx *sql.Tx, p Plan, n Node) error {
	original, e := readMigrationPlacements(ctx, tx, n.Plugin.ID)
	if e != nil {
		return e
	}
	merged := original
	sources := map[string]bool{}
	if n.Clone {
		sources[n.SourceID] = true
	}
	for _, m := range p.Mappings {
		if m.TargetID == n.Plugin.ID && m.SourceID != m.TargetID {
			sources[m.SourceID] = true
		}
	}
	ids := []string{}
	for id := range sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rows, e := readMigrationPlacements(ctx, tx, id)
		if e != nil {
			return e
		}
		merged, e = mergeMigrationPlacements(merged, rows)
		if e != nil {
			return e
		}
	}
	if len(merged) == 0 && n.Clone && n.Plugin.ListingState == model.PluginListingStatePublished && !n.Plugin.IsEmbedded {
		merged = []model.PluginPlacement{{PlacementCode: "default", CategoryID: n.Plugin.CategoryID, Visible: true, CreatedAt: n.Plugin.CreatedAt, UpdatedAt: n.Plugin.UpdatedAt}}
	}
	have := map[string]bool{}
	for _, v := range original {
		have[v.PlacementCode] = true
	}
	for _, v := range merged {
		if !have[v.PlacementCode] {
			if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`, applyStableID(p.ID+":placement:"+n.Plugin.ID+":"+v.PlacementCode), v.PlacementCode, n.Plugin.ID, v.CategoryID, v.Visible, v.SortOrder, v.CreatedAt, v.UpdatedAt); e != nil {
				return e
			}
		}
		// Preserve existing category registrations; add missing ones without changing
		// any current category visibility, ordering, or scene definitions.
		if v.CategoryID != nil {

			categoryVisible, categoryOrder, categoryCreated, categoryUpdated := v.Visible, v.SortOrder, v.CreatedAt, v.UpdatedAt
			err := tx.QueryRowContext(ctx, `SELECT visible,sort_order,created_at,updated_at FROM plugin_category_placements WHERE placement_code=? AND plugin_type=? AND category_id=?`, v.PlacementCode, n.Source.Type, v.CategoryID).Scan(&categoryVisible, &categoryOrder, &categoryCreated, &categoryUpdated)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if _, e = tx.ExecContext(ctx, `INSERT IGNORE INTO plugin_category_placements (placement_id,placement_code,plugin_type,category_id,visible,sort_order,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`, applyStableID("category:"+v.PlacementCode+":"+string(n.Plugin.Type)+":"+*v.CategoryID), v.PlacementCode, n.Plugin.Type, v.CategoryID, categoryVisible, categoryOrder, categoryCreated, categoryUpdated); e != nil {
				return e
			}
		}
	}
	return nil
}

// Keep every historical release and its standalone/embedded eligibility intact.
// Owner/admin history may still list this identity; the report exposes its ID.
func retireMigrationCatalog(ctx context.Context, tx *sql.Tx, id string) error {
	_, e := tx.ExecContext(ctx, `UPDATE plugin_placements SET visible=0 WHERE plugin_id=?`, id)
	return e
}
