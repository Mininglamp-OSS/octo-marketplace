package parse

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// TaskRow represents a row from the parse_tasks table.
type TaskRow struct {
	ID                string
	UploadID          string
	FileName          string
	FileSize          int64
	FileURL           string
	Status            string
	ErrorCode         string
	ErrorMessage      string
	ResultName        string
	ResultDescription *string
	ResultVersion     string
	ResultTags        json.RawMessage
	ResultReadme      *string
	FileSHA256        string
	OwnerID           string
	SpaceID           string
	SkillID           string // empty for new upload, non-empty for reupload
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Repo provides data access for parse tasks.
type Repo struct {
	db *sql.DB
}

// NewRepo creates a new parse task repository.
func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// Create inserts a new parse task.
func (r *Repo) Create(ctx context.Context, t *TaskRow) error {
	query := `
		INSERT INTO parse_tasks (id, upload_id, file_name, file_size, file_url, status, owner_id, space_id, skill_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		t.ID, t.UploadID, t.FileName, t.FileSize, t.FileURL,
		t.Status, t.OwnerID, t.SpaceID, t.SkillID,
	)
	return err
}

// GetByID fetches a parse task by ID.
func (r *Repo) GetByID(ctx context.Context, id string) (*TaskRow, error) {
	query := `
		SELECT id, upload_id, file_name, file_size, file_url, status,
			error_code, error_message,
			result_name, result_description, result_version, COALESCE(result_tags, '[]'), result_readme,
			file_sha256, owner_id, space_id, skill_id, created_at, updated_at
		FROM parse_tasks
		WHERE id = ?
	`
	var t TaskRow
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&t.ID, &t.UploadID, &t.FileName, &t.FileSize, &t.FileURL, &t.Status,
		&t.ErrorCode, &t.ErrorMessage,
		&t.ResultName, &t.ResultDescription, &t.ResultVersion, &t.ResultTags, &t.ResultReadme,
		&t.FileSHA256, &t.OwnerID, &t.SpaceID, &t.SkillID, &t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetByUploadID fetches a parse task by upload_id.
func (r *Repo) GetByUploadID(ctx context.Context, uploadID string) (*TaskRow, error) {
	query := `
		SELECT id, upload_id, file_name, file_size, file_url, status,
			error_code, error_message,
			result_name, result_description, result_version, COALESCE(result_tags, '[]'), result_readme,
			file_sha256, owner_id, space_id, skill_id, created_at, updated_at
		FROM parse_tasks
		WHERE upload_id = ?
		ORDER BY created_at DESC
		LIMIT 1
	`
	var t TaskRow
	err := r.db.QueryRowContext(ctx, query, uploadID).Scan(
		&t.ID, &t.UploadID, &t.FileName, &t.FileSize, &t.FileURL, &t.Status,
		&t.ErrorCode, &t.ErrorMessage,
		&t.ResultName, &t.ResultDescription, &t.ResultVersion, &t.ResultTags, &t.ResultReadme,
		&t.FileSHA256, &t.OwnerID, &t.SpaceID, &t.SkillID, &t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateStatus sets the parse task status.
func (r *Repo) UpdateStatus(ctx context.Context, id, status string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE parse_tasks SET status = ? WHERE id = ?", status, id)
	return err
}

// TransitionPendingToParsing atomically flips a task from pending to parsing.
// It returns false when another caller already consumed the pending state.
func (r *Repo) TransitionPendingToParsing(ctx context.Context, id string) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		"UPDATE parse_tasks SET status = 'parsing' WHERE id = ? AND status = 'pending'", id)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

// UpdateFailed sets the parse task to failed with error info.
func (r *Repo) UpdateFailed(ctx context.Context, id, errorCode, errorMessage string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE parse_tasks SET status = 'failed', error_code = ?, error_message = ? WHERE id = ?",
		errorCode, errorMessage, id)
	return err
}

// UpdateSuccess sets the parse task to success with the parsed results.
func (r *Repo) UpdateSuccess(ctx context.Context, id string, name string, description *string, version string, tags json.RawMessage, readme *string, sha256 string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE parse_tasks SET status = 'success',
			result_name = ?, result_description = ?, result_version = ?,
			result_tags = ?, result_readme = ?, file_sha256 = ?
		WHERE id = ?`,
		name, description, version, tags, readme, sha256, id)
	return err
}
