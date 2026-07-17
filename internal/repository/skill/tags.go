package skill

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// TagRow represents a Space-scoped skill tag.
type TagRow struct {
	SpaceID   string
	Name      string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ListTags returns tags visible to all members of the current Space.
func (r *Repo) ListTags(ctx context.Context, spaceID, query string, limit int) ([]TagRow, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	conditions := []string{"space_id = ?"}
	args := []interface{}{spaceID}
	if strings.TrimSpace(query) != "" {
		conditions = append(conditions, "name LIKE ?")
		args = append(args, "%"+escapeLike(strings.TrimSpace(query))+"%")
	}
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, `
		SELECT space_id, name, created_by, created_at, updated_at
		FROM skill_tags
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY updated_at DESC, name ASC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []TagRow
	for rows.Next() {
		var tag TagRow
		if err := rows.Scan(&tag.SpaceID, &tag.Name, &tag.CreatedBy, &tag.CreatedAt, &tag.UpdatedAt); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

type tagExec interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

func upsertTags(ctx context.Context, ex tagExec, spaceID, createdBy string, tags []string) error {
	for _, tag := range tags {
		if strings.TrimSpace(tag) == "" {
			continue
		}
		if _, err := ex.ExecContext(ctx, `
			INSERT INTO skill_tags (space_id, name, created_by)
			VALUES (?, ?, ?)
			ON DUPLICATE KEY UPDATE updated_at = CURRENT_TIMESTAMP
		`, spaceID, tag, createdBy); err != nil {
			return err
		}
	}
	return nil
}
