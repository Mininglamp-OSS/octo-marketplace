package category

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// CategoryWithCount is a category row joined with its visible skill count.
type CategoryWithCount struct {
	ID         string
	Name       string
	IconKey    string
	SortOrder  int
	SkillCount int
}

// ListFilter holds parameters for listing categories with filtered skill counts.
type ListFilter struct {
	SpaceID            string
	UserID             string
	Query              string
	TagIDGroups        [][]int64
	TagFilterUnmatched bool
}

// ListWithCount returns all non-deleted categories with visible skill counts.
func (r *Repo) ListWithCount(ctx context.Context, f ListFilter) ([]CategoryWithCount, error) {
	joinConditions := []string{
		"s.category_id = c.id",
		"s.is_deleted = 0",
		`(
			s.visibility = 'public'
			OR (s.visibility = 'space' AND s.space_id = ?)
			OR (s.visibility = 'private' AND s.owner_id = ? AND s.space_id = ?)
		)`,
	}
	args := []interface{}{f.SpaceID, f.UserID, f.SpaceID}

	if strings.TrimSpace(f.Query) != "" {
		searchTerm := "%" + escapeLike(strings.TrimSpace(f.Query)) + "%"
		joinConditions = append(joinConditions, `(
			s.name LIKE ? OR s.display_name LIKE ?
		)`)
		args = append(args, searchTerm, searchTerm)
	}

	if f.TagFilterUnmatched {
		joinConditions = append(joinConditions, "1 = 0")
	} else {
		for _, ids := range f.TagIDGroups {
			addTagIDGroupCondition(&joinConditions, &args, ids)
		}
	}

	query := `
		SELECT c.id, c.name, c.icon_key, c.sort_order,
			COUNT(s.id) AS skill_count
		FROM categories c
		LEFT JOIN skills s ON ` + strings.Join(joinConditions, " AND ") + `
		WHERE c.deleted_at IS NULL
		GROUP BY c.id, c.name, c.icon_key, c.sort_order
		ORDER BY c.sort_order ASC, c.name ASC
	`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CategoryWithCount
	for rows.Next() {
		var cat CategoryWithCount
		if err := rows.Scan(&cat.ID, &cat.Name, &cat.IconKey, &cat.SortOrder, &cat.SkillCount); err != nil {
			return nil, err
		}
		result = append(result, cat)
	}
	return result, rows.Err()
}

func addTagIDGroupCondition(conditions *[]string, args *[]interface{}, ids []int64) {
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
		parts = append(parts, "JSON_CONTAINS(s.tags, ?)")
		*args = append(*args, strconv.FormatInt(id, 10))
	}
	if len(parts) > 0 {
		*conditions = append(*conditions, "("+strings.Join(parts, " OR ")+")")
	}
}

func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

// Exists checks whether a non-deleted category with the given ID exists.
func (r *Repo) Exists(ctx context.Context, id string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM categories WHERE id = ? AND deleted_at IS NULL", id).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	return count > 0, nil
}
