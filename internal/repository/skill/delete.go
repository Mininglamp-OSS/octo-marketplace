package skill

import "context"

// Delete hard-deletes a skill by ID. Returns the number of affected rows.
func (r *Repo) Delete(ctx context.Context, id string) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM skill_versions WHERE skill_id = ?", id); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM skills WHERE id = ?", id)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}
