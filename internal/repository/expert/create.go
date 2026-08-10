package expert

import (
	"context"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// CreateExpert inserts a new expert. Tag NAMES on m.Tags are resolved to (or
// created as) ids in expert_tags within the same transaction, then stored as a
// JSON id array; m.Tags is left holding the caller-supplied names. A colliding
// (owner_uid, space_id, name) live tuple fails with duplicate-key, mapped to
// ErrNameTaken.
func (r *Repo) CreateExpert(ctx context.Context, m *model.Expert) error {
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

	const q = `INSERT INTO experts
		(id, short_name, name, summary, category_id, tags, publisher,
		 owner_uid, creator_name, created_by_type, created_by_bot_uid, created_by_bot_name,
		 space_id, visibility, instruction, mcp_config, skills_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := tx.ExecContext(ctx, q,
		m.ID, m.ShortName, m.Name, m.Summary, m.Category, string(tagsRaw), m.Publisher,
		m.OwnerUID, m.CreatorName, string(m.CreatedByType),
		nullableString(m.CreatedByBotUID), nullableString(m.CreatedByBotName),
		nullableString(m.SpaceID), string(m.Visibility),
		m.Instruction, m.MCPConfig, string(skills), m.CreatedAt, m.UpdatedAt,
	); err != nil {
		return mapDuplicateName(err)
	}
	return tx.Commit()
}

// CreateSquad inserts a new squad, resolving tag names to ids in the same
// transaction. members_json snapshots each member's full ExpertSpec + role
// metadata, order preserved.
func (r *Repo) CreateSquad(ctx context.Context, m *model.Squad) error {
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

	const q = `INSERT INTO expert_squads
		(id, short_name, name, summary, category_id, tags, publisher,
		 owner_uid, creator_name, created_by_type, created_by_bot_uid, created_by_bot_name,
		 space_id, visibility, leader, strategies_json, dependencies_json, permission,
		 members_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := tx.ExecContext(ctx, q,
		m.ID, m.ShortName, m.Name, m.Summary, m.Category, string(tagsRaw), m.Publisher,
		m.OwnerUID, m.CreatorName, string(m.CreatedByType),
		nullableString(m.CreatedByBotUID), nullableString(m.CreatedByBotName),
		nullableString(m.SpaceID), string(m.Visibility),
		m.Leader, string(strategies), string(dependencies), m.Permission,
		string(members), m.CreatedAt, m.UpdatedAt,
	); err != nil {
		return mapDuplicateName(err)
	}
	return tx.Commit()
}

// marshalSquadPayload marshals the three squad JSON columns, guaranteeing
// arrays (never null) so reads stay stable.
func marshalSquadPayload(m *model.Squad) (strategies, dependencies, members []byte, err error) {
	if strategies, err = marshalJSON(nonNilStrings(m.Strategies)); err != nil {
		return
	}
	deps := model.SquadDependencies{
		Blocking:    nonNilStrings(m.Dependencies.Blocking),
		Recommended: nonNilStrings(m.Dependencies.Recommended),
	}
	if dependencies, err = marshalJSON(deps); err != nil {
		return
	}
	members, err = marshalJSON(nonNilMembers(m.Members))
	return
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilSkills(s []model.SkillRef) []model.SkillRef {
	if s == nil {
		return []model.SkillRef{}
	}
	return s
}

func nonNilMembers(m []model.SquadMember) []storedMember {
	out := make([]storedMember, 0, len(m))
	for i := range m {
		out = append(out, storedMember{
			MemberKey:   m[i].MemberKey,
			TemplateID:  m[i].TemplateID,
			Name:        m[i].Name,
			Role:        m[i].Role,
			IsLeader:    m[i].IsLeader,
			Instruction: m[i].Instruction,
			MCPConfig:   m[i].MCPConfig,
			Skills:      nonNilSkills(m[i].Skills),
		})
	}
	return out
}

// storedMember is the JSON shape persisted in members_json. It mirrors
// model.SquadMember but carries explicit json tags so the column round-trips
// independently of the wire DTO.
type storedMember struct {
	MemberKey   string           `json:"member_key"`
	TemplateID  string           `json:"template_id"`
	Name        string           `json:"name"`
	Role        string           `json:"role"`
	IsLeader    bool             `json:"is_leader"`
	Instruction string           `json:"instruction"`
	MCPConfig   string           `json:"mcp_config"`
	Skills      []model.SkillRef `json:"skills"`
}

func (s storedMember) toModel() model.SquadMember {
	return model.SquadMember{
		MemberKey:   s.MemberKey,
		TemplateID:  s.TemplateID,
		Name:        s.Name,
		Role:        s.Role,
		IsLeader:    s.IsLeader,
		Instruction: s.Instruction,
		MCPConfig:   s.MCPConfig,
		Skills:      s.Skills,
	}
}
