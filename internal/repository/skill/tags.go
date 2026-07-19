package skill

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GlobalTagSpaceID is the shared tag bucket used by administrator-created
// public Skills. The column is NOT NULL, so an empty string is used instead of
// SQL NULL.
const GlobalTagSpaceID = ""

// TagRow represents a Space-scoped skill tag.
type TagRow struct {
	SpaceID   string
	Name      string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ListTags returns tags visible to all members of the current Space, including
// administrator-created global tags. When both scopes contain the same tag
// name, the Space-local row wins so its metadata is returned.
func (r *Repo) ListTags(ctx context.Context, spaceID, query string, limit int) ([]TagRow, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	conditions := []string{"space_id IN (?, ?)"}
	args := []interface{}{spaceID, GlobalTagSpaceID}
	if strings.TrimSpace(query) != "" {
		conditions = append(conditions, "name LIKE ?")
		args = append(args, "%"+escapeLike(strings.TrimSpace(query))+"%")
	}
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, `
		SELECT ranked.space_id, ranked.name, ranked.created_by, ranked.created_at, ranked.updated_at
		FROM (
			SELECT
				space_id, name, created_by, created_at, updated_at,
				ROW_NUMBER() OVER (
					PARTITION BY name
					ORDER BY CASE WHEN space_id = ? THEN 0 ELSE 1 END, updated_at DESC
				) AS rn
			FROM skill_tags
			WHERE `+strings.Join(conditions, " AND ")+`
		) AS ranked
		WHERE ranked.rn = 1
		ORDER BY ranked.updated_at DESC, ranked.name ASC
		LIMIT ?
	`, append([]interface{}{spaceID}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
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
