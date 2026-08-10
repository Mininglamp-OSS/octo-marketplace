// Package expert holds the Expert Marketplace business rules for both entity
// families — experts (专家 / single agents) and squads (专家团 / teams) — plus
// the shared tag-suggestion aggregation. It validates requests, stamps
// server-owned identity/provenance, enforces the visibility judgement
// (doc §4.2/§4.4), and maps between the flat wire bodies and the domain model.
// Handlers call this layer; this layer never touches HTTP. It depends on the
// expert repository for persistence, including the dedicated expert_categories
// taxonomy it uses to resolve the wire `category` NAME to a stored id on write
// and back to a NAME on read (doc §5).
package expert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/expert"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

const (
	defaultLimit = 20
	maxLimit     = 100
	// maxSkillReadBytes caps a skill_md read (doc §3.1, 2 MiB), mirroring the
	// skill package's maxSkillMDReadBytes.
	maxSkillReadBytes = int64(2 << 20)
	// maxSkillPackageBytes caps an uploaded .zip/.skill package (20 MiB, matches
	// the skills marketplace + the dmworkmcp client cap).
	maxSkillPackageBytes = int64(20 << 20)
	// expertUploadPrefix is the key prefix for freshly-uploaded (not-yet-committed)
	// skill packages. The commit path only accepts upload keys under this prefix.
	expertUploadPrefix = "expert-uploads/"
	// presignTTL bounds the presigned PUT/GET URLs the upload+download endpoints mint.
	presignTTL = time.Hour
)

// Sentinel errors returned to the handler, which maps each to a wire code:
// NotFound→404, Forbidden→403, NameTaken→409, the rest→400 VALIDATION_ERROR.
var (
	ErrNotFound          = errors.New("expert not found")
	ErrForbidden         = errors.New("forbidden")
	ErrNameTaken         = errors.New("expert name taken")
	ErrCategoryNotFound  = errors.New("category not found")
	ErrInvalidVisibility = errors.New("invalid visibility")
	ErrInvalidMCPConfig  = errors.New("invalid mcp_config")
	ErrInvalidMembers    = errors.New("invalid members")
	ErrInvalidRequest    = errors.New("invalid request")
)

// Caller is the resolved identity + Space for a request, stamped server-side
// from the Octo token and X-Space-Id (doc §1). The service never trusts body
// identity fields. BotUID / BotName are non-empty only when the request rode in
// on a Bot token; UID / Name always describe the owner user.
type Caller struct {
	UID     string
	Name    string
	SpaceID string
	BotUID  string
	BotName string
}

// Store is the persistence surface the service needs. *expertrepo.Repo
// satisfies it; tests provide an in-memory fake so the catalog rules stay
// unit-testable without a database.
type Store interface {
	CreateExpert(ctx context.Context, m *model.Expert) error
	GetExpertByID(ctx context.Context, id string) (*model.Expert, error)
	ListExperts(ctx context.Context, f expertrepo.ListFilter) ([]model.Expert, int, error)
	UpdateExpert(ctx context.Context, m *model.Expert) error
	DeleteExpert(ctx context.Context, id, ownerUID, spaceID string, now time.Time) error

	CreateSquad(ctx context.Context, m *model.Squad) error
	GetSquadByID(ctx context.Context, id string) (*model.Squad, error)
	ListSquads(ctx context.Context, f expertrepo.ListFilter) ([]model.Squad, int, error)
	UpdateSquad(ctx context.Context, m *model.Squad) error
	DeleteSquad(ctx context.Context, id, ownerUID, spaceID string, now time.Time) error

	ListTags(ctx context.Context, f expertrepo.TagListFilter) ([]model.TagFilter, error)
	ResolveFilterTagIDs(ctx context.Context, spaceID string, tags []string) ([][]int64, error)

	// CategoryIDByName resolves an incoming category NAME to its stored id,
	// returning "" when no live category carries that name (doc §5).
	CategoryIDByName(ctx context.Context, name string) (string, error)
	// CategoryNamesByIDs resolves stored category ids back to the NAMEs the wire
	// exposes on read, dropping ids with no live category.
	CategoryNamesByIDs(ctx context.Context, ids []string) (map[string]string, error)
	// ListCategoriesWithCount returns every live category with a visible-record
	// count of the given kind for GET /expert_categories.
	ListCategoriesWithCount(ctx context.Context, kind expertrepo.Entity, spaceID, ownerUID string) ([]expertrepo.CategoryCount, error)
}

// Service implements the expert + squad catalog operations.
type Service struct {
	repo  Store
	store storage.Storage
	idGen func() string
	now   func() time.Time
}

// New returns a Service backed by the expert repository (which also owns the
// expert_categories taxonomy) and an object store for viewable skill content.
// idGen mints opaque record ids (the router passes the shared generator). store
// may be nil in tests that never exercise the skill-content path; the write
// path then treats every skill as name-only. The clock is time.Now by default
// and overridable in tests.
func New(repo Store, store storage.Storage, idGen func() string) *Service {
	return &Service{repo: repo, store: store, idGen: idGen, now: time.Now}
}

// ListParams carries the query parameters shared by the four list endpoints.
type ListParams struct {
	Keyword        string
	Categories     []string
	Tags           []string
	Visibilities   []string
	CreatedByTypes []string
	Sort           string
	Limit          int
	Offset         int
}

// ExpertListResult is the service-level list projection for experts.
type ExpertListResult struct {
	Items []model.ExpertAgentListItem
	Total int
}

// SquadListResult is the service-level list projection for squads.
type SquadListResult struct {
	Items []model.ExpertSquadListItem
	Total int
}

// ─── Expert operations ───────────────────────────────────────────────────────

// CreateExpert validates + normalizes a flat create body, stamps identity and
// provenance, and persists a new public expert (doc §4.1).
func (s *Service) CreateExpert(ctx context.Context, caller Caller, req model.ExpertCreateRequest) (*model.ExpertAgentDetail, error) {
	name := strings.TrimSpace(req.Name)
	summary := strings.TrimSpace(req.Summary)
	if err := validateGeneric(name, summary, req.Publisher); err != nil {
		return nil, err
	}
	if err := validatePublicCreateVisibility(req.Visibility); err != nil {
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

	id := s.idGen()
	skills, err := s.buildSkillRefs(ctx, req.Skills, expertSkillsPrefix(id), nil, ErrInvalidRequest)
	if err != nil {
		return nil, err
	}

	now := s.now()
	m := &model.Expert{
		ID:               id,
		ShortName:        deriveShortName(name),
		Name:             name,
		Summary:          summary,
		Category:         categoryID,
		Tags:             tags,
		Publisher:        strings.TrimSpace(req.Publisher),
		OwnerUID:         caller.UID,
		SpaceID:          caller.SpaceID,
		CreatorName:      caller.Name,
		CreatedByType:    resolveCreatedByType(caller),
		CreatedByBotUID:  caller.BotUID,
		CreatedByBotName: caller.BotName,
		Visibility:       model.VisibilityPublic,
		Instruction:      req.Instruction,
		MCPConfig:        req.MCPConfig,
		Skills:           skills,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.repo.CreateExpert(ctx, m); err != nil {
		return nil, mapRepoError(err)
	}
	// The wire echoes the category NAME, not the stored id (doc §5).
	m.Category = categoryName
	detail := m.ToAgentDetail()
	return &detail, nil
}

// GetExpert returns an expert's detail if visible to the caller, else
// ErrNotFound (doc §4.4 — never a leaky 403).
func (s *Service) GetExpert(ctx context.Context, caller Caller, id string) (*model.ExpertAgentDetail, error) {
	m, err := s.loadVisibleExpert(ctx, caller, id)
	if err != nil {
		return nil, err
	}
	if err := s.resolveExpertCategoryNames(ctx, []*model.Expert{m}); err != nil {
		return nil, err
	}
	detail := m.ToAgentDetail()
	return &detail, nil
}

// ListExperts returns the visible-to-caller set in the current Space (doc §4.2).
func (s *Service) ListExperts(ctx context.Context, caller Caller, p ListParams) (*ExpertListResult, error) {
	return s.listExperts(ctx, caller, p, false)
}

// ListExpertsMine returns experts owned by the caller in the current Space
// (doc §4.3), regardless of visibility.
func (s *Service) ListExpertsMine(ctx context.Context, caller Caller, p ListParams) (*ExpertListResult, error) {
	return s.listExperts(ctx, caller, p, true)
}

func (s *Service) listExperts(ctx context.Context, caller Caller, p ListParams, mineOnly bool) (*ExpertListResult, error) {
	filter, err := s.buildListFilter(ctx, caller, p, mineOnly)
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

// PatchExpert applies a partial update. Owner only; a non-owner (or a record in
// another Space) is indistinguishable from not-found for reads, but a
// visible-yet-not-owned record yields ErrForbidden (doc §4.5).
func (s *Service) PatchExpert(ctx context.Context, caller Caller, id string, req model.ExpertPatchRequest) (*model.ExpertAgentDetail, error) {
	m, err := s.loadVisibleExpert(ctx, caller, id)
	if err != nil {
		return nil, err
	}
	if forbidsPublicMutation(m.Visibility, m.OwnerUID, caller) {
		return nil, ErrForbidden
	}
	if err := s.applyExpertPatch(ctx, m, req); err != nil {
		return nil, err
	}
	m.UpdatedAt = s.now()
	if err := s.repo.UpdateExpert(ctx, m); err != nil {
		return nil, mapRepoError(err)
	}
	if err := s.resolveExpertCategoryNames(ctx, []*model.Expert{m}); err != nil {
		return nil, err
	}
	detail := m.ToAgentDetail()
	return &detail, nil
}

// DeleteExpert soft-deletes an owned expert (doc §4.6).
func (s *Service) DeleteExpert(ctx context.Context, caller Caller, id string) error {
	m, err := s.loadVisibleExpert(ctx, caller, id)
	if err != nil {
		return err
	}
	if forbidsPublicMutation(m.Visibility, m.OwnerUID, caller) {
		return ErrForbidden
	}
	if err := s.repo.DeleteExpert(ctx, id, caller.UID, caller.SpaceID, s.now()); err != nil {
		return mapRepoError(err)
	}
	return nil
}

func (s *Service) applyExpertPatch(ctx context.Context, m *model.Expert, req model.ExpertPatchRequest) error {
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return ErrInvalidRequest
		}
		if utf8.RuneCountInString(name) > model.MaxExpertNameLen {
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
		tags := normalizeTagNames(*req.Tags)
		if err := validateTags(tags); err != nil {
			return err
		}
		m.Tags = tags
	}
	if req.Instruction != nil {
		if strings.TrimSpace(*req.Instruction) == "" {
			return ErrInvalidRequest
		}
		m.Instruction = *req.Instruction
	}
	if req.MCPConfig != nil {
		if err := validateMCPConfig(*req.MCPConfig); err != nil {
			return err
		}
		m.MCPConfig = *req.MCPConfig
	}
	if req.Skills != nil {
		// Pass the current skills so name-only entries preserve their stored
		// package/SKILL.md instead of being wiped on a read-modify-write PATCH.
		skills, err := s.buildSkillRefs(ctx, *req.Skills, expertSkillsPrefix(m.ID), m.Skills, ErrInvalidRequest)
		if err != nil {
			return err
		}
		m.Skills = skills
	}
	// visibility is accepted for wire compatibility but never mutated on the
	// public patch path (doc §4.5).
	return nil
}

func (s *Service) loadVisibleExpert(ctx context.Context, caller Caller, id string) (*model.Expert, error) {
	m, err := s.repo.GetExpertByID(ctx, id)
	if err != nil {
		return nil, mapRepoError(err)
	}
	if !isVisible(m.Visibility, m.SpaceID, m.OwnerUID, caller) {
		return nil, ErrNotFound
	}
	return m, nil
}

// ─── Shared helpers ──────────────────────────────────────────────────────────

func (s *Service) buildListFilter(ctx context.Context, caller Caller, p ListParams, mineOnly bool) (expertrepo.ListFilter, error) {
	// Bound the tag filter: ResolveFilterTagIDs runs one query per name, so an
	// uncapped ?tag= list becomes an unbounded sequence of round-trips.
	if len(p.Tags) > model.MaxExpertTags {
		return expertrepo.ListFilter{}, ErrInvalidRequest
	}
	tagGroups, err := s.repo.ResolveFilterTagIDs(ctx, caller.SpaceID, p.Tags)
	if err != nil {
		return expertrepo.ListFilter{}, mapRepoError(err)
	}
	// A tag name that matched no dictionary entry can never be present on a
	// row, so the result set is empty. Signal this by an impossible group.
	if hasNonEmpty(p.Tags) && len(tagGroups) < countNonEmpty(p.Tags) {
		tagGroups = append(tagGroups, []int64{-1})
	}
	return expertrepo.ListFilter{
		CallerUID:      caller.UID,
		SpaceID:        caller.SpaceID,
		Keyword:        strings.TrimSpace(p.Keyword),
		Categories:     p.Categories,
		TagIDGroups:    tagGroups,
		Visibilities:   p.Visibilities,
		CreatedByTypes: p.CreatedByTypes,
		Sort:           p.Sort,
		Limit:          clampLimit(p.Limit),
		Offset:         clampOffset(p.Offset),
		MineOnly:       mineOnly,
	}, nil
}

// isVisible applies the read visibility rule (doc §4.4):
//
//	system  OR  (space_id == caller_space AND (public OR owner == caller))
func isVisible(v model.Visibility, spaceID, ownerUID string, caller Caller) bool {
	if v == model.VisibilitySystem {
		return true
	}
	if spaceID != caller.SpaceID {
		return false
	}
	return v == model.VisibilityPublic || ownerUID == caller.UID
}

// forbidsPublicMutation reports whether a public patch/delete must be rejected.
// system rows are platform/admin-managed and are never mutable through the
// public surface — regardless of owner_uid — while a public/private row is
// mutable only by its owner. `isVisible` deliberately exposes every system row
// for reads, so the mutating verbs need this extra system exclusion on top of
// the ownership check (otherwise a system row whose owner_uid matches the
// caller would be patch/delete-able here).
func forbidsPublicMutation(v model.Visibility, ownerUID string, caller Caller) bool {
	return v == model.VisibilitySystem || ownerUID != caller.UID
}

// resolveCreatedByType stamps provenance: bot iff the request rode in on a Bot
// token, else human. Client-supplied values are never trusted.
func resolveCreatedByType(caller Caller) model.CreatedByType {
	if caller.BotUID != "" {
		return model.CreatedByBot
	}
	return model.CreatedByHuman
}

// resolveCategory maps an incoming category NAME (doc §5: the wire carries
// names) to its stored id, returning the canonical name for the response. An
// empty or unknown name yields ErrCategoryNotFound (VALIDATION_ERROR), matching
// the prior bad-category behavior.
func (s *Service) resolveCategory(ctx context.Context, category string) (id, name string, err error) {
	name = strings.TrimSpace(category)
	if name == "" {
		return "", "", ErrCategoryNotFound
	}
	id, err = s.repo.CategoryIDByName(ctx, name)
	if err != nil {
		return "", "", err
	}
	if id == "" {
		return "", "", ErrCategoryNotFound
	}
	return id, name, nil
}

// resolveExpertCategoryNames rewrites each expert's Category from its stored id
// to the wire NAME in one batch lookup (doc §5). An id with no live category is
// left untouched.
func (s *Service) resolveExpertCategoryNames(ctx context.Context, experts []*model.Expert) error {
	ids := make([]string, 0, len(experts))
	for _, e := range experts {
		ids = append(ids, e.Category)
	}
	names, err := s.repo.CategoryNamesByIDs(ctx, ids)
	if err != nil {
		return err
	}
	for _, e := range experts {
		if n := names[e.Category]; n != "" {
			e.Category = n
		}
	}
	return nil
}

// resolveSquadCategoryNames is the squad twin of resolveExpertCategoryNames.
func (s *Service) resolveSquadCategoryNames(ctx context.Context, squads []*model.Squad) error {
	ids := make([]string, 0, len(squads))
	for _, sq := range squads {
		ids = append(ids, sq.Category)
	}
	names, err := s.repo.CategoryNamesByIDs(ctx, ids)
	if err != nil {
		return err
	}
	for _, sq := range squads {
		if n := names[sq.Category]; n != "" {
			sq.Category = n
		}
	}
	return nil
}

// ListCategories returns every live expert category with the number of records
// of the given kind visible to the caller in their Space (doc §5). kind selects
// experts (EntityExpert) or squads (EntitySquad); the handler defaults it to
// agent.
func (s *Service) ListCategories(ctx context.Context, caller Caller, kind expertrepo.Entity) ([]model.ExpertCategoryItem, error) {
	rows, err := s.repo.ListCategoriesWithCount(ctx, kind, caller.SpaceID, caller.UID)
	if err != nil {
		return nil, mapRepoError(err)
	}
	items := make([]model.ExpertCategoryItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, model.ExpertCategoryItem{
			ExpertCategoryID: r.ID,
			Name:             r.Name,
			Count:            r.Count,
		})
	}
	return items, nil
}

// validateTags bounds an already-normalized tag set: the number of tags and
// each tag's length. Without this a write can (a) 500 on the VARCHAR(128)
// expert_tags column with an over-long name and (b) flood the shared per-Space
// tag dictionary with unbounded rows. Mirrors the skill catalog's tag guard.
func validateTags(tags []string) error {
	if len(tags) > model.MaxExpertTags {
		return ErrInvalidRequest
	}
	for _, t := range tags {
		if utf8.RuneCountInString(t) > model.MaxExpertTagNameLen {
			return ErrInvalidRequest
		}
	}
	return nil
}

// validateGeneric enforces the shared name/summary/publisher rules.
func validateGeneric(name, summary, publisher string) error {
	if name == "" || summary == "" {
		return ErrInvalidRequest
	}
	if utf8.RuneCountInString(name) > model.MaxExpertNameLen {
		return ErrInvalidRequest
	}
	if utf8.RuneCountInString(summary) > model.MaxExpertSummaryLen {
		return ErrInvalidRequest
	}
	if utf8.RuneCountInString(publisher) > model.MaxExpertPublisherLen {
		return ErrInvalidRequest
	}
	return nil
}

// validatePublicCreateVisibility keeps the public create endpoint backward
// compatible with clients that still send public/private while rejecting
// system or unknown values (doc §4.1). The caller always persists public.
func validatePublicCreateVisibility(v model.Visibility) error {
	switch v {
	case "", model.VisibilityPublic, model.VisibilityPrivate:
		return nil
	default:
		return ErrInvalidVisibility
	}
}

// validateMCPConfig enforces the doc §6 rules: empty is allowed (no MCP); a
// non-empty value must be well-formed JSON and at most 64 KiB. Stored verbatim.
func validateMCPConfig(cfg string) error {
	if cfg == "" {
		return nil
	}
	if len(cfg) > model.MaxMCPConfigBytes {
		return ErrInvalidMCPConfig
	}
	if !json.Valid([]byte(cfg)) {
		return ErrInvalidMCPConfig
	}
	return nil
}

// deriveShortName returns the first 2 runes of name as the tile label
// (doc §3.2), capped at the column width.
func deriveShortName(name string) string {
	runes := []rune(strings.TrimSpace(name))
	if len(runes) > 2 {
		runes = runes[:2]
	}
	short := string(runes)
	if utf8.RuneCountInString(short) > model.MaxExpertShortNameLen {
		short = string([]rune(short)[:model.MaxExpertShortNameLen])
	}
	return short
}

// normalizeTagNames trims, drops empties, and de-duplicates tag names
// preserving order — matching the repository's tag resolver so a create/patch
// response reflects the same set a later read returns.
func normalizeTagNames(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// buildSkillRefs converts write-side skills to stored SkillRefs. Order
// preserved; a skill with an empty (trimmed) name AND no upload key is dropped.
// Each skill takes one of two forms, keyed under baseKeyFor(i) (a per-index
// folder prefix, e.g. experts/{id}/skills/{i}):
//
//   - Package upload (upload_object_key set): the whole .zip/.skill is stored
//     and its SKILL.md extracted — see storeSkillPackage. This is the
//     installable form; the ref carries both the SKILL.md key and the zip key.
//   - Inline content (content set): the SKILL.md text is PutObject'd at
//     {base}/SKILL.md. Empty content → a name-only ref.
//
// `invalid` is the caller-context validation sentinel (ErrInvalidRequest for
// experts, ErrInvalidMembers for squad members) returned on any client-side
// failure (oversize content, bad package, missing SKILL.md). When s.store is
// nil (storage-less tests) content/packages are ignored and skills are stored
// name-only.
//
// On PATCH the whole slice is rebuilt and objects are overwritten by index;
// cleanup of objects orphaned by a shorter new slice is DEFERRED (no GC in v1).
// buildSkillRefs converts write-side skills to stored SkillRefs. Order
// preserved; a skill with an empty (trimmed) name AND no upload key is dropped.
// Each NEW stored skill gets a unique object-key folder under keyPrefix
// ({keyPrefix}/{uuid}), so a PATCH never overwrites or collides with an existing
// skill's objects (old objects orphaned → deferred GC). A skill takes one of
// three forms:
//
//   - Package upload (upload_object_key set): the whole .zip/.skill is stored
//     and its SKILL.md extracted — see storeSkillPackage.
//   - Inline content (content set): the SKILL.md text is PutObject'd.
//   - Name-only (neither set): if `existing` holds a stored skill with the same
//     name, that ref is PRESERVED as-is (keeping its objects); otherwise a plain
//     name-only ref. This lets a read-modify-write PATCH that doesn't re-upload a
//     skill keep it intact instead of destroying the stored package.
//
// `invalid` is the caller-context validation sentinel (ErrInvalidRequest for
// experts, ErrInvalidMembers for squad members). When s.store is nil
// (storage-less tests) content/packages are ignored and skills are name-only.
func (s *Service) buildSkillRefs(ctx context.Context, skills []model.SkillWrite, keyPrefix string, existing []model.SkillRef, invalid error) ([]model.SkillRef, error) {
	// Bound the array: each package/content skill triggers an object-store write,
	// so an uncapped list on a single request fans out unboundedly.
	if len(skills) > model.MaxExpertSkills {
		return nil, invalid
	}
	// Index the preservable existing skills by name (those that actually carry
	// stored objects), consuming each at most once.
	preservable := make(map[string][]model.SkillRef)
	for _, r := range existing {
		if r.ObjectKey != "" || r.ZipObjectKey != "" {
			preservable[r.Name] = append(preservable[r.Name], r)
		}
	}

	refs := make([]model.SkillRef, 0, len(skills))
	for _, sk := range skills {
		name := strings.TrimSpace(sk.Name)
		uploadKey := strings.TrimSpace(sk.UploadObjectKey)
		if name == "" && uploadKey == "" {
			continue
		}
		switch {
		case uploadKey != "":
			ref, err := s.storeSkillPackage(ctx, sk, keyPrefix+"/"+s.idGen(), invalid)
			if err != nil {
				return nil, err
			}
			refs = append(refs, *ref)
		case strings.TrimSpace(sk.Content) != "":
			if len(sk.Content) > model.MaxSkillContentBytes {
				return nil, invalid
			}
			ref := model.SkillRef{Name: name}
			if s.store != nil {
				key := keyPrefix + "/" + s.idGen() + "/SKILL.md"
				if err := s.store.PutObject(ctx, key, strings.NewReader(sk.Content), int64(len(sk.Content)), "text/markdown; charset=utf-8"); err != nil {
					return nil, err
				}
				ref.ObjectKey = key
			}
			refs = append(refs, ref)
		default:
			// Name-only: preserve an existing stored skill of the same name.
			if queue := preservable[name]; len(queue) > 0 {
				refs = append(refs, queue[0])
				preservable[name] = queue[1:]
			} else {
				refs = append(refs, model.SkillRef{Name: name})
			}
		}
	}
	return refs, nil
}

// storeSkillPackage commits one uploaded skill package. It validates the upload
// key is a well-formed key under expertUploadPrefix, downloads the temp object,
// extracts SKILL.md + the (capped) file manifest (parse.ExtractZip), derives the
// authoritative name from the SKILL.md frontmatter (falling back to the wire
// name / filename stem), stores {base}/SKILL.md and copies the package to
// {base}/skill.zip, then best-effort deletes the temp object on every path. Any
// extraction/validation failure maps to `invalid` (a 400 for the caller).
func (s *Service) storeSkillPackage(ctx context.Context, sk model.SkillWrite, base string, invalid error) (*model.SkillRef, error) {
	uploadKey := strings.TrimSpace(sk.UploadObjectKey)
	if !isValidUploadKey(uploadKey) || s.store == nil {
		return nil, invalid
	}
	// Best-effort temp cleanup on ALL paths (success or failure) — the temp
	// upload is single-use; orphaned temps are otherwise GC-deferred.
	defer func() { _ = s.store.DeleteObject(ctx, uploadKey) }()

	tmpPath, size, cleanup, err := s.downloadToTempFile(ctx, uploadKey)
	if err != nil {
		// A missing / unreadable temp object is a stale-or-bad client upload.
		return nil, invalid
	}
	defer cleanup()

	res, code, _ := parse.ExtractZip(tmpPath, maxSkillPackageBytes)
	if code != "" {
		return nil, invalid
	}

	fm, _ := parse.ParseFrontmatter(res.SkillMDContent)
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = strings.TrimSpace(sk.Name)
	}
	if name == "" {
		name = fileStem(sk.FileName)
	}
	if name == "" {
		return nil, invalid
	}

	mdKey := base + "/SKILL.md"
	zipKey := base + "/skill.zip"
	md := res.SkillMDContent
	if err := s.store.PutObject(ctx, mdKey, bytes.NewReader(md), int64(len(md)), "text/markdown; charset=utf-8"); err != nil {
		return nil, err
	}
	// Persist the exact bytes we just validated — NOT a CopyObject of uploadKey.
	// The presigned PUT stays replayable for presignTTL, so the uploader could
	// swap in different bytes between ExtractZip (above) and here; copying the
	// mutable source would then store a package that never passed the
	// zip-slip/symlink/size checks. Re-upload the validated temp file instead.
	zf, err := os.Open(tmpPath)
	if err != nil {
		return nil, err
	}
	defer zf.Close()
	if err := s.store.PutObject(ctx, zipKey, zf, size, "application/zip"); err != nil {
		// A stray SKILL.md was written; leave it for the deferred GC.
		return nil, err
	}

	return &model.SkillRef{
		Name:         name,
		ObjectKey:    mdKey,
		ZipObjectKey: zipKey,
		FileName:     fileStem(sk.FileName) + skillPackageExt(sk.FileName),
		FileSize:     size, // authoritative: bytes actually stored, not client-claimed
		Files:        res.Files,
	}, nil
}

// downloadToTempFile streams a stored object to a temp file (capped at
// maxSkillPackageBytes) so parse.ExtractZip can open it by path. Returns the
// temp path, the number of bytes written, and a cleanup that removes the file.
func (s *Service) downloadToTempFile(ctx context.Context, key string) (string, int64, func(), error) {
	reader, err := s.store.GetObject(ctx, key)
	if err != nil {
		return "", 0, nil, err
	}
	defer reader.Close()
	f, err := os.CreateTemp("", "expert-skill-*.zip")
	if err != nil {
		return "", 0, nil, err
	}
	remove := func() { _ = os.Remove(f.Name()) }
	n, err := io.Copy(f, io.LimitReader(reader, maxSkillPackageBytes+1))
	if err != nil {
		_ = f.Close()
		remove()
		return "", 0, nil, err
	}
	if err := f.Close(); err != nil {
		remove()
		return "", 0, nil, err
	}
	return f.Name(), n, remove, nil
}

// InitSkillUpload validates a package filename/size and mints a presigned PUT
// URL under expertUploadPrefix. The client PUTs the raw .zip/.skill there, then
// echoes the returned upload_object_key back on create/update (doc §3.1).
func (s *Service) InitSkillUpload(ctx context.Context, fileName string, fileSize int64) (*SkillUploadInit, error) {
	name := filepath.Base(strings.TrimSpace(fileName))
	lower := strings.ToLower(name)
	if name == "" || name == "." || name == "/" ||
		(!strings.HasSuffix(lower, ".zip") && !strings.HasSuffix(lower, ".skill")) {
		return nil, ErrInvalidRequest
	}
	if fileSize <= 0 || fileSize > maxSkillPackageBytes {
		return nil, ErrInvalidRequest
	}
	if s.store == nil {
		return nil, ErrInvalidRequest
	}
	key := fmt.Sprintf("%s%s/%s", expertUploadPrefix, s.idGen(), name)
	url, headers, err := s.store.PresignPut(ctx, key, "application/zip", presignTTL)
	if err != nil {
		return nil, err
	}
	return &SkillUploadInit{
		UploadObjectKey: key,
		PresignedURL:    url,
		Method:          http.MethodPut,
		Headers:         flattenHeaders(headers),
		ExpiresIn:       int(presignTTL / time.Second),
	}, nil
}

// SkillUploadInit is the presigned-upload handshake result (service level).
type SkillUploadInit struct {
	UploadObjectKey string
	PresignedURL    string
	Method          string
	Headers         map[string]string
	ExpiresIn       int
}

// GetExpertSkillDownload returns a presigned GET URL for the expert's skill
// package at index i, applying the detail visibility rule. ErrNotFound for an
// out-of-range index or a skill with no stored package.
func (s *Service) GetExpertSkillDownload(ctx context.Context, caller Caller, id string, index int) (string, error) {
	m, err := s.loadVisibleExpert(ctx, caller, id)
	if err != nil {
		return "", err
	}
	return s.skillDownloadURL(ctx, m.Skills, index)
}

// GetSquadSkillDownload is the squad twin: it locates the member by member_key,
// then presigns that member's skill package at index i.
func (s *Service) GetSquadSkillDownload(ctx context.Context, caller Caller, id, memberKey string, index int) (string, error) {
	m, err := s.loadVisibleSquad(ctx, caller, id)
	if err != nil {
		return "", err
	}
	for i := range m.Members {
		if m.Members[i].MemberKey == memberKey {
			return s.skillDownloadURL(ctx, m.Members[i].Skills, index)
		}
	}
	return "", ErrNotFound
}

func (s *Service) skillDownloadURL(ctx context.Context, refs []model.SkillRef, index int) (string, error) {
	if index < 0 || index >= len(refs) {
		return "", ErrNotFound
	}
	key := refs[index].ZipObjectKey
	if key == "" || s.store == nil {
		return "", ErrNotFound
	}
	url, err := s.store.PresignGet(ctx, key, presignTTL)
	if err != nil {
		return "", fmt.Errorf("presign skill download: %w", err)
	}
	return url, nil
}

// flattenHeaders reduces multi-valued presign headers to the first value each,
// the shape the client sets verbatim on the PUT.
func flattenHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

// fileStem is the filename minus directories and the final extension.
func fileStem(fileName string) string {
	base := filepath.Base(strings.TrimSpace(fileName))
	if base == "." || base == "/" || base == "" {
		return ""
	}
	if i := strings.LastIndex(base, "."); i > 0 {
		return base[:i]
	}
	return base
}

// expertSkillsPrefix / squadMemberSkillsPrefix are the per-record folder under
// which each skill gets a unique {prefix}/{uuid} subfolder (SKILL.md + skill.zip
// live inside). Unique per-skill keys mean a PATCH never overwrites or collides
// with an existing skill's objects.
func expertSkillsPrefix(expertID string) string {
	return fmt.Sprintf("experts/%s/skills", expertID)
}

func squadMemberSkillsPrefix(squadID, memberKey string) string {
	return fmt.Sprintf("squads/%s/members/%s/skills", squadID, memberKey)
}

// isValidUploadKey accepts only well-formed, single-use upload keys of the exact
// shape expert-uploads/{segment}/{basename} with no path-traversal. This is
// defense-in-depth over the object store's own key handling: it prevents a
// crafted upload_object_key from referencing a permanent/other object (e.g. via
// "..") even if the backing store treats keys as opaque literals.
func isValidUploadKey(key string) bool {
	if !strings.HasPrefix(key, expertUploadPrefix) {
		return false
	}
	if strings.Contains(key, "..") {
		return false
	}
	rest := strings.TrimPrefix(key, expertUploadPrefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return false
	}
	return parts[0] != "" && parts[1] != ""
}

// skillPackageExt returns the package extension (".zip" or ".skill", lowercased)
// for display; empty when the name carries neither.
func skillPackageExt(fileName string) string {
	lower := strings.ToLower(strings.TrimSpace(fileName))
	switch {
	case strings.HasSuffix(lower, ".skill"):
		return ".skill"
	case strings.HasSuffix(lower, ".zip"):
		return ".zip"
	default:
		return ""
	}
}

// GetExpertSkillMD returns the stored content of the expert's skill at index i,
// applying the same visibility rule as the detail endpoint (cross-Space →
// ErrNotFound). An out-of-range index or a name-only skill (empty ObjectKey)
// yields ErrNotFound; a storage read failure surfaces as a wrapped error (→500).
func (s *Service) GetExpertSkillMD(ctx context.Context, caller Caller, id string, index int) (string, error) {
	m, err := s.loadVisibleExpert(ctx, caller, id)
	if err != nil {
		return "", err
	}
	return s.readSkillContent(ctx, m.Skills, index)
}

// GetSquadSkillMD is the squad twin of GetExpertSkillMD: it locates the member
// by member_key, then reads that member's skill at index i.
func (s *Service) GetSquadSkillMD(ctx context.Context, caller Caller, id, memberKey string, index int) (string, error) {
	m, err := s.loadVisibleSquad(ctx, caller, id)
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

// readSkillContent indexes into refs, fetches the object, and returns the
// content capped at maxSkillReadBytes.
func (s *Service) readSkillContent(ctx context.Context, refs []model.SkillRef, index int) (string, error) {
	if index < 0 || index >= len(refs) {
		return "", ErrNotFound
	}
	key := refs[index].ObjectKey
	if key == "" || s.store == nil {
		return "", ErrNotFound
	}
	reader, err := s.store.GetObject(ctx, key)
	if err != nil {
		return "", fmt.Errorf("get skill content: %w", err)
	}
	defer reader.Close()
	data, err := readLimited(reader, maxSkillReadBytes)
	if err != nil {
		return "", fmt.Errorf("read skill content: %w", err)
	}
	return string(data), nil
}

// readLimited reads at most maxBytes from reader, erroring if the object is
// larger. Mirrors the skill package's helper of the same name.
func readLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("invalid size limit")
	}
	limited := io.LimitReader(reader, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("content exceeds size limit")
	}
	return data, nil
}

func hasNonEmpty(values []string) bool { return countNonEmpty(values) > 0 }

// countNonEmpty counts distinct trimmed non-empty values, matching the
// dedup+trim the repository's tag resolver applies.
func countNonEmpty(values []string) int {
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			seen[v] = struct{}{}
		}
	}
	return len(seen)
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

func clampOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

// mapRepoError translates repository sentinels to service sentinels; anything
// else passes through (the handler renders it as an internal 500).
func mapRepoError(err error) error {
	switch {
	case errors.Is(err, expertrepo.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, expertrepo.ErrNameTaken):
		return ErrNameTaken
	default:
		return err
	}
}
