package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ListFilter holds parameters for listing skills.
type ListFilter struct {
	SpaceID    string
	UserID     string
	Query      string
	CategoryID string
	Tags       []string
	Cursor     string // format: "timestamp,id"
	Limit      int
	MineOnly   bool // if true, only return skills owned by UserID
}

// SkillRow represents a row from the skills table.
type SkillRow struct {
	ID            string
	Name          string
	DisplayName   string
	IconURL       string
	Description   string
	CategoryID    string
	Tags          json.RawMessage
	OwnerID       string
	OwnerName     string
	SpaceID       string
	Visibility    string
	Version       string
	ReadmeContent string
	FileName      string
	FileURL       string
	FileSize      int64
	FileSHA256    string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ListResult holds paginated skill results.
type ListResult struct {
	Items      []SkillRow
	NextCursor *string
}

// List returns paginated skills matching the filter.
func (r *Repo) List(ctx context.Context, f ListFilter) (*ListResult, error) {
	if f.Limit <= 0 {
		f.Limit = 20
	}
	if f.Limit > 50 {
		f.Limit = 50
	}

	var conditions []string
	var args []interface{}

	if f.MineOnly {
		conditions = append(conditions, "s.owner_id = ? AND s.space_id = ?")
		args = append(args, f.UserID, f.SpaceID)
	} else {
		// Visibility filter:
		// - public: visible to all members of the same space
		// - space: visible to members of the same space
		// - private: visible only to the owner within the same space
		conditions = append(conditions, `(
			(s.visibility = 'public' AND s.space_id = ?)
			OR (s.visibility = 'space' AND s.space_id = ?)
			OR (s.visibility = 'private' AND s.owner_id = ? AND s.space_id = ?)
		)`)
		args = append(args, f.SpaceID, f.SpaceID, f.UserID, f.SpaceID)
	}

	if f.CategoryID != "" {
		conditions = append(conditions, "s.category_id = ?")
		args = append(args, f.CategoryID)
	}

	if f.Query != "" {
		searchTerm := "%" + escapeLike(f.Query) + "%"
		conditions = append(conditions, `(
			s.name LIKE ? OR s.description LIKE ? OR s.owner_name LIKE ?
			OR JSON_SEARCH(s.tags, 'one', ?) IS NOT NULL
		)`)
		args = append(args, searchTerm, searchTerm, searchTerm, searchTerm)
	}

	for _, tag := range f.Tags {
		if strings.TrimSpace(tag) == "" {
			continue
		}
		conditions = append(conditions, "JSON_CONTAINS(s.tags, ?)")
		tagJSON, _ := json.Marshal(strings.TrimSpace(tag))
		args = append(args, string(tagJSON))
	}

	if f.Cursor != "" {
		cursorTime, cursorID, err := parseCursor(f.Cursor)
		if err == nil {
			conditions = append(conditions, "(s.created_at < ? OR (s.created_at = ? AND s.id < ?))")
			args = append(args, cursorTime, cursorTime, cursorID)
		}
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT s.id, s.name, s.display_name, s.icon_url, s.description, s.category_id, s.tags,
			s.owner_id, s.owner_name, s.space_id, s.visibility, s.version,
			s.readme_content, s.file_name, s.file_url, s.file_size, s.file_sha256,
			s.created_at, s.updated_at
		FROM skills s
		%s
		ORDER BY s.created_at DESC, s.id DESC
		LIMIT ?
	`, where)
	args = append(args, f.Limit+1) // fetch one extra to determine next_cursor

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []SkillRow
	for rows.Next() {
		var s SkillRow
		if err := rows.Scan(
			&s.ID, &s.Name, &s.DisplayName, &s.IconURL, &s.Description, &s.CategoryID, &s.Tags,
			&s.OwnerID, &s.OwnerName, &s.SpaceID, &s.Visibility, &s.Version,
			&s.ReadmeContent, &s.FileName, &s.FileURL, &s.FileSize, &s.FileSHA256,
			&s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := &ListResult{}
	if len(items) > f.Limit {
		items = items[:f.Limit]
		last := items[len(items)-1]
		cursor := buildCursor(last.CreatedAt, last.ID)
		result.NextCursor = &cursor
	}
	result.Items = items
	return result, nil
}

func parseCursor(cursor string) (time.Time, string, error) {
	parts := strings.SplitN(cursor, ",", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("invalid cursor format")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", err
	}
	return t, parts[1], nil
}

func buildCursor(t time.Time, id string) string {
	return t.UTC().Format(time.RFC3339Nano) + "," + id
}

// escapeLike neutralizes MySQL LIKE wildcards in user keywords so search uses
// literal substring semantics.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}
