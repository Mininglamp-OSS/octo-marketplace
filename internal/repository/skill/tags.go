package skill

import (
	"context"
	"database/sql"
	"encoding/json"
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
	ID        int64
	SpaceID   string
	Name      string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ListTags returns tags visible to all members of the current Space, including
// administrator-created global tags. When both scopes contain the same tag
// name, the global row wins so its metadata is returned.
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
		SELECT ranked.id, ranked.space_id, ranked.name, ranked.created_by, ranked.created_at, ranked.updated_at
		FROM (
			SELECT
				id, space_id, name, created_by, created_at, updated_at,
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
	`, append([]interface{}{GlobalTagSpaceID}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var tags []TagRow
	for rows.Next() {
		var tag TagRow
		if err := rows.Scan(&tag.ID, &tag.SpaceID, &tag.Name, &tag.CreatedBy, &tag.CreatedAt, &tag.UpdatedAt); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

type tagExec interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
}

func resolveOrCreateTagIDs(ctx context.Context, ex tagExec, spaceID, createdBy string, tags []string) ([]int64, error) {
	ids := make([]int64, 0, len(tags))
	seen := make(map[int64]struct{}, len(tags))
	for _, tag := range normalizeTags(tags) {
		id, err := resolveTagID(ctx, ex, spaceID, tag)
		if err != nil {
			return nil, err
		}
		if id == 0 {
			id, err = insertTag(ctx, ex, spaceID, createdBy, tag)
			if err != nil {
				return nil, err
			}
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func resolveTagID(ctx context.Context, ex tagExec, spaceID, tag string) (int64, error) {
	var id int64
	err := ex.QueryRowContext(ctx, `
		SELECT id
		FROM skill_tags
		WHERE name = ? AND space_id IN (?, ?)
		ORDER BY CASE WHEN space_id = ? THEN 0 ELSE 1 END
		LIMIT 1
	`, tag, GlobalTagSpaceID, spaceID, GlobalTagSpaceID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

func insertTag(ctx context.Context, ex tagExec, spaceID, createdBy, tag string) (int64, error) {
	if _, err := ex.ExecContext(ctx, `
		INSERT INTO skill_tags (space_id, name, created_by)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE updated_at = CURRENT_TIMESTAMP
	`, spaceID, tag, createdBy); err != nil {
		return 0, err
	}
	return resolveTagID(ctx, ex, spaceID, tag)
}

func tagIDsToRaw(ids []int64) (json.RawMessage, error) {
	if ids == nil {
		return nil, nil
	}
	if len(ids) == 0 {
		return json.RawMessage(`[]`), nil
	}
	out, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func (r *Repo) ResolveFilterTagIDs(ctx context.Context, spaceID string, tags []string) ([][]int64, error) {
	groups := make([][]int64, 0, len(tags))
	for _, tag := range normalizeTags(tags) {
		conditions := "name = ?"
		args := []interface{}{tag}
		if spaceID != GlobalTagSpaceID {
			conditions += " AND space_id IN (?, ?)"
			args = append(args, GlobalTagSpaceID, spaceID)
		}
		args = append(args, GlobalTagSpaceID)
		rows, err := r.db.QueryContext(ctx, `
			SELECT id
			FROM skill_tags
			WHERE `+conditions+`
			ORDER BY CASE WHEN space_id = ? THEN 0 ELSE 1 END
		`, args...)
		if err != nil {
			return nil, err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if len(ids) > 0 {
			groups = append(groups, ids)
		}
	}
	return groups, nil
}

func (r *Repo) ResolveTagNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	names := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name
		FROM skill_tags
		WHERE id IN (`+strings.Join(placeholders, ",")+`)
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve tag names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}
