package category

import (
	"context"
	"database/sql"
)

// CategoryWithCount is a category row joined with its visible skill count.
type CategoryWithCount struct {
	ID         string
	Name       string
	IconKey    string
	SortOrder  int
	SkillCount int
}

// ListWithCount returns all non-deleted categories with skill counts visible to the given space and user.
func (r *Repo) ListWithCount(ctx context.Context, spaceID, userID string) ([]CategoryWithCount, error) {
	query := `
		SELECT c.id, c.name, c.icon_key, c.sort_order,
			COUNT(s.id) AS skill_count
		FROM categories c
		LEFT JOIN skills s ON s.category_id = c.id
			AND (
				(s.visibility = 'public' AND s.space_id = ?)
				OR (s.visibility = 'space' AND s.space_id = ?)
				OR (s.visibility = 'private' AND s.owner_id = ? AND s.space_id = ?)
			)
		WHERE c.deleted_at IS NULL
		GROUP BY c.id, c.name, c.icon_key, c.sort_order
		ORDER BY (COUNT(s.id) > 0) DESC, c.sort_order ASC, c.name ASC
	`
	rows, err := r.db.QueryContext(ctx, query, spaceID, spaceID, userID, spaceID)
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

// Exists checks whether a non-deleted category with the given ID exists.
func (r *Repo) Exists(ctx context.Context, id string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM categories WHERE id = ? AND deleted_at IS NULL", id).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	return count > 0, nil
}
