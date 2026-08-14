package expert

import (
	"context"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Sort modes for expert / squad listing. `latest` (and unknown values) keep
// the default creation-time ordering; `updated` surfaces recently-edited rows;
// the metric-backed modes rank by resource_metrics counters.
const (
	SortComprehensive = "comprehensive"
	SortLatest        = "latest"
	SortInstalls      = "installs"
	SortViews         = "views"
	SortUpdated       = "updated"
)

// ListFilter carries the resolved visibility scope plus the query params. The
// service builds it (resolving tag names to id groups); the repository only
// translates it to SQL. Ordering defaults to created_at DESC, id DESC.
type ListFilter struct {
	CallerUID      string
	SpaceID        string
	Keyword        string
	Categories     []string
	TagIDGroups    [][]int64
	Visibilities   []string
	CreatedByTypes []string
	Sort           string
	Limit          int
	Offset         int
	// MineOnly restricts the result to rows owned by CallerUID inside SpaceID
	// (GET /{entity}/mine). When false the visible-set rule applies.
	MineOnly bool
	// SystemOnly restricts the result to visibility=system rows across every
	// Space (the /admin surface). It takes precedence over the caller-scoped
	// visible-set rule and ignores MineOnly / SpaceID / CallerUID.
	SystemOnly bool
}

// ListExperts returns the page of experts matching the filter plus the total
// count before pagination.
func (r *Repo) ListExperts(ctx context.Context, f ListFilter) ([]model.Expert, int, error) {
	where, args := f.buildWhere()
	total, err := r.count(ctx, "experts", where, args)
	if err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + expertColumns + ` FROM experts WHERE ` + where +
		` ORDER BY ` + f.orderBy(EntityExpert) + ` LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), f.Limit, f.Offset)

	rows, err := r.db.QueryContext(ctx, q, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []model.Expert
	var idGroups [][]int64
	for rows.Next() {
		m, tagIDs, scanErr := scanExpert(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		items = append(items, *m)
		idGroups = append(idGroups, tagIDs)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := r.hydrateListTagNames(ctx, idGroups, func(i int) *[]string { return &items[i].Tags }); err != nil {
		return nil, 0, err
	}
	expertIDs := make([]string, len(items))
	for i := range items {
		expertIDs[i] = items[i].ID
	}
	counts, err := r.loadMetrics(ctx, EntityExpert, expertIDs)
	if err != nil {
		return nil, 0, err
	}
	for i := range items {
		c := counts[items[i].ID]
		items[i].ViewCount, items[i].InstallCount = c.View, c.Install
	}
	return items, total, nil
}

// ListSquads returns the page of squads matching the filter plus the total.
func (r *Repo) ListSquads(ctx context.Context, f ListFilter) ([]model.Squad, int, error) {
	where, args := f.buildWhere()
	total, err := r.count(ctx, "expert_squads", where, args)
	if err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + squadColumns + ` FROM expert_squads WHERE ` + where +
		` ORDER BY ` + f.orderBy(EntitySquad) + ` LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), f.Limit, f.Offset)

	rows, err := r.db.QueryContext(ctx, q, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []model.Squad
	var idGroups [][]int64
	for rows.Next() {
		m, tagIDs, scanErr := scanSquad(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		items = append(items, *m)
		idGroups = append(idGroups, tagIDs)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := r.hydrateListTagNames(ctx, idGroups, func(i int) *[]string { return &items[i].Tags }); err != nil {
		return nil, 0, err
	}
	squadIDs := make([]string, len(items))
	for i := range items {
		squadIDs[i] = items[i].ID
	}
	counts, err := r.loadMetrics(ctx, EntitySquad, squadIDs)
	if err != nil {
		return nil, 0, err
	}
	for i := range items {
		c := counts[items[i].ID]
		items[i].ViewCount, items[i].InstallCount = c.View, c.Install
	}
	return items, total, nil
}

// hydrateListTagNames batch-resolves every tag id across the page in one query
// and assigns ordered names back onto each row via the dst accessor.
func (r *Repo) hydrateListTagNames(ctx context.Context, idGroups [][]int64, dst func(i int) *[]string) error {
	seen := make(map[int64]struct{})
	var all []int64
	for _, ids := range idGroups {
		for _, id := range ids {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			all = append(all, id)
		}
	}
	names, err := r.ResolveTagNames(ctx, all)
	if err != nil {
		return err
	}
	for i, ids := range idGroups {
		*dst(i) = orderedNames(ids, names)
	}
	return nil
}

func (r *Repo) count(ctx context.Context, table, where string, args []any) (int, error) {
	var total int
	q := `SELECT COUNT(*) FROM ` + table + ` WHERE ` + where
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// orderBy resolves the sort mode for one entity's table (derived from the
// entity so the pair can never disagree). `updated` surfaces recently-edited
// rows first; the metric-backed modes rank by the resource_metrics counters
// via a correlated scalar subquery (the metrics table is keyed by
// (resource_type, resource_id), so the lookup is a PK probe and the list
// queries stay join-free). `comprehensive` mirrors the skill catalog's
// ranking (skill/list.go — keep the weights in sync): installs weigh 5×,
// views 1×, plus a recency boost that decays over days so fresh listings
// still surface. Anything else (including `latest` and `relevance` without
// special handling) falls back to the default creation-time ordering
// (doc §4.2).
func (f ListFilter) orderBy(entity Entity) string {
	table := "experts"
	if entity == EntitySquad {
		table = "expert_squads"
	}
	metric := func(column string) string {
		return `COALESCE((SELECT rm.` + column + ` FROM resource_metrics rm
			WHERE rm.resource_type = '` + string(entity) + `' AND rm.resource_id = ` + table + `.id), 0)`
	}
	recent := "created_at DESC, id DESC"
	switch strings.TrimSpace(f.Sort) {
	case SortUpdated:
		return "updated_at DESC, id DESC"
	case SortInstalls:
		return metric("install_count") + " DESC, " + recent
	case SortViews:
		return metric("view_count") + " DESC, " + recent
	case SortComprehensive:
		return `(` + metric("install_count") + ` * 5 + ` + metric("view_count") + `
			+ 20 / POW(TIMESTAMPDIFF(HOUR, created_at, NOW()) / 24 + 2, 1.2)) DESC, ` + recent
	default: // SortLatest and unknown values
		return recent
	}
}

// buildWhere composes the visibility-scoped predicate shared by both entity
// tables. It always includes deleted_at IS NULL. The visible-set rule
// (doc §4.2/§4.4) is:
//
//	system  OR  (space_id = caller_space AND (public OR owner = caller))
//
// The mine rule (doc §4.3) is owner = caller AND space_id = caller_space.
func (f ListFilter) buildWhere() (string, []any) {
	var clauses []string
	var args []any

	if f.MineOnly {
		clauses = append(clauses, "owner_uid = ? AND space_id = ?")
		args = append(args, f.CallerUID, f.SpaceID)
	} else if f.SystemOnly {
		// Admin surface: every system row across all Spaces, no caller scope.
		clauses = append(clauses, "visibility = 'system'")
	} else {
		clauses = append(clauses,
			"(visibility = 'system' OR (space_id = ? AND (visibility = 'public' OR owner_uid = ?)))")
		args = append(args, f.SpaceID, f.CallerUID)
	}

	clauses = append(clauses, "deleted_at IS NULL")

	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		// `category` in the keyword contract (doc §4.2) is the display NAME, but
		// rows store category_id (an opaque slug). Match the name through the
		// taxonomy so a keyword like "研发工具" hits, instead of comparing the raw id.
		clauses = append(clauses,
			"(name LIKE ? OR summary LIKE ? OR creator_name LIKE ? "+
				"OR category_id IN (SELECT id FROM expert_categories WHERE name LIKE ? AND deleted_at IS NULL))")
		like := "%" + escapeLike(kw) + "%"
		args = append(args, like, like, like, like)
	}

	appendIn := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		marks := make([]string, len(values))
		for i, value := range values {
			marks[i] = "?"
			args = append(args, value)
		}
		clauses = append(clauses, column+" IN ("+strings.Join(marks, ",")+")")
	}
	appendIn("category_id", f.Categories)
	appendIn("visibility", f.Visibilities)
	appendIn("created_by_type", f.CreatedByTypes)

	// Tag filter is AND across groups (every selected tag name must be present),
	// OR within a group (a name that resolved to multiple ids matches any).
	for _, ids := range f.TagIDGroups {
		addTagIDGroupCondition(&clauses, &args, ids)
	}

	return strings.Join(clauses, " AND "), args
}

func addTagIDGroupCondition(clauses *[]string, args *[]any, ids []int64) {
	if len(ids) == 0 {
		return
	}
	parts := make([]string, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		parts = append(parts, "JSON_CONTAINS(tags, ?)")
		*args = append(*args, strconv.FormatInt(id, 10))
	}
	if len(parts) > 0 {
		*clauses = append(*clauses, "("+strings.Join(parts, " OR ")+")")
	} else {
		// The group was non-empty but no id resolved to a valid tag (e.g. the
		// impossible-group sentinel the service appends when a tag name matches
		// nothing). AND-ing a false predicate preserves the intended empty
		// result instead of silently dropping the whole tag filter.
		*clauses = append(*clauses, "1 = 0")
	}
}
