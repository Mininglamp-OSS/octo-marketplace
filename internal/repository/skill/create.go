package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// CreateParams holds the data needed to insert a new skill.
type CreateParams struct {
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
	SpaceID          string
	Visibility       model.Visibility
	Version          string
	ReadmeContent    string
	FileName         string
	FileURL          string
	FileSize         int64
	FileSHA256       string
	TagNames         []string
}

// Create inserts a new skill record.
func (r *Repo) Create(ctx context.Context, p CreateParams) (*SkillRow, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	query := `
		INSERT INTO skills (id, name, display_name, icon_url, source_skill_id, current_version_id,
			description, category_id, tags, owner_id, owner_name,
			space_id, visibility, version, readme_content, file_name, file_url, file_size,
			file_sha256, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = tx.ExecContext(ctx, query,
		p.ID, p.Name, p.DisplayName, p.IconURL, p.SourceSkillID, p.CurrentVersionID,
		p.Description, p.CategoryID, string(p.Tags),
		p.OwnerID, p.OwnerName, p.SpaceID, string(p.Visibility), p.Version,
		p.ReadmeContent, p.FileName, p.FileURL, p.FileSize, p.FileSHA256,
		now, now,
	)
	if err != nil {
		return nil, mapDuplicateName(err)
	}
	if err := upsertTags(ctx, tx, p.SpaceID, p.OwnerID, p.TagNames); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &SkillRow{
		ID:               p.ID,
		Name:             p.Name,
		DisplayName:      p.DisplayName,
		IconURL:          p.IconURL,
		SourceSkillID:    p.SourceSkillID,
		CurrentVersionID: p.CurrentVersionID,
		Description:      p.Description,
		CategoryID:       p.CategoryID,
		Tags:             p.Tags,
		OwnerID:          p.OwnerID,
		OwnerName:        p.OwnerName,
		SpaceID:          p.SpaceID,
		Visibility:       string(p.Visibility),
		Version:          p.Version,
		ReadmeContent:    p.ReadmeContent,
		FileName:         p.FileName,
		FileURL:          p.FileURL,
		FileSize:         p.FileSize,
		FileSHA256:       p.FileSHA256,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// CreateSkillAndConsumeTask creates a skill, inserts its initial version record,
// and marks the parse task as consumed — all within a single transaction,
// preventing duplicate skill creation.
func (r *Repo) CreateSkillAndConsumeTask(ctx context.Context, parseTaskID string, p CreateParams, ver *model.SkillVersion) (*SkillRow, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Mark parse task as consumed first (acts as a lock against duplicates)
	res, err := tx.ExecContext(ctx,
		"UPDATE parse_tasks SET status = 'consumed' WHERE id = ? AND status = 'success'",
		parseTaskID)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrParseTaskAlreadyConsumed
	}

	// Insert the skill
	now := time.Now().UTC()
	query := `
		INSERT INTO skills (id, name, display_name, icon_url, source_skill_id, current_version_id,
			description, category_id, tags, owner_id, owner_name,
			space_id, visibility, version, readme_content, file_name, file_url, file_size,
			file_sha256, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = tx.ExecContext(ctx, query,
		p.ID, p.Name, p.DisplayName, p.IconURL, p.SourceSkillID, p.CurrentVersionID,
		p.Description, p.CategoryID, string(p.Tags),
		p.OwnerID, p.OwnerName, p.SpaceID, string(p.Visibility), p.Version,
		p.ReadmeContent, p.FileName, p.FileURL, p.FileSize, p.FileSHA256,
		now, now,
	)
	if err != nil {
		return nil, mapDuplicateName(err)
	}

	// Insert the initial version record in the same transaction
	if ver != nil {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO skill_versions (id, skill_id, version, changelog, storage, changed_by)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			ver.ID, ver.SkillID, ver.Version, ver.Changelog, ver.Storage, ver.ChangedBy,
		)
		if err != nil {
			return nil, fmt.Errorf("insert version: %w", err)
		}
	}

	if err := upsertTags(ctx, tx, p.SpaceID, p.OwnerID, p.TagNames); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &SkillRow{
		ID:               p.ID,
		Name:             p.Name,
		DisplayName:      p.DisplayName,
		IconURL:          p.IconURL,
		SourceSkillID:    p.SourceSkillID,
		CurrentVersionID: p.CurrentVersionID,
		Description:      p.Description,
		CategoryID:       p.CategoryID,
		Tags:             p.Tags,
		OwnerID:          p.OwnerID,
		OwnerName:        p.OwnerName,
		SpaceID:          p.SpaceID,
		Visibility:       string(p.Visibility),
		Version:          p.Version,
		ReadmeContent:    p.ReadmeContent,
		FileName:         p.FileName,
		FileURL:          p.FileURL,
		FileSize:         p.FileSize,
		FileSHA256:       p.FileSHA256,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}
