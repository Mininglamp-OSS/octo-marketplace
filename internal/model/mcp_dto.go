package model

import "time"

// timeMillisLayout renders RFC 3339 with millisecond precision and the numeric
// offset of the server's local timezone (doc §3.1: "RFC 3339 with millisecond
// precision, in the server's local timezone"), e.g. 2026-07-14T18:30:12.123+08:00.
const timeMillisLayout = "2006-01-02T15:04:05.000Z07:00"

// FormatTimestamp renders t per the wire timestamp contract (doc §3.1).
func FormatTimestamp(t time.Time) string {
	return t.Format(timeMillisLayout)
}

// CreateRequest is the FLAT create body (doc §4.1, mirrors the frontend
// CreateMcpParams). Identity and server-derived fields are ignored if present
// (doc §3.3) — they are simply absent from this struct.
type CreateRequest struct {
	Name string `json:"name"`
	// Slug is the ASCII identifier used as the JSON key in the generated
	// mcpServers snippet (mcp-v1.md §3, "服务标识"). Optional on the wire —
	// when omitted or empty the server auto-slugifies Name. Must match
	// ^[a-z0-9-]{1,64}$ after normalization. Unique per Space among live rows.
	Slug          string            `json:"slug"`
	Slogan        string            `json:"slogan"`
	Category      string            `json:"category"`
	Icon          string            `json:"icon"`
	Tags          []string          `json:"tags"`
	Transport     Transport         `json:"transport"`
	URL           string            `json:"url"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	Headers       map[string]string `json:"headers"`
	AuthType      string            `json:"auth_type"`
	Tools         []Tool            `json:"tools"`
	UsageExamples []string          `json:"usage_examples"`
	FAQs          []FAQ             `json:"faqs"`
	Notes         []string          `json:"notes"`
	Visibility    Visibility        `json:"visibility"`
}

// PatchRequest is the FLAT partial-update body (doc §4.5). Every field is a
// pointer so an omitted field is distinguishable from a zero value and left
// untouched. Immutable fields (id, owner_uid, space_id, creator_name,
// createdAt) are not present and thus ignored, not rejected.
type PatchRequest struct {
	Name *string `json:"name"`
	// Slug — same rules as CreateRequest.Slug. Nil pointer leaves the
	// existing slug untouched; a non-nil empty string is rejected as
	// slug_invalid (empty is not a valid identifier).
	Slug          *string            `json:"slug"`
	Slogan        *string            `json:"slogan"`
	Category      *string            `json:"category"`
	Icon          *string            `json:"icon"`
	Tags          *[]string          `json:"tags"`
	Transport     *Transport         `json:"transport"`
	URL           *string            `json:"url"`
	Command       *string            `json:"command"`
	Args          *[]string          `json:"args"`
	Env           *map[string]string `json:"env"`
	Headers       *map[string]string `json:"headers"`
	AuthType      *string            `json:"auth_type"`
	Tools         *[]Tool            `json:"tools"`
	UsageExamples *[]string          `json:"usage_examples"`
	FAQs          *[]FAQ             `json:"faqs"`
	Notes         *[]string          `json:"notes"`
	Visibility    *Visibility        `json:"visibility"`
}

// QuickStart is the NESTED connection block on read responses (doc §3.3).
// Empty collections collapse to omitted via omitempty.
type QuickStart struct {
	Transport  Transport `json:"transport"`
	ServerName string    `json:"server_name"`
	// Slug is the ASCII identifier used as the JSON key in the mcpServers
	// snippet. Frontend template prefers this over ServerName when generating
	// the config; always present in responses for records created after
	// migration 03.
	Slug     string            `json:"slug,omitempty"`
	URL      string            `json:"url,omitempty"`
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	AuthType string            `json:"auth_type,omitempty"`
}

// Detail is the full record returned by GET /mcps/{id}, POST /mcps, PATCH
// /mcps/{id} (doc §3.1). owner_uid is never surfaced.
type Detail struct {
	ID          string     `json:"mcp_id"`
	Name        string     `json:"name"`
	Slogan      string     `json:"slogan"`
	Category    string     `json:"category"`
	Icon        string     `json:"icon"`
	Tags        []string   `json:"tags"`
	ToolCount   int        `json:"tool_count"`
	Visibility  Visibility `json:"visibility"`
	CreatorName string     `json:"creator_name"`
	// CreatedByType is always present (falls back to "human" for legacy rows
	// pre-#894). CreatedByBotUID / CreatedByBotName are omitted when this row
	// was authored by a human — the frontend uses the presence of the bot
	// fields to render the 🤖 badge.
	CreatedByType    CreatedByType `json:"created_by_type"`
	CreatedByBotUID  string        `json:"created_by_bot_uid,omitempty"`
	CreatedByBotName string        `json:"created_by_bot_name,omitempty"`
	QuickStart       QuickStart    `json:"quick_start"`
	Tools            []Tool        `json:"tools"`
	UsageExamples    []string      `json:"usage_examples"`
	FAQs             []FAQ         `json:"faqs"`
	Notes            []string      `json:"notes"`
	CreatedAt        string        `json:"created_at"`
	UpdatedAt        string        `json:"updated_at"`
}

// ListItem is the projection used by GET /mcps and GET /mcps/mine (doc §3.2).
type ListItem struct {
	ID          string     `json:"mcp_id"`
	Name        string     `json:"name"`
	Slogan      string     `json:"slogan"`
	Category    string     `json:"category"`
	Icon        string     `json:"icon"`
	Tags        []string   `json:"tags"`
	ToolCount   int        `json:"tool_count"`
	Visibility  Visibility `json:"visibility"`
	CreatorName string     `json:"creator_name"`
	// Same shape + semantics as Detail (see there for rationale).
	CreatedByType    CreatedByType `json:"created_by_type"`
	CreatedByBotUID  string        `json:"created_by_bot_uid,omitempty"`
	CreatedByBotName string        `json:"created_by_bot_name,omitempty"`
	Source           string        `json:"source"`
	Transport        Transport     `json:"transport"`
	UpdatedAt        string        `json:"updated_at"`
	MatchReasons     []string      `json:"match_reasons,omitempty"`
	Relevance        int           `json:"relevance,omitempty"`
}

// CategoryFilter is one filter pill with its live count (doc §4.2). Labels are
// the frontend's responsibility; only key + count are on the wire.
type CategoryFilter struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// ListResponse is the envelope for both list endpoints (doc §4.2).
type ListResponse struct {
	Items      []ListItem       `json:"items"`
	Total      int              `json:"total"`
	Categories []CategoryFilter `json:"categories"`
}

// ToDetail projects a domain MCP onto the nested Detail wire shape. Secret
// redaction is expected to have happened before persistence (doc §5), so the
// stored Connection is already safe to echo.
func (m *MCP) ToDetail() Detail {
	serverName := m.Connection.ServerName
	if serverName == "" {
		serverName = m.Name
	}
	return Detail{
		ID:               m.ID,
		Name:             m.Name,
		Slogan:           m.Slogan,
		Category:         m.Category,
		Icon:             m.Icon,
		Tags:             nonNilStrings(m.Tags),
		ToolCount:        len(m.Tools),
		Visibility:       m.Visibility,
		CreatorName:      m.CreatorName,
		CreatedByType:    normalizeCreatedByType(m.CreatedByType),
		CreatedByBotUID:  m.CreatedByBotUID,
		CreatedByBotName: m.CreatedByBotName,
		QuickStart: QuickStart{
			Transport:  m.Transport,
			ServerName: serverName,
			Slug:       m.Slug,
			URL:        m.Connection.URL,
			Command:    m.Connection.Command,
			Args:       emptyToNilStrings(m.Connection.Args),
			Env:        emptyToNilMap(m.Connection.Env),
			Headers:    emptyToNilMap(m.Connection.Headers),
			AuthType:   m.Connection.AuthType,
		},
		Tools:         nonNilTools(m.Tools),
		UsageExamples: nonNilStrings(m.UsageExamples),
		FAQs:          nonNilFAQs(m.FAQs),
		Notes:         nonNilStrings(m.Notes),
		CreatedAt:     FormatTimestamp(m.CreatedAt),
		UpdatedAt:     FormatTimestamp(m.UpdatedAt),
	}
}

// ToListItem projects a domain MCP onto the list-card wire shape.
func (m *MCP) ToListItem() ListItem {
	return ListItem{
		ID:               m.ID,
		Name:             m.Name,
		Slogan:           m.Slogan,
		Category:         m.Category,
		Icon:             m.Icon,
		Tags:             nonNilStrings(m.Tags),
		ToolCount:        len(m.Tools),
		Visibility:       m.Visibility,
		CreatorName:      m.CreatorName,
		CreatedByType:    normalizeCreatedByType(m.CreatedByType),
		CreatedByBotUID:  m.CreatedByBotUID,
		CreatedByBotName: m.CreatedByBotName,
		Transport:        m.Transport,
		UpdatedAt:        m.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// normalizeCreatedByType guarantees a non-empty value on the wire (doc §3.1:
// "always present"). Post-migration rows read back as human/bot/import
// verbatim from the ENUM column, so this only fires for zero-value struct
// literals — chiefly router/handler test stubs that return model.Detail{}
// without hydrating provenance. The service create path is authoritative
// for real writes; this normalise sits on the read/serialize side purely as
// a wire-contract guard.
func normalizeCreatedByType(t CreatedByType) CreatedByType {
	if t == "" {
		return CreatedByHuman
	}
	return t
}

// nonNilStrings guarantees a JSON array (not null) for always-present fields.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilTools(t []Tool) []Tool {
	if t == nil {
		return []Tool{}
	}
	return t
}

func nonNilFAQs(f []FAQ) []FAQ {
	if f == nil {
		return []FAQ{}
	}
	return f
}

// emptyToNilStrings returns nil for an empty slice so omitempty drops the key
// (doc §3.3: "Empty array collapses to omitted").
func emptyToNilStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func emptyToNilMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}
