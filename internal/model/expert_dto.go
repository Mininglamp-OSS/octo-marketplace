package model

// This file holds the Expert Marketplace wire DTOs (docs/api/expert-v1.md §3)
// and the domain -> wire projections. Field names are snake_case and match the
// octo-web dmworkmcp ExpertAgent / ExpertSquad / ExpertMember shapes where they
// overlap. It reuses the shared helpers in mcp_dto.go (FormatTimestamp,
// nonNilStrings, emptyToNilStrings, normalizeCreatedByType) since both DTO
// families live in package model.

// ─── Shared skill wire shapes (doc §3.1) ─────────────────────────────────────

// SkillWrite is one skill on a write body (POST/PATCH expert, squad member).
// It has two mutually-exclusive forms:
//   - Package upload (preferred, installable): upload_object_key names a
//     just-uploaded .zip/.skill temp object; the server extracts SKILL.md, copies
//     the package to permanent storage, and records file_name/file_size. name is
//     advisory (the server derives the authoritative name from SKILL.md).
//   - Inline content (legacy / name-only): content carries the SKILL.md text
//     directly. Empty content + empty upload_object_key yields a name-only skill.
//
// The server never echoes content or the package back on read.
type SkillWrite struct {
	Name            string `json:"name"`
	Content         string `json:"content"`
	UploadObjectKey string `json:"upload_object_key"`
	FileName        string `json:"file_name"`
	FileSize        int64  `json:"file_size"`
}

// SkillRead is one skill on a read/detail body: the name, a has_content flag
// (true iff stored SKILL.md content exists), and — when the skill was uploaded
// as a package — can_download plus the package's file_name/file_size and the
// file manifest. Content/package bytes are fetched separately (skill_md /
// download endpoints).
type SkillRead struct {
	Name        string   `json:"name"`
	HasContent  bool     `json:"has_content"`
	CanDownload bool     `json:"can_download,omitempty"`
	FileName    string   `json:"file_name,omitempty"`
	FileSize    int64    `json:"file_size,omitempty"`
	Files       []string `json:"files,omitempty"`
}

// ─── Expert (专家 / single agent) ───────────────────────────────────────────

// ExpertCreateRequest is the FLAT create body for POST /experts (doc §4.1).
// Server-owned fields (expert_id, short_name, creator_name, created_by_*,
// created_at, updated_at, owner_uid, space_id) are intentionally absent so the
// strict decoder rejects them as unknown fields. `visibility` is accepted for
// compatibility but ignored (new records are always public); `system` is
// rejected. `publisher` is descriptive and accepted.
type ExpertCreateRequest struct {
	Name        string       `json:"name"`
	Summary     string       `json:"summary"`
	Category    string       `json:"category"`
	Tags        []string     `json:"tags"`
	Publisher   string       `json:"publisher"`
	Visibility  Visibility   `json:"visibility"`
	Instruction string       `json:"instruction"`
	MCPConfig   string       `json:"mcp_config"`
	Skills      []SkillWrite `json:"skills"`
}

// ExpertPatchRequest is the FLAT partial-update body for PATCH /experts/{id}
// (doc §4.5). Every mutable field is a pointer so an omitted field is
// distinguishable from a zero value. Immutable/identity fields are absent and
// thus rejected as unknown by the strict decoder.
type ExpertPatchRequest struct {
	Name        *string       `json:"name"`
	Summary     *string       `json:"summary"`
	Category    *string       `json:"category"`
	Tags        *[]string     `json:"tags"`
	Publisher   *string       `json:"publisher"`
	Visibility  *Visibility   `json:"visibility"`
	Instruction *string       `json:"instruction"`
	MCPConfig   *string       `json:"mcp_config"`
	Skills      *[]SkillWrite `json:"skills"`
}

// ExpertAgentDetail is the full record for GET/POST/PATCH /experts (doc §3.2).
// owner_uid is never surfaced.
type ExpertAgentDetail struct {
	ExpertID         string        `json:"expert_id"`
	ShortName        string        `json:"short_name"`
	Name             string        `json:"name"`
	Summary          string        `json:"summary"`
	Category         string        `json:"category"`
	Tags             []string      `json:"tags"`
	Publisher        string        `json:"publisher"`
	Visibility       Visibility    `json:"visibility"`
	CreatorName      string        `json:"creator_name"`
	CreatedByType    CreatedByType `json:"created_by_type"`
	CreatedByBotUID  string        `json:"created_by_bot_uid,omitempty"`
	CreatedByBotName string        `json:"created_by_bot_name,omitempty"`
	Instruction      string        `json:"instruction,omitempty"`
	MCPConfig        string        `json:"mcp_config,omitempty"`
	Skills           []SkillRead   `json:"skills,omitempty"`
	CreatedAt        string        `json:"created_at"`
	UpdatedAt        string        `json:"updated_at"`
}

// ExpertAgentListItem is the list projection (doc §3.3) — detail minus the
// heavy ExpertSpec trio (instruction / mcp_config / skills).
type ExpertAgentListItem struct {
	ExpertID         string        `json:"expert_id"`
	ShortName        string        `json:"short_name"`
	Name             string        `json:"name"`
	Summary          string        `json:"summary"`
	Category         string        `json:"category"`
	Tags             []string      `json:"tags"`
	Publisher        string        `json:"publisher"`
	Visibility       Visibility    `json:"visibility"`
	CreatorName      string        `json:"creator_name"`
	CreatedByType    CreatedByType `json:"created_by_type"`
	CreatedByBotUID  string        `json:"created_by_bot_uid,omitempty"`
	CreatedByBotName string        `json:"created_by_bot_name,omitempty"`
}

// ToAgentDetail projects a domain Expert onto the detail wire shape.
func (e *Expert) ToAgentDetail() ExpertAgentDetail {
	return ExpertAgentDetail{
		ExpertID:         e.ID,
		ShortName:        e.ShortName,
		Name:             e.Name,
		Summary:          e.Summary,
		Category:         e.Category,
		Tags:             nonNilStrings(e.Tags),
		Publisher:        e.Publisher,
		Visibility:       e.Visibility,
		CreatorName:      e.CreatorName,
		CreatedByType:    normalizeCreatedByType(e.CreatedByType),
		CreatedByBotUID:  e.CreatedByBotUID,
		CreatedByBotName: e.CreatedByBotName,
		Instruction:      e.Instruction,
		MCPConfig:        e.MCPConfig,
		Skills:           skillRefsToRead(e.Skills),
		CreatedAt:        FormatTimestamp(e.CreatedAt),
		UpdatedAt:        FormatTimestamp(e.UpdatedAt),
	}
}

// ToAgentListItem projects a domain Expert onto the list-card wire shape.
func (e *Expert) ToAgentListItem() ExpertAgentListItem {
	return ExpertAgentListItem{
		ExpertID:         e.ID,
		ShortName:        e.ShortName,
		Name:             e.Name,
		Summary:          e.Summary,
		Category:         e.Category,
		Tags:             nonNilStrings(e.Tags),
		Publisher:        e.Publisher,
		Visibility:       e.Visibility,
		CreatorName:      e.CreatorName,
		CreatedByType:    normalizeCreatedByType(e.CreatedByType),
		CreatedByBotUID:  e.CreatedByBotUID,
		CreatedByBotName: e.CreatedByBotName,
	}
}

// ─── Squad (专家团 / expert team) ────────────────────────────────────────────

// SquadMemberInput is the wire shape for one squad member on a write body
// (POST/PATCH squad). member_key / template_id are optional on write (server
// fills defaults). Skills carry inline {name, content} (doc §3.1/§3.4).
type SquadMemberInput struct {
	MemberKey   string       `json:"member_key"`
	TemplateID  string       `json:"template_id"`
	Name        string       `json:"name"`
	Role        string       `json:"role"`
	IsLeader    bool         `json:"is_leader"`
	Instruction string       `json:"instruction,omitempty"`
	MCPConfig   string       `json:"mcp_config,omitempty"`
	Skills      []SkillWrite `json:"skills,omitempty"`
}

// SquadMemberIO is the wire shape for one squad member on a detail response
// (doc §3.4). member_key / template_id are always present on read; skills
// carry {name, has_content}.
type SquadMemberIO struct {
	MemberKey   string      `json:"member_key"`
	TemplateID  string      `json:"template_id"`
	Name        string      `json:"name"`
	Role        string      `json:"role"`
	IsLeader    bool        `json:"is_leader"`
	Instruction string      `json:"instruction,omitempty"`
	MCPConfig   string      `json:"mcp_config,omitempty"`
	Skills      []SkillRead `json:"skills,omitempty"`
}

// SquadCreateRequest is the FLAT create body for POST /squads (doc §4.7).
type SquadCreateRequest struct {
	Name         string             `json:"name"`
	Summary      string             `json:"summary"`
	Category     string             `json:"category"`
	Tags         []string           `json:"tags"`
	Publisher    string             `json:"publisher"`
	Visibility   Visibility         `json:"visibility"`
	Leader       string             `json:"leader"`
	Strategies   []string           `json:"strategies"`
	Dependencies SquadDependencies  `json:"dependencies"`
	Permission   string             `json:"permission"`
	Members      []SquadMemberInput `json:"members"`
}

// SquadPatchRequest is the FLAT partial-update body for PATCH /squads/{id}.
// Sending members replaces the whole array (full-replace, not merge).
type SquadPatchRequest struct {
	Name         *string             `json:"name"`
	Summary      *string             `json:"summary"`
	Category     *string             `json:"category"`
	Tags         *[]string           `json:"tags"`
	Publisher    *string             `json:"publisher"`
	Visibility   *Visibility         `json:"visibility"`
	Leader       *string             `json:"leader"`
	Strategies   *[]string           `json:"strategies"`
	Dependencies *SquadDependencies  `json:"dependencies"`
	Permission   *string             `json:"permission"`
	Members      *[]SquadMemberInput `json:"members"`
}

// ExpertSquadDetail is the full record for GET/POST/PATCH /squads (doc §3.5).
type ExpertSquadDetail struct {
	SquadID          string            `json:"squad_id"`
	ShortName        string            `json:"short_name"`
	Name             string            `json:"name"`
	Summary          string            `json:"summary"`
	Category         string            `json:"category"`
	Tags             []string          `json:"tags"`
	Publisher        string            `json:"publisher"`
	Visibility       Visibility        `json:"visibility"`
	CreatorName      string            `json:"creator_name"`
	CreatedByType    CreatedByType     `json:"created_by_type"`
	CreatedByBotUID  string            `json:"created_by_bot_uid,omitempty"`
	CreatedByBotName string            `json:"created_by_bot_name,omitempty"`
	Leader           string            `json:"leader"`
	Strategies       []string          `json:"strategies"`
	Dependencies     SquadDependencies `json:"dependencies"`
	Permission       string            `json:"permission"`
	Members          []SquadMemberIO   `json:"members"`
	CreatedAt        string            `json:"created_at"`
	UpdatedAt        string            `json:"updated_at"`
}

// ExpertSquadListItem is the list projection (doc §3.6) — drops the heavy squad
// payload and adds member_count.
type ExpertSquadListItem struct {
	SquadID          string        `json:"squad_id"`
	ShortName        string        `json:"short_name"`
	Name             string        `json:"name"`
	Summary          string        `json:"summary"`
	Category         string        `json:"category"`
	Tags             []string      `json:"tags"`
	Publisher        string        `json:"publisher"`
	Visibility       Visibility    `json:"visibility"`
	CreatorName      string        `json:"creator_name"`
	CreatedByType    CreatedByType `json:"created_by_type"`
	CreatedByBotUID  string        `json:"created_by_bot_uid,omitempty"`
	CreatedByBotName string        `json:"created_by_bot_name,omitempty"`
	MemberCount      int           `json:"member_count"`
}

// ToSquadDetail projects a domain Squad onto the detail wire shape.
func (s *Squad) ToSquadDetail() ExpertSquadDetail {
	members := make([]SquadMemberIO, 0, len(s.Members))
	for i := range s.Members {
		members = append(members, memberToWire(&s.Members[i]))
	}
	return ExpertSquadDetail{
		SquadID:          s.ID,
		ShortName:        s.ShortName,
		Name:             s.Name,
		Summary:          s.Summary,
		Category:         s.Category,
		Tags:             nonNilStrings(s.Tags),
		Publisher:        s.Publisher,
		Visibility:       s.Visibility,
		CreatorName:      s.CreatorName,
		CreatedByType:    normalizeCreatedByType(s.CreatedByType),
		CreatedByBotUID:  s.CreatedByBotUID,
		CreatedByBotName: s.CreatedByBotName,
		Leader:           s.Leader,
		Strategies:       nonNilStrings(s.Strategies),
		Dependencies: SquadDependencies{
			Blocking:    nonNilStrings(s.Dependencies.Blocking),
			Recommended: nonNilStrings(s.Dependencies.Recommended),
		},
		Permission: s.Permission,
		Members:    members,
		CreatedAt:  FormatTimestamp(s.CreatedAt),
		UpdatedAt:  FormatTimestamp(s.UpdatedAt),
	}
}

// ToSquadListItem projects a domain Squad onto the list-card wire shape.
func (s *Squad) ToSquadListItem() ExpertSquadListItem {
	return ExpertSquadListItem{
		SquadID:          s.ID,
		ShortName:        s.ShortName,
		Name:             s.Name,
		Summary:          s.Summary,
		Category:         s.Category,
		Tags:             nonNilStrings(s.Tags),
		Publisher:        s.Publisher,
		Visibility:       s.Visibility,
		CreatorName:      s.CreatorName,
		CreatedByType:    normalizeCreatedByType(s.CreatedByType),
		CreatedByBotUID:  s.CreatedByBotUID,
		CreatedByBotName: s.CreatedByBotName,
		MemberCount:      len(s.Members),
	}
}

func memberToWire(m *SquadMember) SquadMemberIO {
	return SquadMemberIO{
		MemberKey:   m.MemberKey,
		TemplateID:  m.TemplateID,
		Name:        m.Name,
		Role:        m.Role,
		IsLeader:    m.IsLeader,
		Instruction: m.Instruction,
		MCPConfig:   m.MCPConfig,
		Skills:      skillRefsToRead(m.Skills),
	}
}

// ─── Categories (专家市场分类) ────────────────────────────────────────────────

// ExpertCategoryItem is one category chip returned by GET /expert_categories
// (doc §5). expert_category_id is the stored category id; name is the display
// label the wire uses everywhere else for `category`; count is the number of
// records of the requested kind visible to the caller in their Space.
type ExpertCategoryItem struct {
	ExpertCategoryID string `json:"expert_category_id"`
	Name             string `json:"name"`
	Count            int    `json:"count"`
}

// skillRefsToRead projects stored SkillRefs onto the read wire shape
// ({name, has_content, can_download, file_name, file_size, files}). An empty
// set collapses to nil so the omitempty tag drops the field entirely.
func skillRefsToRead(refs []SkillRef) []SkillRead {
	if len(refs) == 0 {
		return nil
	}
	out := make([]SkillRead, 0, len(refs))
	for _, r := range refs {
		out = append(out, SkillRead{
			Name:        r.Name,
			HasContent:  r.ObjectKey != "",
			CanDownload: r.ZipObjectKey != "",
			FileName:    r.FileName,
			FileSize:    r.FileSize,
			Files:       r.Files,
		})
	}
	return out
}
