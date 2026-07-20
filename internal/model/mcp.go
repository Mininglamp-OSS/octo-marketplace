package model

import "time"

// Shared constants aligned byte-for-byte with docs/api/mcp-v1.md §0 and the
// frontend packages/dmworkmcp/src/utils/constants.ts. Changing these literals
// is a breaking change for any deployed frontend/backend pair.
const (
	// SecretPlaceholderSentinel is the only non-empty value the backend accepts
	// for a token-like env/header key. The frontend renders a localized label
	// but always submits this ASCII literal (doc §5).
	SecretPlaceholderSentinel = "__OCTO_SECRET_PLACEHOLDER__"

	// CategoryKeyAll disables the category filter on the list endpoints (doc §0).
	CategoryKeyAll = "all"
)

// Visibility scopes (doc §3.1). system never appears in a client write.
type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
	VisibilitySystem  Visibility = "system"
	VisibilitySpace   Visibility = "space"
)

// CreatedByType records who authored an MCP row (mcp-v1.md §3.1; issue #894).
// A Bot-created MCP is owned by the Bot's owner (owner_uid == owner user) with
// no privilege delta — this value is a metadata badge for the market UI only.
type CreatedByType string

const (
	CreatedByHuman  CreatedByType = "human"
	CreatedByBot    CreatedByType = "bot"
	CreatedByImport CreatedByType = "import" // reserved for Git import (#867); not written today.
)

// Transport kinds, per the MCP spec (doc §4.1).
type Transport string

const (
	TransportStdio          Transport = "stdio"
	TransportStreamableHTTP Transport = "streamable-http"
	TransportSSE            Transport = "sse"
)

// ValidTransport reports whether t is one of the three supported transports.
func ValidTransport(t Transport) bool {
	switch t {
	case TransportStdio, TransportStreamableHTTP, TransportSSE:
		return true
	default:
		return false
	}
}

// Field length limits (in Unicode code points, matching the frontend's
// maxLength which counts UTF-16 units for BMP text but is treated as a
// character cap here). These are the authoritative server-side bounds; the
// frontend maxLength in packages/dmworkmcp/src/components/McpCreateModal.tsx
// mirrors the same numbers. Keep the two in sync — changing a limit here is a
// contract change for the create/edit form.
const (
	MaxNameLen        = 64
	MaxSloganLen      = 200
	MaxURLLen         = 2048
	MaxCommandLen     = 256
	MaxArgLen         = 512  // per single arg entry
	MaxHeaderKeyLen   = 128  // per single header key
	MaxHeaderValueLen = 1024 // per single header value
	MaxToolNameLen    = 64
	// MaxTextLen bounds free-text fields shown on the detail modal: each tool
	// description, each FAQ question/answer, and each note / usage example.
	MaxTextLen = 500
)

// Tool is a single tool exposed by an MCP server (doc §3.1, frontend McpTool).
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// FAQ is one question/answer pair shown on the detail modal.
type FAQ struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Connection is the persisted, redacted connection config. It is stored in the
// config_json column and projected onto QuickStart on read (doc §3.3). It never
// contains secret values (doc §5).
type Connection struct {
	URL        string            `json:"url,omitempty"`
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	AuthType   string            `json:"authType,omitempty"`
	ServerName string            `json:"serverName,omitempty"`
}

// MCP is the domain model persisted in mcp_servers. JSON collections are held
// as native Go types here; the repository marshals them to the JSON columns.
type MCP struct {
	ID   string
	Name string
	// Slug is the ASCII identifier used as the JSON key in generated
	// mcpServers snippets (mcp-v1.md §3). Unique per Space among live rows
	// (see migration 03). Never empty on persisted rows — the service
	// auto-derives it from Name when the request omits it.
	Slug          string
	Slogan        string
	Category      string
	Icon          string
	IconVersion   int
	Tags          []string
	Tools         []Tool
	UsageExamples []string
	FAQs          []FAQ
	Notes         []string
	Visibility    Visibility
	OwnerUID      string
	SpaceID       string // empty string means NULL (system rows)
	CreatorName   string
	// CreatedByType / CreatedByBotUID / CreatedByBotName record provenance
	// (issue #894). For human creates CreatedByType == CreatedByHuman and the
	// two bot fields are empty. For bot creates the fields are stamped from
	// the resolved BotIdentity — CreatedByBotName is a snapshot so the market
	// badge stays intact after the bot is renamed or deleted.
	CreatedByType    CreatedByType
	CreatedByBotUID  string
	CreatedByBotName string
	Transport        Transport
	Connection       Connection
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}
