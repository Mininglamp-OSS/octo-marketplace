package plugin

import (
	"context"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// TagListFilter drives the aggregated tag suggestions across Plugins. Tags are
// free-form strings stored on each row in tags_json — there is no dictionary
// table — so aggregation unnests the array and groups by value.
type TagListFilter struct {
	PlacementCode string
	Type          model.PluginType
	Keyword       string
	Mine          bool
	Limit         int
}

// ListTags aggregates tag names from Plugins visible to the caller, ordered by
// descending plugin count with alphabetical tie-break, mirroring the visible set
// of List: the same scope predicate, plus the same listing gate on the grid path
// (listedSQL) so a chip can never filter to an empty grid. Mine mirrors 我的发布
// instead and therefore keeps tags from the caller's own drafts.
func (r *Repo) ListTags(ctx context.Context, scope Scope, f TagListFilter) ([]model.TagFilter, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	from := ` FROM plugins p`
	args := []any{}
	if f.PlacementCode != "" {
		from += ` JOIN plugin_placements pp ON pp.plugin_id=p.plugin_id AND pp.placement_code=? AND pp.visible=1`
		args = append(args, f.PlacementCode)
	}
	from += ` JOIN JSON_TABLE(p.tags_json, '$[*]' COLUMNS (tag VARCHAR(128) CHARACTER SET utf8mb4 PATH '$')) jt`
	where := ` WHERE p.status=1 AND p.deleted_at IS NULL AND p.is_embedded=0 AND ` + visibilitySQL
	args = append(args, scope.SpaceID, scope.CallerUID)
	if f.Type != "" {
		where += ` AND p.plugin_type=?`
		args = append(args, f.Type)
	}
	if f.Mine {
		where += ` AND p.owner_uid=? AND p.space_id=?`
		args = append(args, scope.CallerUID, scope.SpaceID)
	} else {
		where += listedSQL
	}
	where += ` AND jt.tag IS NOT NULL AND jt.tag <> ''`
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		where += ` AND jt.tag LIKE ? ESCAPE '!'`
		args = append(args, "%"+escapeLike(kw)+"%")
	}
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, `SELECT jt.tag, COUNT(DISTINCT p.plugin_id) cnt`+from+where+` GROUP BY jt.tag ORDER BY cnt DESC, jt.tag ASC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.TagFilter, 0, limit)
	for rows.Next() {
		var t model.TagFilter
		if err := rows.Scan(&t.Name, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
