package skill

import (
	"context"
	"database/sql"
	"encoding/json"
)

// ParseTaskRow holds parse_task data needed for skill creation.
type ParseTaskRow struct {
	ID                string
	UploadID          string
	FileName          string
	FileSize          int64
	FileURL           string
	FileSHA256        string
	Status            string
	ResultName        string
	ResultDescription *string
	ResultVersion     string
	ResultTags        json.RawMessage
	ResultReadme      *string
	OwnerID           string
	SpaceID           string
	SkillID           string
}

// GetParseTask retrieves a parse task by ID.
func (r *Repo) GetParseTask(ctx context.Context, id string) (*ParseTaskRow, error) {
	query := `
		SELECT id, upload_id, file_name, file_size, file_url, file_sha256, status,
			result_name, result_description, result_version, result_tags, result_readme,
			owner_id, space_id, skill_id
		FROM parse_tasks
		WHERE id = ?
	`
	var pt ParseTaskRow
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&pt.ID, &pt.UploadID, &pt.FileName, &pt.FileSize, &pt.FileURL, &pt.FileSHA256,
		&pt.Status, &pt.ResultName, &pt.ResultDescription, &pt.ResultVersion,
		&pt.ResultTags, &pt.ResultReadme, &pt.OwnerID, &pt.SpaceID, &pt.SkillID,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pt, nil
}

// MarkParseTaskConsumed marks a parse task as consumed (changes status to prevent reuse).
// It uses a conditional update with status='success' and ownership checks to prevent
// duplicate consumption and race conditions. Returns ErrParseTaskAlreadyConsumed if
// the task was already consumed or doesn't match the criteria.
func (r *Repo) MarkParseTaskConsumed(ctx context.Context, id, ownerID, spaceID, skillID string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE parse_tasks SET status = 'consumed'
		 WHERE id = ? AND status = 'success' AND owner_id = ? AND space_id = ? AND skill_id = ?`,
		id, ownerID, spaceID, skillID)
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

// UpdateSkillAndConsumeTask updates a skill and marks the parse task as consumed
// within a single transaction, preventing duplicate reupload consumption.
func (r *Repo) UpdateSkillAndConsumeTask(ctx context.Context, skillID string, p UpdateParams, parseTaskID, ownerID, spaceID, taskSkillID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Consume parse task first (acts as a lock)
	res, err := tx.ExecContext(ctx,
		`UPDATE parse_tasks SET status = 'consumed'
		 WHERE id = ? AND status = 'success' AND owner_id = ? AND space_id = ? AND skill_id = ?`,
		parseTaskID, ownerID, spaceID, taskSkillID)
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
	if len(sets) == 0 {
		return tx.Commit()
	}

	query := "UPDATE skills SET " + joinStrings(sets, ", ") + " WHERE id = ?"
	args = append(args, skillID)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return mapDuplicateName(err)
	}
	if err := upsertTags(ctx, tx, spaceID, ownerID, p.TagNames); err != nil {
		return err
	}

	return tx.Commit()
}
