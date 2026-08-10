package model

import "time"

// Expert marketplace field length caps (in Unicode code points). These mirror
// the migration column widths (migrations/sql/20260806-00-expert-marketplace.sql)
// and are the authoritative server-side bounds; the octo-web dmworkmcp create
// forms mirror the same numbers.
const (
	MaxExpertNameLen      = 128
	MaxExpertSummaryLen   = 512
	MaxExpertPublisherLen = 128
	MaxExpertShortNameLen = 16
	MaxSquadLeaderLen     = 128
	MaxSquadPermissionLen = 512
	// MaxExpertTextLen bounds member role / free-text list entries so a member
	// role or dependency string cannot bloat members_json unbounded.
	MaxExpertTextLen = 500
	// MaxExpertTags caps the number of tags per record and MaxExpertTagNameLen
	// each tag's length. Bounds the shared per-Space expert_tags dictionary a
	// single write can grow and keeps a tag inside the VARCHAR(128) column (an
	// over-long name would otherwise surface as a 500 on insert).
	MaxExpertTags       = 20
	MaxExpertTagNameLen = 64
	// MaxMemberKeyLen bounds a squad member_key. The key is interpolated into a
	// storage object prefix, so it is also charset-restricted (see the service);
	// a long or exotic key would otherwise yield 500s or backend key-limit errors.
	MaxMemberKeyLen = 64
	// Collection caps bound every client-supplied array on the write path so a
	// single (≤8 MiB) request can't fan out to an unbounded number of object-store
	// writes or an oversized JSON column. MaxExpertSkills applies per expert AND
	// per squad member; MaxSquadMembers per squad; the strategy/dependency caps
	// bound the squad dispatch lists.
	MaxExpertSkills      = 20
	MaxSquadMembers      = 30
	MaxSquadStrategies   = 30
	MaxSquadDependencies = 50
	// MaxMCPConfigBytes caps the raw mcp_config document (doc §6, v1: 64 KiB).
	// Measured in bytes because the config is stored verbatim as text.
	MaxMCPConfigBytes = 64 << 10
	// MaxSkillContentBytes caps a single skill's markdown/plain-text content on
	// write (doc §3.1, v1: 1 MiB). Content over the cap is rejected. Measured in
	// bytes because the content is stored verbatim in object storage.
	MaxSkillContentBytes = 1 << 20
)

// SkillRef is the stored representation of one skill on an ExpertSpec. Name is
// the skill's display name; ObjectKey is the storage key of its SKILL.md
// (markdown/plain-text) content, empty for a name-only skill.
//
// When the skill was uploaded as a whole .zip/.skill package (the installable
// form), the raw package is stored too and these carry it: ZipObjectKey is the
// storage key of the package, FileName/FileSize its original name/size, and
// Files the manifest of paths inside it (for the detail file list). A skill
// with a non-empty ZipObjectKey is downloadable.
//
// The write wire carries {name, content} (legacy inline) OR
// {name, upload_object_key, file_name, file_size} (package upload); the read
// wire carries {name, has_content, can_download, file_name, file_size, files}
// (has_content == ObjectKey != ""; can_download == ZipObjectKey != "").
type SkillRef struct {
	Name         string   `json:"name"`
	ObjectKey    string   `json:"object_key"`
	ZipObjectKey string   `json:"zip_object_key,omitempty"`
	FileName     string   `json:"file_name,omitempty"`
	FileSize     int64    `json:"file_size,omitempty"`
	Files        []string `json:"files,omitempty"`
}

// Expert is the domain model for a single expert (专家), persisted in the
// experts table. JSON collections are held as native Go types here; the
// repository marshals them to the JSON columns. Tags carry NAMES in this
// struct — the repository resolves them to/from ids in expert_tags.
type Expert struct {
	ID               string
	ShortName        string
	Name             string
	Summary          string
	Category         string // category_id from the shared categories table
	Tags             []string
	Publisher        string
	OwnerUID         string
	SpaceID          string // empty string means NULL
	CreatorName      string
	CreatedByType    CreatedByType
	CreatedByBotUID  string
	CreatedByBotName string
	Visibility       Visibility
	Instruction      string
	MCPConfig        string
	Skills           []SkillRef
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

// SquadMember is one member expert inside a squad — an ExpertSpec (instruction
// + mcp_config + skills) plus team-role metadata. Members are self-contained
// snapshots, NOT foreign keys into experts (brief). Skills carry NAMES on the
// wire but are stored as SkillRef in members_json.
type SquadMember struct {
	MemberKey   string
	TemplateID  string
	Name        string
	Role        string
	IsLeader    bool
	Instruction string
	MCPConfig   string
	Skills      []SkillRef
}

// SquadDependencies is the {blocking, recommended} dependency block (doc §3.5).
type SquadDependencies struct {
	Blocking    []string `json:"blocking"`
	Recommended []string `json:"recommended"`
}

// Squad is the domain model for an expert team (专家团), persisted in the
// expert_squads table. It shares the generic marketplace metadata with Expert
// and swaps the ExpertSpec for the squad dispatch payload.
type Squad struct {
	ID               string
	ShortName        string
	Name             string
	Summary          string
	Category         string
	Tags             []string
	Publisher        string
	OwnerUID         string
	SpaceID          string
	CreatorName      string
	CreatedByType    CreatedByType
	CreatedByBotUID  string
	CreatedByBotName string
	Visibility       Visibility
	Leader           string
	Strategies       []string
	Dependencies     SquadDependencies
	Permission       string
	Members          []SquadMember
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}
