package expert

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

const expertColumns = `id, short_name, name, summary, category_id, tags, publisher,
	owner_uid, creator_name, created_by_type, created_by_bot_uid, created_by_bot_name,
	space_id, visibility, instruction, mcp_config, skills_json, created_at, updated_at, deleted_at`

const squadColumns = `id, short_name, name, summary, category_id, tags, publisher,
	owner_uid, creator_name, created_by_type, created_by_bot_uid, created_by_bot_name,
	space_id, visibility, leader, strategies_json, dependencies_json, permission,
	members_json, created_at, updated_at, deleted_at`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// GetExpertByID loads a single live expert by id, ignoring visibility (the
// service applies the visibility rule). Returns ErrNotFound when absent. Tag
// ids are resolved back to names.
func (r *Repo) GetExpertByID(ctx context.Context, id string) (*model.Expert, error) {
	const q = `SELECT ` + expertColumns + ` FROM experts WHERE id = ? AND deleted_at IS NULL`
	m, tagIDs, err := scanExpert(r.db.QueryRowContext(ctx, q, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := r.hydrateTagNames(ctx, tagIDs, &m.Tags); err != nil {
		return nil, err
	}
	counts, err := r.loadMetrics(ctx, EntityExpert, []string{m.ID})
	if err != nil {
		return nil, err
	}
	m.ViewCount, m.InstallCount = counts[m.ID].View, counts[m.ID].Install
	return m, nil
}

// GetSquadByID loads a single live squad by id, ignoring visibility.
func (r *Repo) GetSquadByID(ctx context.Context, id string) (*model.Squad, error) {
	const q = `SELECT ` + squadColumns + ` FROM expert_squads WHERE id = ? AND deleted_at IS NULL`
	m, tagIDs, err := scanSquad(r.db.QueryRowContext(ctx, q, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := r.hydrateTagNames(ctx, tagIDs, &m.Tags); err != nil {
		return nil, err
	}
	counts, err := r.loadMetrics(ctx, EntitySquad, []string{m.ID})
	if err != nil {
		return nil, err
	}
	m.ViewCount, m.InstallCount = counts[m.ID].View, counts[m.ID].Install
	return m, nil
}

// hydrateTagNames resolves a row's tag ids into ordered names, dropping ids
// with no dictionary entry.
func (r *Repo) hydrateTagNames(ctx context.Context, ids []int64, dst *[]string) error {
	if len(ids) == 0 {
		*dst = []string{}
		return nil
	}
	names, err := r.ResolveTagNames(ctx, ids)
	if err != nil {
		return err
	}
	*dst = orderedNames(ids, names)
	return nil
}

// orderedNames maps ids to names preserving id order and dropping unknowns.
func orderedNames(ids []int64, names map[int64]string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		name := names[id]
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func scanExpert(s rowScanner) (*model.Expert, []int64, error) {
	var (
		m                model.Expert
		tags             []byte
		skills           []byte
		visibility       string
		createdByType    string
		createdByBotUID  sql.NullString
		createdByBotName sql.NullString
		spaceID          sql.NullString
		deletedAt        sql.NullTime
	)
	if err := s.Scan(
		&m.ID, &m.ShortName, &m.Name, &m.Summary, &m.Category, &tags, &m.Publisher,
		&m.OwnerUID, &m.CreatorName, &createdByType, &createdByBotUID, &createdByBotName,
		&spaceID, &visibility, &m.Instruction, &m.MCPConfig, &skills,
		&m.CreatedAt, &m.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, nil, err
	}
	m.Visibility = model.Visibility(visibility)
	m.CreatedByType = model.CreatedByType(createdByType)
	if createdByBotUID.Valid {
		m.CreatedByBotUID = createdByBotUID.String
	}
	if createdByBotName.Valid {
		m.CreatedByBotName = createdByBotName.String
	}
	if spaceID.Valid {
		m.SpaceID = spaceID.String
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		m.DeletedAt = &t
	}
	tagIDs := parseTagIDs(tags)
	if err := unmarshalInto(skills, &m.Skills); err != nil {
		return nil, nil, fmt.Errorf("unmarshal skills: %w", err)
	}
	return &m, tagIDs, nil
}

func scanSquad(s rowScanner) (*model.Squad, []int64, error) {
	var (
		m                model.Squad
		tags             []byte
		strategies       []byte
		dependencies     []byte
		members          []byte
		visibility       string
		createdByType    string
		createdByBotUID  sql.NullString
		createdByBotName sql.NullString
		spaceID          sql.NullString
		deletedAt        sql.NullTime
	)
	if err := s.Scan(
		&m.ID, &m.ShortName, &m.Name, &m.Summary, &m.Category, &tags, &m.Publisher,
		&m.OwnerUID, &m.CreatorName, &createdByType, &createdByBotUID, &createdByBotName,
		&spaceID, &visibility, &m.Leader, &strategies, &dependencies, &m.Permission,
		&members, &m.CreatedAt, &m.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, nil, err
	}
	m.Visibility = model.Visibility(visibility)
	m.CreatedByType = model.CreatedByType(createdByType)
	if createdByBotUID.Valid {
		m.CreatedByBotUID = createdByBotUID.String
	}
	if createdByBotName.Valid {
		m.CreatedByBotName = createdByBotName.String
	}
	if spaceID.Valid {
		m.SpaceID = spaceID.String
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		m.DeletedAt = &t
	}
	tagIDs := parseTagIDs(tags)
	if err := unmarshalInto(strategies, &m.Strategies); err != nil {
		return nil, nil, fmt.Errorf("unmarshal strategies: %w", err)
	}
	if err := unmarshalInto(dependencies, &m.Dependencies); err != nil {
		return nil, nil, fmt.Errorf("unmarshal dependencies: %w", err)
	}
	var stored []storedMember
	if err := unmarshalInto(members, &stored); err != nil {
		return nil, nil, fmt.Errorf("unmarshal members: %w", err)
	}
	m.Members = make([]model.SquadMember, 0, len(stored))
	for i := range stored {
		m.Members = append(m.Members, stored[i].toModel())
	}
	return &m, tagIDs, nil
}

// parseTagIDs decodes the JSON id array stored in the tags column. Malformed
// content yields an empty slice rather than an error — the wire never exposes
// ids and a corrupt tag column should not fail the whole read.
func parseTagIDs(raw []byte) []int64 {
	if len(raw) == 0 {
		return nil
	}
	var ids []int64
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil
	}
	return ids
}
