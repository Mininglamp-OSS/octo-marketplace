package expert

import (
	"context"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// UpdateExpert applies a full replacement of the mutable columns for an
// existing live expert. Tag NAMES on m.Tags are re-resolved to ids in the same
// transaction. A rename colliding with another live row owned by the same
// caller returns ErrNameTaken. Returns ErrNotFound when the row is gone. The
// caller must have already loaded the record and verified ownership.
func (r *Repo) UpdateExpert(ctx context.Context, m *model.Expert) error {
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
		visibility = ?, instruction = ?, mcp_config = ?, skills_json = ?, updated_at = ?
		WHERE id = ? AND owner_uid = ? AND space_id = ? AND visibility <> 'system' AND deleted_at IS NULL`
	res, err := tx.ExecContext(ctx, q,
		m.ShortName, m.Name, m.Summary, m.Category, string(tagsRaw), m.Publisher,
		string(m.Visibility), m.Instruction, m.MCPConfig, string(skills), m.UpdatedAt,
		m.ID, m.OwnerUID, m.SpaceID,
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

// UpdateSquad applies a full replacement of the mutable squad columns
// (members_json is fully replaced, not merged — doc §4.10).
func (r *Repo) UpdateSquad(ctx context.Context, m *model.Squad) error {
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
		visibility = ?, leader = ?, strategies_json = ?, dependencies_json = ?, permission = ?,
		members_json = ?, updated_at = ?
		WHERE id = ? AND owner_uid = ? AND space_id = ? AND visibility <> 'system' AND deleted_at IS NULL`
	res, err := tx.ExecContext(ctx, q,
		m.ShortName, m.Name, m.Summary, m.Category, string(tagsRaw), m.Publisher,
		string(m.Visibility), m.Leader, string(strategies), string(dependencies), m.Permission,
		string(members), m.UpdatedAt,
		m.ID, m.OwnerUID, m.SpaceID,
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
