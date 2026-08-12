package expert

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// This file holds the administrator (SuperAdmin) persistence surface for the
// Expert Marketplace: mutating platform-provided (visibility=system) experts
// and squads keyed by id (bypassing the owner/space scope the public path
// enforces), the system-scoped name-uniqueness pre-check the DB unique index
// can't provide for NULL-space rows, and full CRUD over the expert_categories
// taxonomy. Everything here is reachable only from the /api/v1/admin/* routes.

// Category-management sentinels for the admin taxonomy surface.
var (
	// ErrCategoryNameTaken is returned by Create/Update when another live
	// category already carries the given name (uq_expert_categories_name_live).
	ErrCategoryNameTaken = errors.New("expert category name taken")
	// ErrCategoryInUse is returned by DeleteExpertCategory when live experts or
	// squads still reference the category.
	ErrCategoryInUse = errors.New("expert category in use")
)

// CategoryAdminRow is one row of the admin category listing: the stored
// category plus the number of live records (experts + squads, any visibility)
// referencing it, so the console can warn before a delete.
type CategoryAdminRow struct {
	ID        string
	Name      string
	IconKey   string
	SortOrder int
	Count     int
}

// UpdateSystemExpert replaces the mutable columns of a system expert keyed by
// id alone (no owner/space scope, and only visibility=system rows). Mirrors
// UpdateExpert otherwise. A rename colliding with another live row surfaces as
// ErrNameTaken; a gone/non-system row is ErrNotFound.
func (r *Repo) UpdateSystemExpert(ctx context.Context, m *model.Expert) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tagIDs, err := resolveOrCreateTagIDs(ctx, tx, m.SpaceID, m.OwnerUID, m.Tags)
	if err != nil {
		return err
	}
	tagsRaw, err := tagIDsToRaw(tagIDs)
	if err != nil {
		return err
	}
	skills, err := marshalJSON(nonNilSkills(m.Skills))
	if err != nil {
		return err
	}

	const q = `UPDATE experts SET
		short_name = ?, name = ?, summary = ?, category_id = ?, tags = ?, publisher = ?,
		instruction = ?, mcp_config = ?, skills_json = ?, updated_at = ?
		WHERE id = ? AND visibility = 'system' AND deleted_at IS NULL`
	res, err := tx.ExecContext(ctx, q,
		m.ShortName, m.Name, m.Summary, m.Category, string(tagsRaw), m.Publisher,
		m.Instruction, m.MCPConfig, string(skills), m.UpdatedAt, m.ID,
	)
	if err != nil {
		return mapDuplicateName(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// UpdateSystemSquad is the squad twin of UpdateSystemExpert.
func (r *Repo) UpdateSystemSquad(ctx context.Context, m *model.Squad) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tagIDs, err := resolveOrCreateTagIDs(ctx, tx, m.SpaceID, m.OwnerUID, m.Tags)
	if err != nil {
		return err
	}
	tagsRaw, err := tagIDsToRaw(tagIDs)
	if err != nil {
		return err
	}
	strategies, dependencies, members, err := marshalSquadPayload(m)
	if err != nil {
		return err
	}

	const q = `UPDATE expert_squads SET
		short_name = ?, name = ?, summary = ?, category_id = ?, tags = ?, publisher = ?,
		leader = ?, strategies_json = ?, dependencies_json = ?, permission = ?,
		members_json = ?, updated_at = ?
		WHERE id = ? AND visibility = 'system' AND deleted_at IS NULL`
	res, err := tx.ExecContext(ctx, q,
		m.ShortName, m.Name, m.Summary, m.Category, string(tagsRaw), m.Publisher,
		m.Leader, string(strategies), string(dependencies), m.Permission,
		string(members), m.UpdatedAt, m.ID,
	)
	if err != nil {
		return mapDuplicateName(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// DeleteSystemExpert soft-deletes a system expert by id (no owner/space scope).
func (r *Repo) DeleteSystemExpert(ctx context.Context, id string, now time.Time) error {
	return r.softDeleteSystem(ctx, "experts", id, now)
}

// DeleteSystemSquad soft-deletes a system squad by id.
func (r *Repo) DeleteSystemSquad(ctx context.Context, id string, now time.Time) error {
	return r.softDeleteSystem(ctx, "expert_squads", id, now)
}

func (r *Repo) softDeleteSystem(ctx context.Context, table, id string, now time.Time) error {
	q := `UPDATE ` + table + ` SET deleted_at = ?, updated_at = ? WHERE id = ? AND visibility = 'system' AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, now, now, id)
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

// SystemExpertNameExists reports whether another live system expert already
// carries name (excluding excludeID). The DB unique index keys on
// (owner_uid, space_id, name_live), but system rows store space_id NULL — and
// NULLs never collide in a MySQL unique index — so the service needs this
// explicit pre-check to keep system names unique across the platform.
func (r *Repo) SystemExpertNameExists(ctx context.Context, name, excludeID string) (bool, error) {
	return r.systemNameExists(ctx, "experts", name, excludeID)
}

// SystemSquadNameExists is the squad twin of SystemExpertNameExists.
func (r *Repo) SystemSquadNameExists(ctx context.Context, name, excludeID string) (bool, error) {
	return r.systemNameExists(ctx, "expert_squads", name, excludeID)
}

func (r *Repo) systemNameExists(ctx context.Context, table, name, excludeID string) (bool, error) {
	q := `SELECT 1 FROM ` + table +
		` WHERE visibility = 'system' AND name = ? AND deleted_at IS NULL AND id <> ? LIMIT 1`
	var one int
	err := r.db.QueryRowContext(ctx, q, strings.TrimSpace(name), excludeID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ─── Category taxonomy CRUD (admin) ──────────────────────────────────────────

// ListExpertCategoriesAdmin returns every live category with a combined count
// of the live experts + squads (any visibility) referencing it, ordered by
// sort_order then name.
func (r *Repo) ListExpertCategoriesAdmin(ctx context.Context) ([]CategoryAdminRow, error) {
	const q = `SELECT ec.id, ec.name, ec.icon_key, ec.sort_order,
		(SELECT COUNT(*) FROM experts e WHERE e.category_id = ec.id AND e.deleted_at IS NULL)
		+ (SELECT COUNT(*) FROM expert_squads s WHERE s.category_id = ec.id AND s.deleted_at IS NULL) AS cnt
		FROM expert_categories ec
		WHERE ec.deleted_at IS NULL
		ORDER BY ec.sort_order ASC, ec.name ASC`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategoryAdminRow
	for rows.Next() {
		var row CategoryAdminRow
		if err := rows.Scan(&row.ID, &row.Name, &row.IconKey, &row.SortOrder, &row.Count); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// CreateExpertCategory inserts a new category. A name colliding with a live
// category (uq_expert_categories_name_live) maps to ErrCategoryNameTaken.
func (r *Repo) CreateExpertCategory(ctx context.Context, id, name, iconKey string, sortOrder int, now time.Time) error {
	const q = `INSERT INTO expert_categories (id, name, icon_key, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`
	if _, err := r.db.ExecContext(ctx, q, id, strings.TrimSpace(name), iconKey, sortOrder, now, now); err != nil {
		return mapDuplicateCategoryName(err)
	}
	return nil
}

// UpdateExpertCategory replaces a category's mutable columns. ErrNotFound when
// the id is gone/deleted; ErrCategoryNameTaken on a rename collision.
func (r *Repo) UpdateExpertCategory(ctx context.Context, id, name, iconKey string, sortOrder int, now time.Time) error {
	const q = `UPDATE expert_categories SET name = ?, icon_key = ?, sort_order = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, strings.TrimSpace(name), iconKey, sortOrder, now, id)
	if err != nil {
		return mapDuplicateCategoryName(err)
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

// DeleteExpertCategory soft-deletes a category, refusing when live experts or
// squads still reference it (ErrCategoryInUse, with the reference count so the
// caller can surface it). ErrNotFound when already gone.
func (r *Repo) DeleteExpertCategory(ctx context.Context, id string, now time.Time) (int, error) {
	var count int
	const countQ = `SELECT
		(SELECT COUNT(*) FROM experts e WHERE e.category_id = ? AND e.deleted_at IS NULL)
		+ (SELECT COUNT(*) FROM expert_squads s WHERE s.category_id = ? AND s.deleted_at IS NULL)`
	if err := r.db.QueryRowContext(ctx, countQ, id, id).Scan(&count); err != nil {
		return 0, err
	}
	if count > 0 {
		return count, ErrCategoryInUse
	}
	const q = `UPDATE expert_categories SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, now, now, id)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrNotFound
	}
	return 0, nil
}

// mapDuplicateCategoryName converts a duplicate-key violation on the category
// name_live unique index into ErrCategoryNameTaken.
func mapDuplicateCategoryName(err error) error {
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) && myErr.Number == mysqlErrDupEntry &&
		strings.Contains(myErr.Message, "uq_expert_categories_name_live") {
		return ErrCategoryNameTaken
	}
	return err
}
