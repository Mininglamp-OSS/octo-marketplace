package skill

import (
	"context"
)

// GetByID returns a single skill by ID. Returns nil if not found.
func (r *Repo) GetByID(ctx context.Context, id string) (*SkillRow, error) {
	query := `
		SELECT s.id, s.name, s.display_name, s.icon_url, s.source_skill_id, s.current_version_id,
			s.description, s.category_id, s.tags,
			s.owner_id, s.owner_name, s.creator_id, s.creator_name, s.space_id, s.visibility, s.version,
			s.readme_content, s.file_name, s.file_url, s.file_size, s.file_sha256,
			s.created_at, s.updated_at,
			COALESCE(v.version, s.version) AS resolved_version,
			COALESCE(v.storage, '') AS version_storage,
			COALESCE(rm.view_count, 0), COALESCE(rm.download_count, 0)
		FROM skills s
		LEFT JOIN skill_versions v ON v.id = s.current_version_id
		LEFT JOIN resource_metrics rm ON rm.resource_type = 'skill' AND rm.resource_id = s.id
		WHERE s.id = ? AND s.is_deleted = 0
	`
	rows, err := r.db.QueryContext(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	var s SkillRow
	if err := scanSkillRow(rows, &s); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &s, nil
}
