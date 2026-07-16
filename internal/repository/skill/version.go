package skill

import (
	"context"
	"database/sql"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// InsertVersion inserts a version record into skill_versions.
func (r *Repo) InsertVersion(ctx context.Context, v model.SkillVersion) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO skill_versions (id, skill_id, version, changelog, storage, changed_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		v.ID, v.SkillID, v.Version, v.Changelog, v.Storage, v.ChangedBy,
	)
	return err
}

// VersionRow is the DB row representation of a skill version.
type VersionRow struct {
	ID        string
	SkillID   string
	Version   string
	Changelog string
	Storage   string // JSON string
	ChangedBy string
	CreatedAt time.Time
}

// ListVersions returns all versions for a skill, ordered by created_at DESC.
func (r *Repo) ListVersions(ctx context.Context, skillID string) ([]VersionRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, skill_id, version, changelog, storage, changed_by, created_at
		 FROM skill_versions WHERE skill_id = ? ORDER BY created_at DESC`,
		skillID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []VersionRow
	for rows.Next() {
		var row VersionRow
		var changelog, storage sql.NullString
		if err := rows.Scan(
			&row.ID, &row.SkillID, &row.Version, &changelog,
			&storage, &row.ChangedBy, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Changelog = changelog.String
		row.Storage = storage.String
		result = append(result, row)
	}
	return result, rows.Err()
}
