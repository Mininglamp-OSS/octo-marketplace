package expert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// expertReceiptFleet models Fleet's persisted capability receipt for the direct
// expert/squad routes. A seeded receipt represents an operation committed
// before the connector-default change whose response was lost.
type expertReceiptFleet struct {
	t *testing.T

	mu            sync.Mutex
	receipts      map[string][]byte
	results       map[string][]byte
	installBodies [][]byte
	replayBodies  [][]byte
	gets          int
	commits       int
	installReply  int
	hideOnce      bool
	replayMissing bool
}

func newExpertReceiptFleet(t *testing.T) *expertReceiptFleet {
	t.Helper()
	return &expertReceiptFleet{t: t, receipts: map[string][]byte{}, results: map[string][]byte{}}
}

func (f *expertReceiptFleet) seed(key string, request, result []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.receipts[key] = append([]byte(nil), request...)
	f.results[key] = append([]byte(nil), result...)
}

func (f *expertReceiptFleet) hideNextPresence() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hideOnce = true
}

func (f *expertReceiptFleet) setReplayMissing(missing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replayMissing = missing
}

func (f *expertReceiptFleet) setInstallReply(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installReply = status
}

func (f *expertReceiptFleet) counts() (gets, installs, replays, commits int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets, len(f.installBodies), len(f.replayBodies), f.commits
}

func (f *expertReceiptFleet) replayBody(t *testing.T, index int) []byte {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if index >= len(f.replayBodies) {
		t.Fatalf("receipt replay %d missing (have %d)", index+1, len(f.replayBodies))
	}
	return append([]byte(nil), f.replayBodies[index]...)
}

func (f *expertReceiptFleet) writeJSON(w http.ResponseWriter, status int, value any, replayed bool) {
	w.Header().Set("Content-Type", "application/json")
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.WriteHeader(status)
	raw, err := json.Marshal(value)
	if err != nil {
		f.t.Errorf("encode fixture response: %v", err)
		return
	}
	_, _ = w.Write(raw)
}

func (f *expertReceiptFleet) writeData(w http.ResponseWriter, status int, raw []byte, replayed bool) {
	w.Header().Set("Content-Type", "application/json")
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"data":`)
	_, _ = w.Write(raw)
	_, _ = io.WriteString(w, `}`)
}

func (f *expertReceiptFleet) writeError(w http.ResponseWriter, status int, code, resource string) {
	envelope := map[string]any{"error": map[string]any{"code": code}}
	if resource != "" {
		envelope["error"].(map[string]any)["details"] = map[string]any{"resource": resource}
	}
	f.writeJSON(w, status, envelope, false)
}

func legacyMCPHasExtensions(body []byte) bool {
	var request struct {
		Definition struct {
			Connectors []json.RawMessage `json:"connectors"`
			Experts    []struct {
				MCPConfig json.RawMessage `json:"mcp_config"`
			} `json:"experts"`
		} `json:"definition"`
	}
	if json.Unmarshal(body, &request) != nil || len(request.Definition.Connectors) != 0 {
		return false
	}
	for _, expert := range request.Definition.Experts {
		if len(expert.MCPConfig) == 0 {
			continue
		}
		var config map[string]json.RawMessage
		if json.Unmarshal(expert.MCPConfig, &config) != nil {
			continue
		}
		for name := range config {
			if name != "mcpServers" {
				return true
			}
		}
		var servers map[string]json.RawMessage
		if json.Unmarshal(config["mcpServers"], &servers) != nil {
			continue
		}
		for _, rawServer := range servers {
			var server map[string]json.RawMessage
			if json.Unmarshal(rawServer, &server) != nil {
				continue
			}
			for name := range server {
				switch name {
				case "type", "command", "args", "env", "url", "headers":
				default:
					return true
				}
			}
		}
	}
	return false
}
func (f *expertReceiptFleet) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities/install/receipt":
		f.mu.Lock()
		f.gets++
		hide := f.hideOnce
		f.hideOnce = false
		_, exists := f.receipts[key]
		f.mu.Unlock()
		f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"has_receipt": exists && !hide}}, false)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/capabilities/install":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			f.t.Errorf("read install body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.installBodies = append(f.installBodies, append([]byte(nil), body...))
		previous, exists := f.receipts[key]
		if exists {
			if !bytes.Equal(previous, body) {
				f.mu.Unlock()
				f.writeError(w, http.StatusConflict, "DUPLICATE", "idempotency_key")
				return
			}
			result := append([]byte(nil), f.results[key]...)
			f.mu.Unlock()
			f.writeData(w, http.StatusOK, result, true)
			return
		}
		f.commits++
		result := expertReceiptResult(body, fmt.Sprintf("agent-fresh-%d", f.commits))
		reply := f.installReply
		if reply == 0 {
			reply = http.StatusServiceUnavailable
		}
		if result == nil {
			f.mu.Unlock()
			f.writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "")
			return
		}
		f.receipts[key] = append([]byte(nil), body...)
		f.results[key] = result
		f.mu.Unlock()
		if reply == http.StatusOK {
			f.writeData(w, http.StatusOK, result, false)
			return
		}
		f.writeError(w, reply, "UPSTREAM_UNAVAILABLE", "")
	case r.Method == http.MethodPost && r.URL.Path == "/v1/capabilities/install/receipt":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			f.t.Errorf("read receipt body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.replayBodies = append(f.replayBodies, append([]byte(nil), body...))
		missing := f.replayMissing
		previous, exists := f.receipts[key]
		var result []byte
		if exists {
			result = append([]byte(nil), f.results[key]...)
		}
		f.mu.Unlock()
		if legacyMCPHasExtensions(body) {
			f.writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "")
			return
		}
		if missing || !exists {
			f.writeError(w, http.StatusNotFound, "NOT_FOUND", "capability_installation_receipt")
			return
		}
		if !bytes.Equal(previous, body) {
			f.writeError(w, http.StatusConflict, "DUPLICATE", "idempotency_key")
			return
		}
		f.writeData(w, http.StatusOK, result, true)
	default:
		f.t.Errorf("unexpected Fleet request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func expertReceiptResult(request []byte, expertID string) []byte {
	var in fleet.CapabilityInstallRequest
	if json.Unmarshal(request, &in) != nil || len(in.Definition.Experts) != 1 || in.Definition.ExpertTeam != nil {
		return nil
	}
	root := in.Definition.Experts[0]
	expert := map[string]any{
		"expert_id": expertID, "name": root.Name, "version": root.Version,
		"market_expert_id": root.MarketExpertID, "created": true, "restored": false,
		"skill_ids": []string{},
	}
	result := map[string]any{"type": "expert", "expert_id": expertID, "experts": []any{expert}, "skills": []any{}}
	if len(in.Definition.Connectors) > 0 {
		byName := make(map[string]string, len(in.Definition.Connectors))
		connectors := make([]any, 0, len(in.Definition.Connectors))
		for i, connector := range in.Definition.Connectors {
			id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
			byName[connector.Name] = id
			var marketMCPID any
			if connector.MarketMCPID != "" {
				marketMCPID = connector.MarketMCPID
			}
			connectors = append(connectors, map[string]any{
				"connector_id": id, "name": connector.Name, "version": connector.Version,
				"market_mcp_id": marketMCPID, "created": true,
			})
		}
		result["connectors"] = connectors
		ids := make([]string, 0, len(root.ConnectorNames))
		for _, name := range root.ConnectorNames {
			ids = append(ids, byName[name])
		}
		expert["connector_ids"] = ids
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	return raw
}

const expertLegacyMCPConfig = `{"mcpServers":{"tools":{"command":"run"}}}`

// legacyExpertReceiptWire freezes the pre-connector-default Fleet wire for the
// installFixture expert so recovery must reproduce it byte-for-byte rather than
// merely reusing the idempotency key.
func legacyExpertReceiptWire(t *testing.T, config string) []byte {
	t.Helper()
	request := fleet.CapabilityInstallRequest{
		Definition: fleet.CapabilityDefinition{
			SchemaVersion: "1.0", Name: "marketplace:exp-1",
			Experts: []fleet.CapabilityExpert{{
				Name: "Code Helper", Version: nil, MarketExpertID: "exp-1",
				Description: "helps with code", Instructions: "You are a coding expert.",
				MCPConfig: json.RawMessage(config),
			}},
		},
		Bindings: fleet.CapabilityBindings{Experts: []fleet.CapabilityExpertBinding{{
			ExpertName: "Code Helper", RuntimeID: "rt-1",
		}}},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

const expertLegacyReceiptResult = `{"type":"expert","expert_id":"agent-legacy","experts":[{"expert_id":"agent-legacy","name":"Code Helper","version":null,"market_expert_id":"exp-1","created":true,"restored":false,"skill_ids":[]}],"skills":[]}`

func TestInstallExpertRecoversPreUpgradeLegacyReceipt(t *testing.T) {
	upstream := newExpertReceiptFleet(t)
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	svc, caller, id := installFixture(t, fleet.New(server.URL), nil)
	svc.repo.(*fakeStore).experts[id].MCPConfig = expertLegacyMCPConfig

	const key = "marketplace-expert-legacy-receipt"
	legacy := legacyExpertReceiptWire(t, expertLegacyMCPConfig)
	upstream.seed(key, legacy, []byte(expertLegacyReceiptResult))

	input := baseInput()
	input.IdempotencyKey = key
	result, err := svc.InstallExpert(context.Background(), caller, id, input)
	if err != nil {
		t.Fatalf("legacy receipt recovery: %v", err)
	}
	if result.AgentID != "agent-legacy" {
		t.Fatalf("recovered agent=%q, want the original receipt resource", result.AgentID)
	}
	gets, installs, replays, commits := upstream.counts()
	if gets != 1 || installs != 0 || replays != 2 || commits != 0 {
		t.Fatalf("recovery counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
	if body := upstream.replayBody(t, 1); !bytes.Equal(body, legacy) {
		t.Fatalf("recovery did not replay the frozen legacy wire: %s", body)
	}

	// Changed input under the same key must conflict, not write a new operation.
	input.CustomEnv = map[string]string{"OCTOBUDDY_PROVIDER_ID": "changed"}
	if _, err := svc.InstallExpert(context.Background(), caller, id, input); err == nil {
		t.Fatal("changed environment recovered a mismatched receipt")
	} else {
		var apiErr *fleet.CapabilityAPIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
			t.Fatalf("changed environment error=%v, want a 409 receipt mismatch", err)
		}
	}
	input.CustomEnv = nil
	input.RuntimeID = "rt-2"
	if _, err := svc.InstallExpert(context.Background(), caller, id, input); err == nil {
		t.Fatal("changed runtime recovered a mismatched receipt")
	} else {
		var apiErr *fleet.CapabilityAPIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
			t.Fatalf("changed runtime error=%v, want a 409 receipt mismatch", err)
		}
	}
	gets, installs, replays, commits = upstream.counts()
	if gets != 3 || installs != 0 || replays != 6 || commits != 0 {
		t.Fatalf("conflict counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
}

func TestInstallExpertReplaysCurrentReceiptBeforeLegacyMCPRejectsExtensions(t *testing.T) {
	const config = `{"mcpServers":{"tools":{"command":"run","providerOptions":{"region":"test"}}},"vendor":{"feature":true}}`
	upstream := newExpertReceiptFleet(t)
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	svc, caller, id := installFixture(t, fleet.New(server.URL), nil)
	svc.repo.(*fakeStore).experts[id].MCPConfig = config
	upstream.setInstallReply(http.StatusOK)

	input := baseInput()
	input.IdempotencyKey = "marketplace-expert-current-connector-receipt"
	first, err := svc.InstallExpert(context.Background(), caller, id, input)
	if err != nil || first.AgentID != "agent-fresh-1" {
		t.Fatalf("initial connector install=%+v err=%v", first, err)
	}
	replayed, err := svc.InstallExpert(context.Background(), caller, id, input)
	if err != nil || replayed.AgentID != first.AgentID {
		t.Fatalf("current connector receipt replay=%+v err=%v", replayed, err)
	}
	gets, installs, replays, commits := upstream.counts()
	if gets != 2 || installs != 1 || replays != 1 || commits != 1 {
		t.Fatalf("current receipt counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
	var recoveredWire fleet.CapabilityInstallRequest
	if err := json.Unmarshal(upstream.replayBody(t, 0), &recoveredWire); err != nil {
		t.Fatal(err)
	}
	if len(recoveredWire.Definition.Connectors) != 1 || len(recoveredWire.Definition.Experts) != 1 ||
		len(recoveredWire.Definition.Experts[0].MCPConfig) != 0 {
		t.Fatalf("replay did not try the current connector wire first: %+v", recoveredWire.Definition)
	}
	if !bytes.Equal(recoveredWire.Definition.Connectors[0].Config, []byte(config)) {
		t.Fatalf("current receipt replay changed the complete MCP connector config: %s", recoveredWire.Definition.Connectors[0].Config)
	}
}

func TestInstallExpertPresenceRaceRecoversReadOnly(t *testing.T) {
	upstream := newExpertReceiptFleet(t)
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	svc, caller, id := installFixture(t, fleet.New(server.URL), nil)
	svc.repo.(*fakeStore).experts[id].MCPConfig = expertLegacyMCPConfig

	const key = "marketplace-expert-presence-race"
	legacy := legacyExpertReceiptWire(t, expertLegacyMCPConfig)
	upstream.seed(key, legacy, []byte(expertLegacyReceiptResult))
	upstream.hideNextPresence()

	input := baseInput()
	input.IdempotencyKey = key
	result, err := svc.InstallExpert(context.Background(), caller, id, input)
	if err != nil {
		t.Fatalf("presence-race recovery: %v", err)
	}
	if result.AgentID != "agent-legacy" {
		t.Fatalf("presence-race recovered agent=%q", result.AgentID)
	}
	gets, installs, replays, commits := upstream.counts()
	if gets != 1 || installs != 1 || replays != 1 || commits != 0 {
		t.Fatalf("presence-race counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
	if body := upstream.replayBody(t, 0); !bytes.Equal(body, legacy) {
		t.Fatalf("presence-race did not replay the frozen wire: %s", body)
	}
}

func TestInstallExpertReceiptReadFailureDoesNotWrite(t *testing.T) {
	upstream := newExpertReceiptFleet(t)
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	svc, caller, id := installFixture(t, fleet.New(server.URL), nil)
	svc.repo.(*fakeStore).experts[id].MCPConfig = expertLegacyMCPConfig

	const key = "marketplace-expert-receipt-vanished"
	legacy := legacyExpertReceiptWire(t, expertLegacyMCPConfig)
	upstream.seed(key, legacy, []byte(expertLegacyReceiptResult))
	upstream.setReplayMissing(true)

	input := baseInput()
	input.IdempotencyKey = key
	if _, err := svc.InstallExpert(context.Background(), caller, id, input); err == nil {
		t.Fatal("vanished receipt produced a success")
	}
	gets, installs, replays, commits := upstream.counts()
	if gets != 1 || installs != 0 || replays != 1 || commits != 0 {
		t.Fatalf("vanished receipt wrote: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
}

func TestInstallExpertKeylessSkipsReceiptPresenceProbe(t *testing.T) {
	upstream := newExpertReceiptFleet(t)
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	svc, caller, id := installFixture(t, fleet.New(server.URL), nil)
	svc.repo.(*fakeStore).experts[id].MCPConfig = expertLegacyMCPConfig
	svc.idGen = func() string { return "generated-expert-operation" }
	upstream.setInstallReply(http.StatusOK)

	result, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if err != nil {
		t.Fatalf("keyless install: %v", err)
	}
	if result.AgentID != "agent-fresh-1" {
		t.Fatalf("keyless install agent=%q", result.AgentID)
	}
	gets, installs, replays, commits := upstream.counts()
	if gets != 0 || installs != 1 || replays != 0 || commits != 1 {
		t.Fatalf("keyless counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
}

func legacySquadInstallRequest(t *testing.T, members []model.SquadMember) []byte {
	t.Helper()
	experts := make([]fleet.CapabilityExpert, len(members))
	bindings := fleet.CapabilityBindings{Experts: make([]fleet.CapabilityExpertBinding, 0, len(members))}
	teamMembers := make([]fleet.CapabilityTeamMember, len(members))
	leader := -1
	for i := range members {
		if members[i].IsLeader && leader == -1 {
			leader = i
		}
	}
	if leader == -1 {
		leader = 0
	}
	for i, member := range members {
		var mcpConfig json.RawMessage
		if member.MCPConfig != "" {
			mcpConfig = json.RawMessage(member.MCPConfig)
		}
		experts[i] = fleet.CapabilityExpert{
			Name: member.Name, Version: nil, Description: "ships features",
			Instructions: member.Instruction, MCPConfig: mcpConfig,
		}
		bindings.Experts = append(bindings.Experts, fleet.CapabilityExpertBinding{
			ExpertName: member.Name, RuntimeID: "rt-1",
		})
		role, isLeader := member.Role, i == leader
		if isLeader {
			role = "leader"
		} else if role == "" {
			role = "member"
		}
		teamMembers[i] = fleet.CapabilityTeamMember{ExpertName: member.Name, IsLeader: isLeader, Role: role}
	}
	request := fleet.CapabilityInstallRequest{
		Definition: fleet.CapabilityDefinition{
			SchemaVersion: "1.0", Name: "marketplace:sq-1", Experts: experts,
			ExpertTeam: &fleet.CapabilityExpertTeam{
				Name: "Delivery Squad", Version: nil, MarketExpertTeamID: "sq-1",
				Members: teamMembers, Description: "ships features", Instructions: "",
			},
		},
		Bindings: bindings,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

const squadLegacyReceiptResult = `{"type":"expert_team","expert_team_id":"team-legacy","leader_expert_id":"leader-legacy","expert_team":{"expert_team_id":"team-legacy","name":"Delivery Squad","version":null,"market_expert_team_id":"sq-1","created":true,"restored":false},"experts":[{"expert_id":"leader-legacy","name":"Solo","version":null,"created":true,"restored":false,"skill_ids":[],"is_leader":true}],"skills":[]}`

func TestInstallSquadRecoversPreUpgradeLegacyReceipt(t *testing.T) {
	upstream := newExpertReceiptFleet(t)
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	members := []model.SquadMember{{
		MemberKey: "member_01", Name: "Solo", Instruction: "do work", MCPConfig: expertLegacyMCPConfig,
	}}
	svc, caller, id := installSquadFixture(t, fleet.New(server.URL), members)

	const key = "marketplace-squad-legacy-receipt"
	legacy := legacySquadInstallRequest(t, members)
	upstream.seed(key, legacy, []byte(squadLegacyReceiptResult))

	input := baseInput()
	input.IdempotencyKey = key
	result, err := svc.InstallSquad(context.Background(), caller, id, input)
	if err != nil {
		t.Fatalf("legacy squad receipt recovery: %v", err)
	}
	if result.SquadID != "team-legacy" || result.LeaderAgentID != "leader-legacy" {
		t.Fatalf("recovered squad=%+v, want the original receipt resources", result)
	}
	gets, installs, replays, commits := upstream.counts()
	if gets != 1 || installs != 0 || replays != 2 || commits != 0 {
		t.Fatalf("squad recovery counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
	if body := upstream.replayBody(t, 1); !bytes.Equal(body, legacy) {
		t.Fatalf("squad recovery did not replay the frozen legacy wire: %s", body)
	}
}

func TestInstallSquadRecoversEveryMemberLegacyMCPWire(t *testing.T) {
	members := []model.SquadMember{
		{
			MemberKey: "member_01", Name: "Planner", Role: "planner", IsLeader: true,
			Instruction: "plan tasks", MCPConfig: `{"mcpServers":{"planner":{"command":"run-planner"}}}`,
		},
		{
			MemberKey: "member_02", Name: "Coder", Role: "coder",
			Instruction: "write code", MCPConfig: `{"mcpServers":{"coder":{"command":"run-coder"}}}`,
		},
	}
	upstream := newExpertReceiptFleet(t)
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	svc, caller, id := installSquadFixture(t, fleet.New(server.URL), members)

	const key = "marketplace-squad-multi-member-legacy-receipt"
	legacy := legacySquadInstallRequest(t, members)
	const result = `{"type":"expert_team","expert_team_id":"team-legacy-multi","leader_expert_id":"leader-legacy-multi","expert_team":{"expert_team_id":"team-legacy-multi","name":"Delivery Squad","version":null,"market_expert_team_id":"sq-1","created":true,"restored":false},"experts":[{"expert_id":"leader-legacy-multi","name":"Planner","version":null,"created":true,"restored":false,"skill_ids":[],"is_leader":true},{"expert_id":"coder-legacy","name":"Coder","version":null,"created":true,"restored":false,"skill_ids":[]}],"skills":[]}`
	upstream.seed(key, legacy, []byte(result))

	input := baseInput()
	input.IdempotencyKey = key
	recovered, err := svc.InstallSquad(context.Background(), caller, id, input)
	if err != nil {
		t.Fatalf("multi-member legacy receipt recovery: %v", err)
	}
	if recovered.SquadID != "team-legacy-multi" || recovered.LeaderAgentID != "leader-legacy-multi" {
		t.Fatalf("recovered squad=%+v, want original resources", recovered)
	}
	gets, installs, replays, commits := upstream.counts()
	if gets != 1 || installs != 0 || replays != 2 || commits != 0 {
		t.Fatalf("multi-member recovery counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
	if body := upstream.replayBody(t, 1); !bytes.Equal(body, legacy) {
		t.Fatalf("recovery omitted or changed a member's legacy MCP wire: %s", body)
	}
}
