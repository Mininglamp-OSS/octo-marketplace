package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// capture records what the fake fleet server received on the last request.
type capture struct {
	method      string
	path        string
	token       string
	workspaceID string
	spaceID     string
	body        map[string]any
}

func newFakeFleet(t *testing.T, status int, respBody string, cap *capture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.token = r.Header.Get("Token")
		cap.workspaceID = r.Header.Get("X-Workspace-Id")
		cap.spaceID = r.Header.Get("X-Space-Id")
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				cap.body = map[string]any{}
				_ = json.Unmarshal(raw, &cap.body)
			}
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
}

func TestCreateAgentForwardsCredentialsAndReturnsID(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusCreated, `{"id":"agent-1"}`, &cap)
	defer srv.Close()

	id, err := New(srv.URL).CreateAgent(context.Background(), "tok", "space-1", "ws-1", AgentSpec{
		Name:         "Helper",
		Description:  "desc",
		Instructions: "do things",
		RuntimeID:    "rt-1",
		MCPConfig:    json.RawMessage(`{"mcpServers":{}}`),
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if id != "agent-1" {
		t.Fatalf("id = %q, want agent-1", id)
	}
	if cap.method != http.MethodPost || cap.path != "/api/agents" {
		t.Fatalf("got %s %s, want POST /api/agents", cap.method, cap.path)
	}
	if cap.token != "tok" || cap.workspaceID != "ws-1" || cap.spaceID != "space-1" {
		t.Fatalf("headers token=%q ws=%q space=%q", cap.token, cap.workspaceID, cap.spaceID)
	}
	if cap.body["name"] != "Helper" || cap.body["runtime_id"] != "rt-1" || cap.body["instructions"] != "do things" {
		t.Fatalf("unexpected body: %#v", cap.body)
	}
	if _, ok := cap.body["mcp_config"]; !ok {
		t.Fatalf("mcp_config not forwarded: %#v", cap.body)
	}
}

func TestCreateAgentOmitsEmptyMCPConfig(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusCreated, `{"id":"agent-2"}`, &cap)
	defer srv.Close()

	if _, err := New(srv.URL).CreateAgent(context.Background(), "tok", "", "ws-1", AgentSpec{
		Name:      "NoMCP",
		RuntimeID: "rt-1",
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, ok := cap.body["mcp_config"]; ok {
		t.Fatalf("mcp_config should be omitted when empty: %#v", cap.body)
	}
	// Empty space id must not send the X-Space-Id header.
	if cap.spaceID != "" {
		t.Fatalf("X-Space-Id = %q, want empty", cap.spaceID)
	}
}

func TestCreateSkillReturnsID(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusCreated, `{"id":"skill-1"}`, &cap)
	defer srv.Close()

	id, err := New(srv.URL).CreateSkill(context.Background(), "tok", "space-1", "ws-1", SkillSpec{
		Name:    "Research",
		Content: "# SKILL",
	})
	if err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	if id != "skill-1" {
		t.Fatalf("id = %q, want skill-1", id)
	}
	if cap.path != "/api/skills" || cap.body["content"] != "# SKILL" {
		t.Fatalf("got path=%s body=%#v", cap.path, cap.body)
	}
}

func TestSetAgentSkillsSendsSkillIDs(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusOK, `{}`, &cap)
	defer srv.Close()

	if err := New(srv.URL).SetAgentSkills(context.Background(), "tok", "space-1", "ws-1", "agent-1", []string{"s1", "s2"}); err != nil {
		t.Fatalf("SetAgentSkills: %v", err)
	}
	if cap.method != http.MethodPut || cap.path != "/api/agents/agent-1/skills" {
		t.Fatalf("got %s %s", cap.method, cap.path)
	}
	ids, _ := cap.body["skill_ids"].([]any)
	if len(ids) != 2 || ids[0] != "s1" || ids[1] != "s2" {
		t.Fatalf("skill_ids = %#v", cap.body["skill_ids"])
	}
}

func TestUpsertSkillFilePutsPathAndContent(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusOK, `{}`, &cap)
	defer srv.Close()

	if err := New(srv.URL).UpsertSkillFile(context.Background(), "tok", "space-1", "ws-1", "skill-1", "reference.md", "# ref"); err != nil {
		t.Fatalf("UpsertSkillFile: %v", err)
	}
	if cap.method != http.MethodPut || cap.path != "/api/skills/skill-1/files" {
		t.Fatalf("got %s %s", cap.method, cap.path)
	}
	if cap.body["path"] != "reference.md" || cap.body["content"] != "# ref" {
		t.Fatalf("body = %#v", cap.body)
	}
}

func TestDeleteAgentAndSkillUseDelete(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusNoContent, ``, &cap)
	defer srv.Close()
	c := New(srv.URL)

	if err := c.DeleteAgent(context.Background(), "tok", "space-1", "ws-1", "agent-1"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if cap.method != http.MethodDelete || cap.path != "/api/agents/agent-1" {
		t.Fatalf("DeleteAgent got %s %s", cap.method, cap.path)
	}
	if err := c.DeleteSkill(context.Background(), "tok", "space-1", "ws-1", "skill-1"); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}
	if cap.path != "/api/skills/skill-1" {
		t.Fatalf("DeleteSkill path = %s", cap.path)
	}
}

func TestNon2xxReturnsAPIError(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusConflict, `{"error":"an agent named \"X\" already exists"}`, &cap)
	defer srv.Close()

	_, err := New(srv.URL).CreateAgent(context.Background(), "tok", "space-1", "ws-1", AgentSpec{Name: "X", RuntimeID: "rt-1"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", apiErr.Status)
	}
	if apiErr.Message != `an agent named "X" already exists` {
		t.Fatalf("message = %q", apiErr.Message)
	}
}

func TestMissingIDInResponseIsError(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusCreated, `{}`, &cap)
	defer srv.Close()

	if _, err := New(srv.URL).CreateAgent(context.Background(), "tok", "space-1", "ws-1", AgentSpec{Name: "X", RuntimeID: "rt-1"}); err == nil {
		t.Fatal("expected error when response has no id")
	}
}

func TestCreateSquadForwardsLeaderAndReturnsID(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusCreated, `{"id":"squad-1"}`, &cap)
	defer srv.Close()

	id, err := New(srv.URL).CreateSquad(context.Background(), "tok", "space-1", "ws-1", SquadSpec{
		Name:          "Delivery Squad",
		Description:   "ships features",
		LeaderAgentID: "agent-lead",
	})
	if err != nil {
		t.Fatalf("CreateSquad: %v", err)
	}
	if id != "squad-1" {
		t.Fatalf("id = %q, want squad-1", id)
	}
	if cap.method != http.MethodPost || cap.path != "/api/squads" {
		t.Fatalf("got %s %s, want POST /api/squads", cap.method, cap.path)
	}
	if cap.token != "tok" || cap.workspaceID != "ws-1" || cap.spaceID != "space-1" {
		t.Fatalf("headers token=%q ws=%q space=%q", cap.token, cap.workspaceID, cap.spaceID)
	}
	if cap.body["name"] != "Delivery Squad" || cap.body["leader_id"] != "agent-lead" || cap.body["description"] != "ships features" {
		t.Fatalf("unexpected body: %#v", cap.body)
	}
}

func TestAddSquadMemberPostsMemberFields(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusCreated, `{}`, &cap)
	defer srv.Close()

	if err := New(srv.URL).AddSquadMember(context.Background(), "tok", "space-1", "ws-1", "squad-1", SquadMemberSpec{
		MemberType: "agent",
		MemberID:   "agent-2",
		Role:       "coder",
	}); err != nil {
		t.Fatalf("AddSquadMember: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/api/squads/squad-1/members" {
		t.Fatalf("got %s %s", cap.method, cap.path)
	}
	if cap.body["member_type"] != "agent" || cap.body["member_id"] != "agent-2" || cap.body["role"] != "coder" {
		t.Fatalf("body = %#v", cap.body)
	}
}

func TestAddSquadMemberConflictReturnsAPIError(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusConflict, `{"error":"member already in squad"}`, &cap)
	defer srv.Close()

	err := New(srv.URL).AddSquadMember(context.Background(), "tok", "space-1", "ws-1", "squad-1", SquadMemberSpec{
		MemberType: "agent", MemberID: "agent-2", Role: "coder",
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("err = %v, want *APIError 409", err)
	}
}

func TestUpdateSquadInstructionsPutsOnlyInstructions(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusOK, `{"id":"squad-1"}`, &cap)
	defer srv.Close()

	if err := New(srv.URL).UpdateSquadInstructions(context.Background(), "tok", "space-1", "ws-1", "squad-1", "1. 先分析\n2. 再分派"); err != nil {
		t.Fatalf("UpdateSquadInstructions: %v", err)
	}
	if cap.method != http.MethodPut || cap.path != "/api/squads/squad-1" {
		t.Fatalf("got %s %s, want PUT /api/squads/squad-1", cap.method, cap.path)
	}
	if cap.body["instructions"] != "1. 先分析\n2. 再分派" {
		t.Fatalf("body = %#v", cap.body)
	}
	// Fleet's update treats absent fields as unchanged — the body must not carry
	// name/description/leader_id, or the update would blank them.
	for _, k := range []string{"name", "description", "leader_id"} {
		if _, ok := cap.body[k]; ok {
			t.Fatalf("body must not include %q: %#v", k, cap.body)
		}
	}
}

func TestDeleteSquadUsesDelete(t *testing.T) {
	var cap capture
	srv := newFakeFleet(t, http.StatusNoContent, ``, &cap)
	defer srv.Close()

	if err := New(srv.URL).DeleteSquad(context.Background(), "tok", "space-1", "ws-1", "squad-1"); err != nil {
		t.Fatalf("DeleteSquad: %v", err)
	}
	if cap.method != http.MethodDelete || cap.path != "/api/squads/squad-1" {
		t.Fatalf("got %s %s, want DELETE /api/squads/squad-1", cap.method, cap.path)
	}
}

// The client must NOT follow redirects: it forwards the end user's raw Token in
// a custom header, and Go copies custom headers verbatim across a cross-origin
// redirect. A 3xx is surfaced as an *APIError and the credential never reaches
// the redirect target.
func TestClientDoesNotFollowRedirects(t *testing.T) {
	var leakedToken string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakedToken = r.Header.Get("Token")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"leaked"}`))
	}))
	defer sink.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+"/api/agents", http.StatusFound)
	}))
	defer origin.Close()

	_, err := New(origin.URL).CreateAgent(context.Background(), "USER-SECRET", "space-1", "ws-1", AgentSpec{Name: "a"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("err = %v, want *APIError 302 (redirect not followed)", err)
	}
	if leakedToken != "" {
		t.Fatalf("token leaked to redirect target: %q", leakedToken)
	}
}

// parseFleetError caps the message without splitting a multi-byte rune (fleet
// returns Chinese error text).
func TestParseFleetErrorTruncatesOnRuneBoundary(t *testing.T) {
	// 100 Chinese runes (3 bytes each = 300 bytes) exceeds the 200-byte cap.
	long := ""
	for i := 0; i < 100; i++ {
		long += "错"
	}
	body, _ := json.Marshal(map[string]string{"error": long})
	got := parseFleetError(body)
	if len(got) > 200 {
		t.Fatalf("message not capped: %d bytes", len(got))
	}
	if !utf8ValidString(got) {
		t.Fatalf("message split a rune (invalid UTF-8): %q", got)
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
