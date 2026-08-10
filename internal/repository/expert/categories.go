package expert

import (
	"context"
	"database/sql"
	"strings"
)

// CategoryCount is one category row joined with the number of records of the
// selected kind that are visible to the caller (doc §5). It backs
// GET /expert_categories.
type CategoryCount struct {
	ID    string
	Name  string
	Count int
}

// CategoryExists reports whether a live expert_categories row with the given id
// exists.
func (r *Repo) CategoryExists(ctx context.Context, id string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM expert_categories WHERE id = ? AND deleted_at IS NULL LIMIT 1`, id).
		Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// CategoryIDByName resolves a category NAME to its id, returning "" when no live
// category carries that name. The wire carries names; the service uses this to
// map an incoming name to the stored category_id on write.
func (r *Repo) CategoryIDByName(ctx context.Context, name string) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx,
		`SELECT id FROM expert_categories WHERE name = ? AND deleted_at IS NULL LIMIT 1`,
		strings.TrimSpace(name)).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// CategoryNamesByIDs maps a set of category ids back to their names, dropping
// ids with no live category. The service uses it to resolve stored category_ids
// to the NAME the wire exposes on read (single detail or a whole list page).
func (r *Repo) CategoryNamesByIDs(ctx context.Context, ids []string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	uniq := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return names, nil
	}
	placeholders := make([]string, len(uniq))
	args := make([]any, len(uniq))
	for i, id := range uniq {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name FROM expert_categories
		 WHERE id IN (`+strings.Join(placeholders, ",")+`) AND deleted_at IS NULL`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

// ListCategoriesWithCount returns every live category with the number of records
// of the given kind (experts for kind=agent, expert_squads for kind=squad) that
// are VISIBLE to the caller in their Space, using the same visible-set rule as
// the list endpoints (doc §5): system OR (space + (public OR owner)),
// soft-deleted excluded. Categories with no visible records report count 0. Rows
// are ordered by sort_order.
func (r *Repo) ListCategoriesWithCount(ctx context.Context, kind Entity, spaceID, ownerUID string) ([]CategoryCount, error) {
	table := "experts"
	if kind == EntitySquad {
		table = "expert_squads"
	}
	q := `SELECT ec.id, ec.name, COUNT(t.id) AS cnt
		FROM expert_categories ec
		LEFT JOIN ` + table + ` t
			ON t.category_id = ec.id
			AND t.deleted_at IS NULL
			AND (t.visibility = 'system' OR (t.space_id = ? AND (t.visibility = 'public' OR t.owner_uid = ?)))
		WHERE ec.deleted_at IS NULL
		GROUP BY ec.id, ec.name, ec.sort_order
		ORDER BY ec.sort_order ASC, ec.name ASC`

	rows, err := r.db.QueryContext(ctx, q, spaceID, ownerUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CategoryCount
	for rows.Next() {
		var c CategoryCount
		if err := rows.Scan(&c.ID, &c.Name, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
