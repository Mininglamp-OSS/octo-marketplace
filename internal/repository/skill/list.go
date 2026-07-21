package skill

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Sort modes for skill listing.
const (
	SortComprehensive = "comprehensive"
	SortLatest        = "latest"
	SortDownloads     = "downloads"
	SortViews         = "views"
)

// ListFilter holds parameters for listing skills.
type ListFilter struct {
	SpaceID    string
	UserID     string
	Query      string
	CategoryID string
	Tags       []string
	Cursor     string // format: "timestamp,id" — used only with SortLatest
	Limit      int
	Offset     int    // used with comprehensive/downloads/views sort modes
	Sort       string // comprehensive, latest, downloads, views
	MineOnly   bool   // if true, only return skills owned by UserID
}

// SkillRow represents a row from the skills table.
type SkillRow struct {
	ID               string
	Name             string
	DisplayName      string
	IconURL          string
	SourceSkillID    string
	CurrentVersionID string
	Description      string
	CategoryID       string
	Tags             json.RawMessage
	OwnerID          string
	OwnerName        string
	CreatorID        string
	CreatorName      string
	SpaceID          string
	Visibility       string
	Version          string
	ReadmeContent    string
	FileName         string
	FileURL          string
	FileSize         int64
	FileSHA256       string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ResolvedVersion  string // version from skill_versions, falls back to skills.version
	VersionStorage   string // storage JSON from skill_versions
	ViewCount        int64
	DownloadCount    int64
}

// ListResult holds paginated skill results.
type ListResult struct {
	Items      []SkillRow
	NextCursor *string
	Total      int // total count for offset-based pagination (only set when using offset)
}

// List returns paginated skills matching the filter.
func (r *Repo) List(ctx context.Context, f ListFilter) (*ListResult, error) {
	if f.Limit <= 0 {
		f.Limit = 20
	}
	if f.Limit > 50 {
		f.Limit = 50
	}

	sort := f.Sort
	if sort == "" {
		sort = SortComprehensive
	}

	var conditions []string
	var args []interface{}

	conditions = append(conditions, "s.is_deleted = 0")

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
			s.name LIKE ? OR s.description LIKE ? OR s.owner_name LIKE ? OR s.creator_name LIKE ?
			OR JSON_SEARCH(s.tags, 'one', ?) IS NOT NULL
		)`)
		args = append(args, searchTerm, searchTerm, searchTerm, searchTerm, searchTerm)
	}

	for _, tag := range f.Tags {
		if strings.TrimSpace(tag) == "" {
			continue
		}
		conditions = append(conditions, "JSON_CONTAINS(s.tags, ?)")
		tagJSON, _ := json.Marshal(strings.TrimSpace(tag))
		args = append(args, string(tagJSON))
	}

	// For "latest" sort mode, use cursor-based pagination
	if sort == SortLatest && f.Cursor != "" {
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

	// Build ORDER BY and pagination based on sort mode
	var orderBy string
	switch sort {
	case SortLatest:
		orderBy = "ORDER BY s.created_at DESC, s.id DESC"
	case SortDownloads:
		orderBy = "ORDER BY COALESCE(rm.download_count, 0) DESC, s.created_at DESC, s.id DESC"
	case SortViews:
		orderBy = "ORDER BY COALESCE(rm.view_count, 0) DESC, s.created_at DESC, s.id DESC"
	default: // SortComprehensive
		orderBy = `ORDER BY (COALESCE(rm.download_count, 0) * 5
			+ COALESCE(rm.view_count, 0) * 1
			+ 20 / POW(TIMESTAMPDIFF(HOUR, s.created_at, NOW()) / 24 + 2, 1.2)) DESC,
			s.created_at DESC, s.id DESC`
	}

	selectCols := `s.id, s.name, s.display_name, s.icon_url, s.source_skill_id, s.current_version_id,
		s.description, s.category_id, s.tags,
		s.owner_id, s.owner_name, s.creator_id, s.creator_name, s.space_id, s.visibility, s.version,
		s.readme_content, s.file_name, s.file_url, s.file_size, s.file_sha256,
		s.created_at, s.updated_at,
		COALESCE(v.version, s.version) AS resolved_version,
		COALESCE(v.storage, '') AS version_storage,
		COALESCE(rm.view_count, 0), COALESCE(rm.download_count, 0)`

	join := `LEFT JOIN skill_versions v ON v.id = s.current_version_id
		LEFT JOIN resource_metrics rm ON rm.resource_type = 'skill' AND rm.resource_id = s.id`

	if sort == SortLatest {
		// Cursor-based pagination
		query := fmt.Sprintf(`
			SELECT %s
			FROM skills s
			%s
			%s
			%s
			LIMIT ?
		`, selectCols, join, where, orderBy)
		args = append(args, f.Limit+1)

		return r.queryListResult(ctx, query, args, f.Limit, true)
	}

	// Offset-based pagination for comprehensive/downloads/views
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	// Count total
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM skills s %s %s`, join, where)
	var total int
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, err
	}

	query := fmt.Sprintf(`
		SELECT %s
		FROM skills s
		%s
		%s
		%s
		LIMIT ? OFFSET ?
	`, selectCols, join, where, orderBy)
	args = append(args, f.Limit, offset)

	result, err := r.queryListResult(ctx, query, args, f.Limit, false)
	if err != nil {
		return nil, err
	}
	result.Total = total
	return result, nil
}

// queryListResult executes the query and returns a ListResult.
// If useCursor is true, it uses the extra-row method to determine the next cursor.
func (r *Repo) queryListResult(ctx context.Context, query string, args []interface{}, limit int, useCursor bool) (*ListResult, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []SkillRow
	for rows.Next() {
		var s SkillRow
		if err := scanSkillRow(rows, &s); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := &ListResult{}
	if useCursor && len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		cursor := buildCursor(last.CreatedAt, last.ID)
		result.NextCursor = &cursor
	}
	result.Items = items
	return result, nil
}

func scanSkillRow(rows *sql.Rows, s *SkillRow) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	if len(cols) == 25 {
		if err := rows.Scan(
			&s.ID, &s.Name, &s.DisplayName, &s.IconURL, &s.SourceSkillID, &s.CurrentVersionID,
			&s.Description, &s.CategoryID, &s.Tags,
			&s.OwnerID, &s.OwnerName, &s.SpaceID, &s.Visibility, &s.Version,
			&s.ReadmeContent, &s.FileName, &s.FileURL, &s.FileSize, &s.FileSHA256,
			&s.CreatedAt, &s.UpdatedAt,
			&s.ResolvedVersion, &s.VersionStorage, &s.ViewCount, &s.DownloadCount,
		); err != nil {
			return err
		}
		s.CreatorID = s.OwnerID
		s.CreatorName = s.OwnerName
		return nil
	}
	return rows.Scan(
		&s.ID, &s.Name, &s.DisplayName, &s.IconURL, &s.SourceSkillID, &s.CurrentVersionID,
		&s.Description, &s.CategoryID, &s.Tags,
		&s.OwnerID, &s.OwnerName, &s.CreatorID, &s.CreatorName, &s.SpaceID, &s.Visibility, &s.Version,
		&s.ReadmeContent, &s.FileName, &s.FileURL, &s.FileSize, &s.FileSHA256,
		&s.CreatedAt, &s.UpdatedAt,
		&s.ResolvedVersion, &s.VersionStorage, &s.ViewCount, &s.DownloadCount,
	)
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
