package expert

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/expert"
)

// This file holds the administrator (SuperAdmin) business rules for the Expert
// Marketplace: creating and managing platform-provided (visibility=system)
// experts and squads, and the expert_categories taxonomy. It mirrors the MCP
// admin surface (internal/service/mcp.go §"Admin surface"): create stamps
// Visibility=system + SpaceID="" (space_id NULL) and rejects a client-sent
// visibility; name uniqueness across system rows is pre-checked for a friendly
// error and enforced atomically by the uq_*_system_name_live unique index
// (20260813-00) when concurrent writes race past the pre-check. Get/update/
// delete load by id and require the row to be system (else ErrNotFound),
// bypassing the owner/space scope the public path enforces.

// Admin-only sentinels for the category taxonomy surface.
var (
	ErrCategoryNameTaken = errors.New("expert category name taken")
	ErrCategoryInUse     = errors.New("expert category in use")
)

// ─── System experts ──────────────────────────────────────────────────────────

// CreateSystemExpert publishes a platform-provided expert. Unlike the public
// CreateExpert it stamps Visibility=system and SpaceID="" (space_id NULL) —
// a client-sent visibility is rejected rather than silently overridden — and
// guards name uniqueness across all system experts via a pre-check backed by
// the uq_expert_system_name_live unique index.
func (s *Service) CreateSystemExpert(ctx context.Context, caller Caller, req model.ExpertCreateRequest) (*model.ExpertAgentDetail, error) {
	if req.Visibility != "" {
		return nil, ErrVisibilityNotAllowed
	}
	name := strings.TrimSpace(req.Name)
	summary := strings.TrimSpace(req.Summary)
	if err := validateGeneric(name, summary, req.Publisher); err != nil {
		return nil, err
	}
	categoryID, categoryName, err := s.resolveCategory(ctx, req.Category)
	if err != nil {
		return nil, err
	}
	if err := validateMCPConfig(req.MCPConfig); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Instruction) == "" {
		return nil, ErrInvalidRequest
	}
	tags := normalizeTagNames(req.Tags)
	if err := validateTags(tags); err != nil {
		return nil, err
	}
	if exists, err := s.repo.SystemExpertNameExists(ctx, name, ""); err != nil {
		return nil, mapRepoError(err)
	} else if exists {
		return nil, ErrNameTaken
	}

	id := s.idGen()
	skills, err := s.buildSkillRefs(ctx, req.Skills, expertSkillsPrefix(id), nil, ErrInvalidRequest)
	if err != nil {
		return nil, err
	}

	now := s.now()
	m := &model.Expert{
		ID:            id,
		ShortName:     deriveShortName(name),
		Name:          name,
		Summary:       summary,
		Category:      categoryID,
		Tags:          tags,
		Publisher:     strings.TrimSpace(req.Publisher),
		OwnerUID:      caller.UID,
		SpaceID:       "", // system rows are cross-Space (space_id NULL)
		CreatorName:   caller.Name,
		CreatedByType: model.CreatedByHuman,
		Visibility:    model.VisibilitySystem,
		Instruction:   req.Instruction,
		MCPConfig:     req.MCPConfig,
		Skills:        skills,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.repo.CreateExpert(ctx, m); err != nil {
		return nil, mapRepoError(err)
	}
	m.Category = categoryName
	detail := m.ToAgentDetail()
	return &detail, nil
}

// GetSystemExpert returns a system expert by id, or ErrNotFound if the id is
// gone or names a non-system record (prevents cross-enumeration of Space rows).
func (s *Service) GetSystemExpert(ctx context.Context, id string) (*model.ExpertAgentDetail, error) {
	m, err := s.loadSystemExpert(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.resolveExpertCategoryNames(ctx, []*model.Expert{m}); err != nil {
		return nil, err
	}
	detail := m.ToAgentDetail()
	return &detail, nil
}

// ListSystemExperts returns every system expert across Spaces (admin surface).
func (s *Service) ListSystemExperts(ctx context.Context, p ListParams) (*ExpertListResult, error) {
	filter, err := s.buildAdminListFilter(ctx, p)
	if err != nil {
		return nil, err
	}
	records, total, err := s.repo.ListExperts(ctx, filter)
	if err != nil {
		return nil, mapRepoError(err)
	}
	ptrs := make([]*model.Expert, len(records))
	for i := range records {
		ptrs[i] = &records[i]
	}
	if err := s.resolveExpertCategoryNames(ctx, ptrs); err != nil {
		return nil, err
	}
	items := make([]model.ExpertAgentListItem, 0, len(records))
	for i := range records {
		items = append(items, records[i].ToAgentListItem())
	}
	return &ExpertListResult{Items: items, Total: total}, nil
}

// UpdateSystemExpert applies a partial update to a system expert (metadata or a
// full spec replacement). No ownership check; the row must be system.
func (s *Service) UpdateSystemExpert(ctx context.Context, id string, req model.ExpertPatchRequest) (*model.ExpertAgentDetail, error) {
	m, err := s.loadSystemExpert(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.applyExpertPatch(ctx, m, req); err != nil {
		return nil, err
	}
	if req.Name != nil {
		if exists, err := s.repo.SystemExpertNameExists(ctx, m.Name, m.ID); err != nil {
			return nil, mapRepoError(err)
		} else if exists {
			return nil, ErrNameTaken
		}
	}
	m.UpdatedAt = s.now()
	if err := s.repo.UpdateSystemExpert(ctx, m); err != nil {
		return nil, mapRepoError(err)
	}
	if err := s.resolveExpertCategoryNames(ctx, []*model.Expert{m}); err != nil {
		return nil, err
	}
	detail := m.ToAgentDetail()
	return &detail, nil
}

// DeleteSystemExpert soft-deletes a system expert by id.
func (s *Service) DeleteSystemExpert(ctx context.Context, id string) error {
	if _, err := s.loadSystemExpert(ctx, id); err != nil {
		return err
	}
	if err := s.repo.DeleteSystemExpert(ctx, id, s.now()); err != nil {
		return mapRepoError(err)
	}
	return nil
}

func (s *Service) loadSystemExpert(ctx context.Context, id string) (*model.Expert, error) {
	m, err := s.repo.GetExpertByID(ctx, id)
	if err != nil {
		return nil, mapRepoError(err)
	}
	if m.Visibility != model.VisibilitySystem {
		return nil, ErrNotFound
	}
	return m, nil
}

// ─── System squads ───────────────────────────────────────────────────────────

// CreateSystemSquad publishes a platform-provided squad (visibility=system).
// A client-sent visibility is rejected, mirroring CreateSystemExpert.
func (s *Service) CreateSystemSquad(ctx context.Context, caller Caller, req model.SquadCreateRequest) (*model.ExpertSquadDetail, error) {
	if req.Visibility != "" {
		return nil, ErrVisibilityNotAllowed
	}
	name := strings.TrimSpace(req.Name)
	summary := strings.TrimSpace(req.Summary)
	if err := validateGeneric(name, summary, req.Publisher); err != nil {
		return nil, err
	}
	if err := validateSquadFields(req.Leader, req.Permission); err != nil {
		return nil, err
	}
	categoryID, categoryName, err := s.resolveCategory(ctx, req.Category)
	if err != nil {
		return nil, err
	}
	tags := normalizeTagNames(req.Tags)
	if err := validateTags(tags); err != nil {
		return nil, err
	}
	if err := validateSquadLists(req.Strategies, req.Dependencies); err != nil {
		return nil, err
	}
	if exists, err := s.repo.SystemSquadNameExists(ctx, name, ""); err != nil {
		return nil, mapRepoError(err)
	} else if exists {
		return nil, ErrNameTaken
	}

	id := s.idGen()
	members, leaderIdx, err := s.buildMembers(ctx, id, req.Members, nil)
	if err != nil {
		return nil, err
	}
	leader := resolveLeaderName(req.Leader, members, leaderIdx)

	now := s.now()
	m := &model.Squad{
		ID:            id,
		ShortName:     deriveShortName(name),
		Name:          name,
		Summary:       summary,
		Category:      categoryID,
		Tags:          tags,
		Publisher:     strings.TrimSpace(req.Publisher),
		OwnerUID:      caller.UID,
		SpaceID:       "",
		CreatorName:   caller.Name,
		CreatedByType: model.CreatedByHuman,
		Visibility:    model.VisibilitySystem,
		Leader:        leader,
		Strategies:    req.Strategies,
		Dependencies:  req.Dependencies,
		Permission:    strings.TrimSpace(req.Permission),
		Members:       members,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.repo.CreateSquad(ctx, m); err != nil {
		return nil, mapRepoError(err)
	}
	m.Category = categoryName
	detail := m.ToSquadDetail()
	return &detail, nil
}

// GetSystemSquad returns a system squad by id, or ErrNotFound otherwise.
func (s *Service) GetSystemSquad(ctx context.Context, id string) (*model.ExpertSquadDetail, error) {
	m, err := s.loadSystemSquad(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.resolveSquadCategoryNames(ctx, []*model.Squad{m}); err != nil {
		return nil, err
	}
	detail := m.ToSquadDetail()
	return &detail, nil
}

// ListSystemSquads returns every system squad across Spaces (admin surface).
func (s *Service) ListSystemSquads(ctx context.Context, p ListParams) (*SquadListResult, error) {
	filter, err := s.buildAdminListFilter(ctx, p)
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

// UpdateSystemSquad applies a partial update to a system squad.
func (s *Service) UpdateSystemSquad(ctx context.Context, id string, req model.SquadPatchRequest) (*model.ExpertSquadDetail, error) {
	m, err := s.loadSystemSquad(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.applySquadPatch(ctx, m, req); err != nil {
		return nil, err
	}
	if req.Name != nil {
		if exists, err := s.repo.SystemSquadNameExists(ctx, m.Name, m.ID); err != nil {
			return nil, mapRepoError(err)
		} else if exists {
			return nil, ErrNameTaken
		}
	}
	m.UpdatedAt = s.now()
	if err := s.repo.UpdateSystemSquad(ctx, m); err != nil {
		return nil, mapRepoError(err)
	}
	if err := s.resolveSquadCategoryNames(ctx, []*model.Squad{m}); err != nil {
		return nil, err
	}
	detail := m.ToSquadDetail()
	return &detail, nil
}

// DeleteSystemSquad soft-deletes a system squad by id.
func (s *Service) DeleteSystemSquad(ctx context.Context, id string) error {
	if _, err := s.loadSystemSquad(ctx, id); err != nil {
		return err
	}
	if err := s.repo.DeleteSystemSquad(ctx, id, s.now()); err != nil {
		return mapRepoError(err)
	}
	return nil
}

func (s *Service) loadSystemSquad(ctx context.Context, id string) (*model.Squad, error) {
	m, err := s.repo.GetSquadByID(ctx, id)
	if err != nil {
		return nil, mapRepoError(err)
	}
	if m.Visibility != model.VisibilitySystem {
		return nil, ErrNotFound
	}
	return m, nil
}

// buildAdminListFilter builds a SystemOnly list filter (all Spaces, no caller
// scope). Tag names resolve against the global (system) tag bucket.
func (s *Service) buildAdminListFilter(ctx context.Context, p ListParams) (expertrepo.ListFilter, error) {
	if len(p.Tags) > model.MaxExpertTags {
		return expertrepo.ListFilter{}, ErrInvalidRequest
	}
	tagGroups, err := s.repo.ResolveFilterTagIDs(ctx, expertrepo.GlobalTagSpaceID, p.Tags)
	if err != nil {
		return expertrepo.ListFilter{}, mapRepoError(err)
	}
	if hasNonEmpty(p.Tags) && len(tagGroups) < countNonEmpty(p.Tags) {
		tagGroups = append(tagGroups, []int64{-1})
	}
	return expertrepo.ListFilter{
		Keyword:     strings.TrimSpace(p.Keyword),
		Categories:  p.Categories,
		TagIDGroups: tagGroups,
		Sort:        p.Sort,
		Limit:       clampLimit(p.Limit),
		Offset:      clampOffset(p.Offset),
		SystemOnly:  true,
	}, nil
}

// ─── Category taxonomy (admin) ───────────────────────────────────────────────

// AdminCategory is the admin category projection: the stored fields plus the
// combined count of live experts + squads referencing the category.
type AdminCategory struct {
	ExpertCategoryID string `json:"expert_category_id"`
	Name             string `json:"name"`
	IconKey          string `json:"icon_key"`
	SortOrder        int    `json:"sort_order"`
	Count            int    `json:"count"`
}

// ListAdminCategories returns every live category for administrator management.
func (s *Service) ListAdminCategories(ctx context.Context) ([]AdminCategory, error) {
	rows, err := s.repo.ListExpertCategoriesAdmin(ctx)
	if err != nil {
		return nil, mapRepoError(err)
	}
	out := make([]AdminCategory, 0, len(rows))
	for _, r := range rows {
		out = append(out, AdminCategory{
			ExpertCategoryID: r.ID,
			Name:             r.Name,
			IconKey:          r.IconKey,
			SortOrder:        r.SortOrder,
			Count:            r.Count,
		})
	}
	return out, nil
}

// CreateCategory adds a new expert category (mints its id).
func (s *Service) CreateCategory(ctx context.Context, name, iconKey string, sortOrder int) (*AdminCategory, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > model.MaxExpertTagNameLen {
		return nil, ErrInvalidRequest
	}
	id := s.idGen()
	if err := s.repo.CreateExpertCategory(ctx, id, trimmed, strings.TrimSpace(iconKey), sortOrder, s.now()); err != nil {
		return nil, mapCategoryError(err)
	}
	return &AdminCategory{ExpertCategoryID: id, Name: trimmed, IconKey: strings.TrimSpace(iconKey), SortOrder: sortOrder}, nil
}

// UpdateCategory replaces a category's mutable fields.
func (s *Service) UpdateCategory(ctx context.Context, id, name, iconKey string, sortOrder int) (*AdminCategory, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > model.MaxExpertTagNameLen {
		return nil, ErrInvalidRequest
	}
	if err := s.repo.UpdateExpertCategory(ctx, id, trimmed, strings.TrimSpace(iconKey), sortOrder, s.now()); err != nil {
		return nil, mapCategoryError(err)
	}
	return &AdminCategory{ExpertCategoryID: id, Name: trimmed, IconKey: strings.TrimSpace(iconKey), SortOrder: sortOrder}, nil
}

// DeleteCategory soft-deletes a category, returning the reference count when it
// is still in use (ErrCategoryInUse).
func (s *Service) DeleteCategory(ctx context.Context, id string) (int, error) {
	count, err := s.repo.DeleteExpertCategory(ctx, id, s.now())
	if err != nil {
		return count, mapCategoryError(err)
	}
	return 0, nil
}

// mapCategoryError translates repo category sentinels to service sentinels.
func mapCategoryError(err error) error {
	switch {
	case errors.Is(err, expertrepo.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, expertrepo.ErrCategoryNameTaken):
		return ErrCategoryNameTaken
	case errors.Is(err, expertrepo.ErrCategoryInUse):
		return ErrCategoryInUse
	default:
		return err
	}
}

// ─── System skill content (admin) ────────────────────────────────────────────

// GetSystemExpertSkillMD returns the stored SKILL.md text for a system
// expert's skill at index i — the admin twin of GetExpertSkillMD, gated on
// visibility=system instead of the caller-visible rule.
func (s *Service) GetSystemExpertSkillMD(ctx context.Context, id string, index int) (string, error) {
	m, err := s.loadSystemExpert(ctx, id)
	if err != nil {
		return "", err
	}
	return s.readSkillContent(ctx, m.Skills, index)
}

// GetSystemSquadSkillMD is the squad twin: it locates the member by
// member_key, then reads that member's skill at index i.
func (s *Service) GetSystemSquadSkillMD(ctx context.Context, id, memberKey string, index int) (string, error) {
	m, err := s.loadSystemSquad(ctx, id)
	if err != nil {
		return "", err
	}
	for i := range m.Members {
		if m.Members[i].MemberKey == memberKey {
			return s.readSkillContent(ctx, m.Members[i].Skills, index)
		}
	}
	return "", ErrNotFound
}
