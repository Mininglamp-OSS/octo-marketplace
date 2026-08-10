package expert

import (
	"context"
	"time"
)

// DeleteExpert soft-deletes a live expert (sets deleted_at). The generated
// name_live column drops to NULL so the name frees up for reuse (doc §4.6).
// Returns ErrNotFound when the row is already gone. Ownership is checked by the
// service first; the owner_uid scope predicate keeps the invariant local.
func (r *Repo) DeleteExpert(ctx context.Context, id, ownerUID string, now time.Time) error {
	return r.softDelete(ctx, "experts", id, ownerUID, now)
}

// DeleteSquad soft-deletes a live squad.
func (r *Repo) DeleteSquad(ctx context.Context, id, ownerUID string, now time.Time) error {
	return r.softDelete(ctx, "expert_squads", id, ownerUID, now)
}

func (r *Repo) softDelete(ctx context.Context, table, id, ownerUID string, now time.Time) error {
	q := `UPDATE ` + table + ` SET deleted_at = ?, updated_at = ? WHERE id = ? AND owner_uid = ? AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, now, now, id, ownerUID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
