package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// AdminListFilter holds parameters for admin-level skill listing.
type AdminListFilter struct {
	Query      string
	CategoryID string
	Tags       []string
	Limit      int
	Offset     int
	Sort       string
}

// AdminList returns paginated public skills without Space restriction.
func (r *Repo) AdminList(ctx context.Context, f AdminListFilter) (*ListResult, error) {
	if f.Limit <= 0 {
		f.Limit = 20
	}
	if f.Limit > 50 {
		f.Limit = 50
	}

	sort := f.Sort
	if sort == "" {
		sort = SortLatest
	}

	var conditions []string
	var args []interface{}

	conditions = append(conditions, "s.visibility = 'public'")

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

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

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

// AdminConsumeParseTask marks a parse task as consumed without owner/space checks.
// Only requires the task to have status='success'.
func (r *Repo) AdminConsumeParseTask(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE parse_tasks SET status = 'consumed' WHERE id = ? AND status = 'success'", id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrParseTaskAlreadyConsumed
	}
	return nil
}

// AdminUpdateSkillAndConsumeTask updates a skill, inserts a new version record,
// and marks the parse task as consumed within a single transaction without
// owner/space checks on the parse task (admin-only).
func (r *Repo) AdminUpdateSkillAndConsumeTask(ctx context.Context, skillID string, p UpdateParams, parseTaskID string, ver *model.SkillVersion) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Consume parse task first (acts as a lock, no owner/space check)
	res, err := tx.ExecContext(ctx,
		"UPDATE parse_tasks SET status = 'consumed' WHERE id = ? AND status = 'success'",
		parseTaskID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrParseTaskAlreadyConsumed
	}

	// Build and execute the skill update
	sets, args := buildUpdateSets(p)
	if len(sets) > 0 {
		query := "UPDATE skills SET " + joinStrings(sets, ", ") + " WHERE id = ?"
		args = append(args, skillID)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return mapDuplicateName(err)
		}
	}

	// Insert the new version record in the same transaction
	if ver != nil {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO skill_versions (id, skill_id, version, changelog, storage, changed_by)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			ver.ID, ver.SkillID, ver.Version, ver.Changelog, ver.Storage, ver.ChangedBy,
		)
		if err != nil {
			return fmt.Errorf("insert version: %w", err)
		}
	}

	return tx.Commit()
}
