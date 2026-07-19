package category

import (
	"context"
	"time"
)

// Create inserts a new category.
func (r *Repo) Create(ctx context.Context, id, name, iconKey string, sortOrder int) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO categories (id, name, icon_key, sort_order, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, iconKey, sortOrder, now, now)
	if err != nil {
		return mapCategoryDuplicateName(err)
	}
	return nil
}

// Update modifies an existing non-deleted category.
func (r *Repo) Update(ctx context.Context, id, name, iconKey string, sortOrder int) (int64, error) {
	result, err := r.db.ExecContext(ctx,
		`UPDATE categories SET name = ?, icon_key = ?, sort_order = ? WHERE id = ? AND deleted_at IS NULL`,
		name, iconKey, sortOrder, id)
	if err != nil {
		return 0, mapCategoryDuplicateName(err)
	}
	return result.RowsAffected()
}

// Delete soft-deletes a category by setting deleted_at.
func (r *Repo) Delete(ctx context.Context, id string) (int64, error) {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, "UPDATE categories SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL", now, id)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// SkillCountByCategory returns the number of skills in a given non-deleted category.
func (r *Repo) SkillCountByCategory(ctx context.Context, categoryID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM skills WHERE category_id = ?", categoryID).Scan(&count)
	return count, err
}

// GetByID returns a single non-deleted category by ID. Returns nil if not found.
func (r *Repo) GetByID(ctx context.Context, id string) (*CategoryRow, error) {
	var c CategoryRow
	err := r.db.QueryRowContext(ctx,
		"SELECT id, name, icon_key, sort_order, created_at, updated_at FROM categories WHERE id = ? AND deleted_at IS NULL", id).
		Scan(&c.ID, &c.Name, &c.IconKey, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CategoryRow represents a row from the categories table.
type CategoryRow struct {
	ID        string
	Name      string
	IconKey   string
	SortOrder int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AdminList returns all non-deleted categories (admin view, includes sort_order).
func (r *Repo) AdminList(ctx context.Context) ([]CategoryRow, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, name, icon_key, sort_order, created_at, updated_at FROM categories WHERE deleted_at IS NULL ORDER BY sort_order ASC, name ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []CategoryRow
	for rows.Next() {
		var c CategoryRow
		if err := rows.Scan(&c.ID, &c.Name, &c.IconKey, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
