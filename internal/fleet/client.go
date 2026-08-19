// Package fleet is a thin server-to-server client for the octo-fleet (Loop)
// service. The Expert Marketplace install flow uses it to provision an agent
// (and its skills) in a Loop workspace/runtime on behalf of the calling user.
//
// Auth model: fleet rejects bot/service credentials on /api/agents and
// /api/skills, so every call forwards the END USER's own octo `Token` verbatim
// plus the workspace selector `X-Workspace-Id` (and optional `X-Space-Id`).
// This mirrors exactly what octo-web sends. The client is deliberately plain
// (base URL + a timed http.Client), like internal/auth/resolver.go, but it does
// NOT follow redirects: because it forwards a raw user credential in a custom
// header, chasing a 3xx to another host would leak that credential (see New).
package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"
)

// maxRespBytes bounds a fleet response body so a misbehaving upstream can't
// balloon memory. Agent/skill responses are small JSON objects.
const maxRespBytes = 1 << 20

// Client talks to one octo-fleet base URL.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client for the given fleet base URL (no trailing slash).
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Never follow redirects. We forward the end user's raw octo Token as
			// a custom header, and Go only strips the well-known auth headers
			// (Authorization/Cookie/…) on a cross-origin redirect — a custom
			// "Token"/"X-Workspace-Id"/"X-Space-Id" would be copied verbatim to
			// whatever host a 3xx names, leaking a live credential (and turning an
			// install into an SSRF primitive). A server-to-server JSON API has no
			// legitimate reason to redirect, so surface the 3xx as the response
			// instead of chasing it.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// AgentSpec is the subset of fleet's CreateAgentRequest the install flow sets.
type AgentSpec struct {
	Name         string
	Description  string
	Instructions string
	RuntimeID    string
	// MCPConfig is the expert's verbatim mcp_config JSON; omitted when empty.
	MCPConfig json.RawMessage
}

// SkillSpec is the subset of fleet's CreateSkillRequest the install flow sets:
// a workspace skill seeded from the expert's SKILL.md content.
type SkillSpec struct {
	Name        string
	Description string
	Content     string
}

// APIError is a non-2xx fleet response. Status lets the caller distinguish a
// client-fault 4xx (surface it) from a 5xx/transport failure (treat as
// upstream-unavailable). Message is fleet's `{"error": "..."}` text.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("fleet: status %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("fleet: status %d", e.Status)
}

// CreateAgent creates an agent in the caller's workspace/runtime and returns
// its id (fleet AgentResponse.id).
func (c *Client) CreateAgent(ctx context.Context, token, spaceID, workspaceID string, spec AgentSpec) (string, error) {
	body := map[string]any{
		"name":         spec.Name,
		"description":  spec.Description,
		"instructions": spec.Instructions,
		"runtime_id":   spec.RuntimeID,
	}
	if len(spec.MCPConfig) > 0 {
		body["mcp_config"] = spec.MCPConfig
	}
	return c.doCreate(ctx, http.MethodPost, "/api/agents", token, spaceID, workspaceID, body)
}

// CreateSkill creates a workspace skill from SKILL.md content and returns its
// id (fleet SkillWithFilesResponse.id).
func (c *Client) CreateSkill(ctx context.Context, token, spaceID, workspaceID string, spec SkillSpec) (string, error) {
	body := map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"content":     spec.Content,
	}
	return c.doCreate(ctx, http.MethodPost, "/api/skills", token, spaceID, workspaceID, body)
}

// SetAgentSkills replaces the agent's bound skill set.
func (c *Client) SetAgentSkills(ctx context.Context, token, spaceID, workspaceID, agentID string, skillIDs []string) error {
	body := map[string]any{"skill_ids": skillIDs}
	_, err := c.do(ctx, http.MethodPut, "/api/agents/"+agentID+"/skills", token, spaceID, workspaceID, body)
	return err
}

// UpsertSkillFile adds/updates one supporting file on a skill
// (PUT /api/skills/{id}/files). Used to attach a packaged skill's non-SKILL.md
// files after the skill is created.
func (c *Client) UpsertSkillFile(ctx context.Context, token, spaceID, workspaceID, skillID, path, content string) error {
	body := map[string]any{"path": path, "content": content}
	_, err := c.do(ctx, http.MethodPut, "/api/skills/"+skillID+"/files", token, spaceID, workspaceID, body)
	return err
}

// DeleteAgent removes an agent (rollback path; best-effort at the call site).
func (c *Client) DeleteAgent(ctx context.Context, token, spaceID, workspaceID, agentID string) error {
	_, err := c.do(ctx, http.MethodDelete, "/api/agents/"+agentID, token, spaceID, workspaceID, nil)
	return err
}

// DeleteSkill removes a skill (rollback path; best-effort at the call site).
func (c *Client) DeleteSkill(ctx context.Context, token, spaceID, workspaceID, skillID string) error {
	_, err := c.do(ctx, http.MethodDelete, "/api/skills/"+skillID, token, spaceID, workspaceID, nil)
	return err
}

// SquadSpec is the subset of fleet's CreateSquad request the squad-install flow
// sets: a squad seeded from the marketplace squad's name/summary, led by an
// already-created member agent. Fleet auto-adds the leader as a member.
type SquadSpec struct {
	Name          string
	Description   string
	LeaderAgentID string
}

// SquadMemberSpec is the subset of fleet's AddSquadMember request the flow sets:
// a non-leader member agent and its team role.
type SquadMemberSpec struct {
	MemberType string
	MemberID   string
	Role       string
}

// CreateSquad creates a squad led by the given agent and returns its id (fleet
// SquadResponse.id). Fleet requires the caller be a workspace owner/admin and
// the leader to be an existing agent in the workspace; it auto-adds the leader
// as a member with role "leader".
func (c *Client) CreateSquad(ctx context.Context, token, spaceID, workspaceID string, spec SquadSpec) (string, error) {
	body := map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"leader_id":   spec.LeaderAgentID,
	}
	return c.doCreate(ctx, http.MethodPost, "/api/squads", token, spaceID, workspaceID, body)
}

// AddSquadMember adds one member to a squad (POST /api/squads/{id}/members).
// The leader is already a member from CreateSquad, so only non-leader members
// go through here. A duplicate member returns a fleet 409 (*APIError).
func (c *Client) AddSquadMember(ctx context.Context, token, spaceID, workspaceID, squadID string, m SquadMemberSpec) error {
	body := map[string]any{
		"member_type": m.MemberType,
		"member_id":   m.MemberID,
		"role":        m.Role,
	}
	_, err := c.do(ctx, http.MethodPost, "/api/squads/"+squadID+"/members", token, spaceID, workspaceID, body)
	return err
}

// UpdateSquadInstructions sets a squad's instructions (PUT /api/squads/{id}).
// Fleet's create endpoint does not accept instructions, so the squad-install
// flow writes them in a follow-up update right after CreateSquad. Fleet's
// update handler treats absent fields as "leave unchanged", so sending only
// instructions never clobbers the name/description/leader set at create.
func (c *Client) UpdateSquadInstructions(ctx context.Context, token, spaceID, workspaceID, squadID, instructions string) error {
	body := map[string]any{"instructions": instructions}
	_, err := c.do(ctx, http.MethodPut, "/api/squads/"+squadID, token, spaceID, workspaceID, body)
	return err
}

// DeleteSquad archives a squad (rollback path; best-effort at the call site).
func (c *Client) DeleteSquad(ctx context.Context, token, spaceID, workspaceID, squadID string) error {
	_, err := c.do(ctx, http.MethodDelete, "/api/squads/"+squadID, token, spaceID, workspaceID, nil)
	return err
}

// doCreate performs a create request and decodes the `{"id": "..."}` field
// fleet returns for agents and skills.
func (c *Client) doCreate(ctx context.Context, method, path, token, spaceID, workspaceID string, body any) (string, error) {
	raw, err := c.do(ctx, method, path, token, spaceID, workspaceID, body)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("fleet: decode %s response: %w", path, err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("fleet: %s response missing id", path)
	}
	return out.ID, nil
}

// do issues one request with the forwarded user credentials, returning the
// (bounded) response body on 2xx or an *APIError otherwise.
func (c *Client) do(ctx context.Context, method, path, token, spaceID, workspaceID string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("fleet: encode %s request: %w", path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("fleet: create %s request: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Forward the end user's octo credential + workspace selector. fleet blocks
	// bot/service tokens on these routes, so this MUST be the caller's token.
	req.Header.Set("Token", token)
	req.Header.Set("X-Workspace-Id", workspaceID)
	if spaceID != "" {
		req.Header.Set("X-Space-Id", spaceID)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fleet: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if err != nil {
		return nil, fmt.Errorf("fleet: read %s response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Status: resp.StatusCode, Message: parseFleetError(data)}
	}
	return data, nil
}

// parseFleetError extracts fleet's `{"error": "..."}` message, falling back to
// the raw (truncated) body when it isn't that shape.
func parseFleetError(data []byte) string {
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err == nil && env.Error != "" {
		return truncateRunes(env.Error, 200)
	}
	return truncateRunes(string(data), 200)
}

// truncateRunes caps s at maxBytes without splitting a multi-byte rune (fleet
// returns Chinese error text, so a naive s[:n] could leave a dangling U+FFFD).
func truncateRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Back up to the start of the rune that straddles the byte boundary.
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
