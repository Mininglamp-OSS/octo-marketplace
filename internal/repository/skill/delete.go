package skill

import "context"

// Delete soft-deletes a live skill by ID. Returns the number of affected rows.
func (r *Repo) Delete(ctx context.Context, id string) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE skills
		SET is_deleted = 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND is_deleted = 0
	`, id)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
