package plugin

import (
	"context"
	"database/sql"
	"errors"
)

// ErrEditConflict indicates a missing, stale, or exhausted editing revision.
// Public v2 adapters map it to CONFLICT with reason edit_conflict.
var ErrEditConflict = errors.New("plugin edit conflict")

// GetEditRevision only discloses editing state to the existing owner/admin scope.
// It is not a public catalog read and does not grant access by knowing an ID.
func (r *Repo) GetEditRevision(ctx context.Context, scope Scope, id string) (uint64, error) {
	query := `SELECT edit_revision FROM plugins WHERE plugin_id=? AND status=1 AND deleted_at IS NULL`
	args := []any{id}
	if !scope.Admin {
		query += ` AND owner_uid=? AND space_id=?`
		args = append(args, scope.CallerUID, scope.SpaceID)
	}
	var revision uint64
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, wrapped("read editing revision", err)
	}
	return revision, nil
}

// checkEditRevision runs after getOwnedForUpdate, under the same row lock as
// the mutation. Legacy adapters remain compatible until all v2 writers ship.
func checkEditRevision(ctx context.Context, tx *sql.Tx, id string, expected *uint64, required bool) (uint64, error) {
	if expected == nil {
		if required {
			return 0, ErrEditConflict
		}
		return 0, nil
	}
	var current uint64
	if err := tx.QueryRowContext(ctx, `SELECT edit_revision FROM plugins WHERE plugin_id=?`, id).Scan(&current); err != nil {
		return 0, wrapped("check editing revision", err)
	}
	if *expected == 0 || *expected != current || current == ^uint64(0) {
		return 0, ErrEditConflict
	}
	return current + 1, nil
}
