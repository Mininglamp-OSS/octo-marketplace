package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// UpdateParams holds optional fields to update.
type UpdateParams struct {
	Name             *string
	DisplayName      *string
	IconURL          *string
	Description      *string
	CategoryID       *string
	Tags             json.RawMessage // nil means no change
	Visibility       *model.Visibility
	Version          *string
	ReadmeContent    *string
	FileName         *string
	FileURL          *string
	FileSize         *int64
	FileSHA256       *string
	CurrentVersionID *string
	TagNames         []string
}

// buildUpdateSets constructs the SET clause parts and args for an UPDATE query.
func buildUpdateSets(p UpdateParams) ([]string, []interface{}) {
	var sets []string
	var args []interface{}

	if p.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *p.Name)
	}
	if p.DisplayName != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *p.DisplayName)
	}
	if p.IconURL != nil {
		sets = append(sets, "icon_url = ?")
		args = append(args, *p.IconURL)
	}
	if p.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *p.Description)
	}
	if p.CategoryID != nil {
		sets = append(sets, "category_id = ?")
		args = append(args, *p.CategoryID)
	}
	if p.Tags != nil {
		sets = append(sets, "tags = ?")
		args = append(args, string(p.Tags))
	}
	if p.Visibility != nil {
		sets = append(sets, "visibility = ?")
		args = append(args, string(*p.Visibility))
	}
	if p.Version != nil {
		sets = append(sets, "version = ?")
		args = append(args, *p.Version)
	}
	if p.ReadmeContent != nil {
		sets = append(sets, "readme_content = ?")
		args = append(args, *p.ReadmeContent)
	}
	if p.FileName != nil {
		sets = append(sets, "file_name = ?")
		args = append(args, *p.FileName)
	}
	if p.FileURL != nil {
		sets = append(sets, "file_url = ?")
		args = append(args, *p.FileURL)
	}
	if p.FileSize != nil {
		sets = append(sets, "file_size = ?")
		args = append(args, *p.FileSize)
	}
	if p.FileSHA256 != nil {
		sets = append(sets, "file_sha256 = ?")
		args = append(args, *p.FileSHA256)
	}
	if p.CurrentVersionID != nil {
		sets = append(sets, "current_version_id = ?")
		args = append(args, *p.CurrentVersionID)
	}

	return sets, args
}

// joinStrings joins strings with a separator.
func joinStrings(s []string, sep string) string {
	return strings.Join(s, sep)
}

// Update updates the specified fields on a skill. Returns the number of affected rows.
func (r *Repo) Update(ctx context.Context, id string, p UpdateParams) (int64, error) {
	sets, args := buildUpdateSets(p)
	if len(sets) == 0 {
		return 0, nil
	}

	query := fmt.Sprintf("UPDATE skills SET %s WHERE id = ?", strings.Join(sets, ", "))
	args = append(args, id)

	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, mapDuplicateName(err)
	}
	return result.RowsAffected()
}

// UpdateWithTags updates a skill and syncs newly introduced tags into the
// Space-level tag index in one transaction.
func (r *Repo) UpdateWithTags(ctx context.Context, id, spaceID, ownerID string, p UpdateParams) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	sets, args := buildUpdateSets(p)
	var affected int64
	if len(sets) > 0 {
		query := fmt.Sprintf("UPDATE skills SET %s WHERE id = ?", strings.Join(sets, ", "))
		args = append(args, id)

		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, mapDuplicateName(err)
		}
		affected, err = result.RowsAffected()
		if err != nil {
			return 0, err
		}
	}
	if err := upsertTags(ctx, tx, spaceID, ownerID, p.TagNames); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}
