package expert

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/expert"
)

// ─── Squad operations ────────────────────────────────────────────────────────

// CreateSquad validates + normalizes a flat squad create body, stamps identity
// and provenance, applies member defaults, and persists a new public squad
// (doc §4.7).
func (s *Service) CreateSquad(ctx context.Context, caller Caller, req model.SquadCreateRequest) (*model.ExpertSquadDetail, error) {
	name := strings.TrimSpace(req.Name)
	summary := strings.TrimSpace(req.Summary)
	if err := validateGeneric(name, summary, req.Publisher); err != nil {
		return nil, err
	}
	if err := validateSquadFields(req.Leader, req.Permission); err != nil {
		return nil, err
	}
	if err := validatePublicCreateVisibility(req.Visibility); err != nil {
		return nil, err
	}
	categoryID, categoryName, err := s.resolveCategory(ctx, req.Category)
	if err != nil {
		return nil, err
	}

	id := s.idGen()
	members, leaderIdx, err := s.buildMembers(ctx, id, req.Members, nil)
	if err != nil {
		return nil, err
	}
	leader := resolveLeaderName(req.Leader, members, leaderIdx)

	now := s.now()
	m := &model.Squad{
		ID:               id,
		ShortName:        deriveShortName(name),
		Name:             name,
		Summary:          summary,
		Category:         categoryID,
		Tags:             normalizeTagNames(req.Tags),
		Publisher:        strings.TrimSpace(req.Publisher),
		OwnerUID:         caller.UID,
		SpaceID:          caller.SpaceID,
		CreatorName:      caller.Name,
		CreatedByType:    resolveCreatedByType(caller),
		CreatedByBotUID:  caller.BotUID,
		CreatedByBotName: caller.BotName,
		Visibility:       model.VisibilityPublic,
		Leader:           leader,
		Strategies:       req.Strategies,
		Dependencies:     req.Dependencies,
		Permission:       strings.TrimSpace(req.Permission),
		Members:          members,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.repo.CreateSquad(ctx, m); err != nil {
		return nil, mapRepoError(err)
	}
	// The wire echoes the category NAME, not the stored id (doc §5).
	m.Category = categoryName
	detail := m.ToSquadDetail()
	return &detail, nil
}

// GetSquad returns a squad's detail if visible, else ErrNotFound (doc §4.4).
func (s *Service) GetSquad(ctx context.Context, caller Caller, id string) (*model.ExpertSquadDetail, error) {
	m, err := s.loadVisibleSquad(ctx, caller, id)
	if err != nil {
		return nil, err
	}
	if err := s.resolveSquadCategoryNames(ctx, []*model.Squad{m}); err != nil {
		return nil, err
	}
	detail := m.ToSquadDetail()
	return &detail, nil
}

// ListSquads returns the visible-to-caller set in the current Space (doc §4.2).
func (s *Service) ListSquads(ctx context.Context, caller Caller, p ListParams) (*SquadListResult, error) {
	return s.listSquads(ctx, caller, p, false)
}

// ListSquadsMine returns squads owned by the caller in the current Space.
func (s *Service) ListSquadsMine(ctx context.Context, caller Caller, p ListParams) (*SquadListResult, error) {
	return s.listSquads(ctx, caller, p, true)
}

func (s *Service) listSquads(ctx context.Context, caller Caller, p ListParams, mineOnly bool) (*SquadListResult, error) {
	filter, err := s.buildListFilter(ctx, caller, p, mineOnly)
	if err != nil {
		return nil, err
	}
	records, total, err := s.repo.ListSquads(ctx, filter)
	if err != nil {
		return nil, mapRepoError(err)
	}
	ptrs := make([]*model.Squad, len(records))
	for i := range records {
		ptrs[i] = &records[i]
	}
	if err := s.resolveSquadCategoryNames(ctx, ptrs); err != nil {
		return nil, err
	}
	items := make([]model.ExpertSquadListItem, 0, len(records))
	for i := range records {
		items = append(items, records[i].ToSquadListItem())
	}
	return &SquadListResult{Items: items, Total: total}, nil
}

// PatchSquad applies a partial update; owner only. Sending members replaces the
// whole array (full-replace, not merge — doc §4.10).
func (s *Service) PatchSquad(ctx context.Context, caller Caller, id string, req model.SquadPatchRequest) (*model.ExpertSquadDetail, error) {
	m, err := s.loadVisibleSquad(ctx, caller, id)
	if err != nil {
		return nil, err
	}
	if m.OwnerUID != caller.UID {
		return nil, ErrForbidden
	}
	if err := s.applySquadPatch(ctx, m, req); err != nil {
		return nil, err
	}
	m.UpdatedAt = s.now()
	if err := s.repo.UpdateSquad(ctx, m); err != nil {
		return nil, mapRepoError(err)
	}
	if err := s.resolveSquadCategoryNames(ctx, []*model.Squad{m}); err != nil {
		return nil, err
	}
	detail := m.ToSquadDetail()
	return &detail, nil
}

// DeleteSquad soft-deletes an owned squad (doc §4.12).
func (s *Service) DeleteSquad(ctx context.Context, caller Caller, id string) error {
	m, err := s.loadVisibleSquad(ctx, caller, id)
	if err != nil {
		return err
	}
	if m.OwnerUID != caller.UID {
		return ErrForbidden
	}
	if err := s.repo.DeleteSquad(ctx, id, s.now()); err != nil {
		return mapRepoError(err)
	}
	return nil
}

func (s *Service) applySquadPatch(ctx context.Context, m *model.Squad, req model.SquadPatchRequest) error {
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || utf8.RuneCountInString(name) > model.MaxExpertNameLen {
			return ErrInvalidRequest
		}
		m.Name = name
		m.ShortName = deriveShortName(name)
	}
	if req.Summary != nil {
		summary := strings.TrimSpace(*req.Summary)
		if summary == "" || utf8.RuneCountInString(summary) > model.MaxExpertSummaryLen {
			return ErrInvalidRequest
		}
		m.Summary = summary
	}
	if req.Publisher != nil {
		if utf8.RuneCountInString(*req.Publisher) > model.MaxExpertPublisherLen {
			return ErrInvalidRequest
		}
		m.Publisher = strings.TrimSpace(*req.Publisher)
	}
	if req.Category != nil {
		categoryID, _, err := s.resolveCategory(ctx, *req.Category)
		if err != nil {
			return err
		}
		m.Category = categoryID
	}
	if req.Tags != nil {
		m.Tags = normalizeTagNames(*req.Tags)
	}
	if req.Strategies != nil {
		m.Strategies = *req.Strategies
	}
	if req.Dependencies != nil {
		m.Dependencies = *req.Dependencies
	}
	if req.Permission != nil {
		if utf8.RuneCountInString(*req.Permission) > model.MaxSquadPermissionLen {
			return ErrInvalidRequest
		}
		m.Permission = strings.TrimSpace(*req.Permission)
	}
	// members is a FULL replace when present (doc §4.10).
	if req.Members != nil {
		members, leaderIdx, err := s.buildMembers(ctx, m.ID, *req.Members, m.Members)
		if err != nil {
			return err
		}
		m.Members = members
		// Re-derive the leader display name from the (possibly new) member set
		// unless the same request pins it explicitly.
		if req.Leader == nil {
			m.Leader = resolveLeaderName("", members, leaderIdx)
		}
	}
	if req.Leader != nil {
		leader := strings.TrimSpace(*req.Leader)
		if utf8.RuneCountInString(leader) > model.MaxSquadLeaderLen {
			return ErrInvalidRequest
		}
		m.Leader = leader
	}
	return nil
}

func (s *Service) loadVisibleSquad(ctx context.Context, caller Caller, id string) (*model.Squad, error) {
	m, err := s.repo.GetSquadByID(ctx, id)
	if err != nil {
		return nil, mapRepoError(err)
	}
	if !isVisible(m.Visibility, m.SpaceID, m.OwnerUID, caller) {
		return nil, ErrNotFound
	}
	return m, nil
}

// validateSquadFields bounds the squad-specific top-level strings.
func validateSquadFields(leader, permission string) error {
	if utf8.RuneCountInString(leader) > model.MaxSquadLeaderLen {
		return ErrInvalidRequest
	}
	if utf8.RuneCountInString(permission) > model.MaxSquadPermissionLen {
		return ErrInvalidRequest
	}
	return nil
}

// buildMembers validates each member and fills defaults (doc §3.4):
//   - member_key → member_NN (1-based, zero-padded)
//   - template_id → expert-{squad_id}-NN
//   - at least one member required; each name/role required and its mcp_config
//     validated; if no member is flagged leader the first is marked leader.
//
// Returns the built members (order preserved) and the leader index.
func (s *Service) buildMembers(ctx context.Context, squadID string, in []model.SquadMemberInput, existing []model.SquadMember) ([]model.SquadMember, int, error) {
	if len(in) == 0 {
		return nil, 0, ErrInvalidMembers
	}
	// Index existing members by member_key so a name-only skill on a PATCH can
	// preserve that member's stored package instead of wiping it.
	existingByKey := make(map[string][]model.SkillRef, len(existing))
	for i := range existing {
		existingByKey[existing[i].MemberKey] = existing[i].Skills
	}
	members := make([]model.SquadMember, 0, len(in))
	leaderIdx := -1
	for i := range in {
		mw := in[i]
		name := strings.TrimSpace(mw.Name)
		role := strings.TrimSpace(mw.Role)
		if name == "" || role == "" {
			return nil, 0, ErrInvalidMembers
		}
		if strings.TrimSpace(mw.Instruction) == "" {
			return nil, 0, ErrInvalidMembers
		}
		if utf8.RuneCountInString(name) > model.MaxExpertNameLen ||
			utf8.RuneCountInString(role) > model.MaxExpertTextLen {
			return nil, 0, ErrInvalidMembers
		}
		// Per-member mcp_config is validated the same way as the top-level one;
		// a malformed member config fails the whole request (doc §4.7).
		if err := validateMCPConfig(mw.MCPConfig); err != nil {
			return nil, 0, ErrInvalidMembers
		}
		memberKey := strings.TrimSpace(mw.MemberKey)
		if memberKey == "" {
			memberKey = fmt.Sprintf("member_%02d", i+1)
		}
		templateID := strings.TrimSpace(mw.TemplateID)
		if templateID == "" {
			templateID = fmt.Sprintf("expert-%s-%02d", squadID, i+1)
		}
		// Per-member skills: package uploads / inline content are stored under a
		// member-scoped prefix; name-only entries preserve the prior member's
		// stored skill of the same name (existingByKey).
		skills, err := s.buildSkillRefs(ctx, mw.Skills,
			squadMemberSkillsPrefix(squadID, memberKey), existingByKey[memberKey], ErrInvalidMembers)
		if err != nil {
			return nil, 0, err
		}
		if mw.IsLeader && leaderIdx == -1 {
			leaderIdx = i
		}
		members = append(members, model.SquadMember{
			MemberKey:   memberKey,
			TemplateID:  templateID,
			Name:        name,
			Role:        role,
			IsLeader:    mw.IsLeader,
			Instruction: mw.Instruction,
			MCPConfig:   mw.MCPConfig,
			Skills:      skills,
		})
	}
	if leaderIdx == -1 {
		leaderIdx = 0
		members[0].IsLeader = true
	}
	return members, leaderIdx, nil
}

// resolveLeaderName returns the explicit leader when supplied, else the leader
// member's display name (doc §3.5).
func resolveLeaderName(explicit string, members []model.SquadMember, leaderIdx int) string {
	if l := strings.TrimSpace(explicit); l != "" {
		return l
	}
	if leaderIdx >= 0 && leaderIdx < len(members) {
		return members[leaderIdx].Name
	}
	return ""
}

// ─── Tag suggestions ─────────────────────────────────────────────────────────

// ListTags aggregates tag names from records visible to the caller in the
// current Space (doc §4.13). entity selects experts (kind=agent) vs squads
// (kind=squad).
func (s *Service) ListTags(ctx context.Context, caller Caller, entity expertrepo.Entity, query string, limit int, mineOnly bool) ([]model.TagFilter, error) {
	tags, err := s.repo.ListTags(ctx, expertrepo.TagListFilter{
		Entity:    entity,
		CallerUID: caller.UID,
		SpaceID:   caller.SpaceID,
		Query:     query,
		Limit:     limit,
		MineOnly:  mineOnly,
	})
	if err != nil {
		return nil, mapRepoError(err)
	}
	if tags == nil {
		tags = []model.TagFilter{}
	}
	return tags, nil
}
