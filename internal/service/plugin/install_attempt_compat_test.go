package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

type historicalPinnedRequest struct {
	Definition historicalPinnedDefinition `json:"definition"`
	Bindings   fleet.CapabilityBindings   `json:"bindings"`
}

type historicalPinnedDefinition struct {
	SchemaVersion string                      `json:"schema_version"`
	Name          string                      `json:"name"`
	Skills        []historicalPinnedSkill     `json:"skills,omitempty"`
	Connectors    []fleet.CapabilityConnector `json:"connectors,omitempty"`
	Experts       []historicalPinnedExpert    `json:"experts"`
	ExpertTeam    *historicalPinnedExpertTeam `json:"expert_team,omitempty"`
}

type historicalPinnedSkill struct {
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Content     string                      `json:"content"`
	Config      map[string]any              `json:"config"`
	Files       []fleet.CapabilitySkillFile `json:"files,omitempty"`
}

type historicalPinnedExpert struct {
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Instructions   string            `json:"instructions"`
	SkillNames     []string          `json:"skill_names,omitempty"`
	ConnectorNames []string          `json:"connector_names,omitempty"`
	CustomEnv      map[string]string `json:"custom_env,omitempty"`
	MCPConfig      json.RawMessage   `json:"mcp_config,omitempty"`
}

type historicalPinnedExpertTeam struct {
	Name         string                       `json:"name"`
	Members      []fleet.CapabilityTeamMember `json:"members"`
	Description  string                       `json:"description"`
	Instructions string                       `json:"instructions"`
}

func TestPinnedInstallReplaysSignedHistoricalReceiptWithOldSkillWireShape(t *testing.T) {
	s, f, _ := fixturePinned()
	p := installationParams()
	p.WorkspaceConnectors = false
	attempt, historicalWire := makeHistoricalPinnedAttempt(t, s, p, "expert-a", "expert-v1")
	f.attempts = map[string]*pluginrepo.InstallAttempt{p.WorkspaceID + attempt.KeyDigest: attempt}

	var captureMu sync.Mutex
	var received [][]byte
	var receivedKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read retry body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		captureMu.Lock()
		received = append(received, append([]byte(nil), body...))
		receivedKeys = append(receivedKeys, r.Header.Get("Idempotency-Key"))
		captureMu.Unlock()
		var in fleet.CapabilityInstallRequest
		if err := json.Unmarshal(body, &in); err != nil {
			t.Errorf("decode retry body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(historicalInstallResponse(t, in))
	}))
	defer server.Close()
	s.WithCapabilityInstaller(fleet.New(server.URL))

	out, err := s.CreateInstallation(context.Background(), testCaller, "expert-a", p)
	if err != nil {
		t.Fatalf("signed historical receipt retry failed: %v", err)
	}
	captureMu.Lock()
	gotBodies := append([][]byte(nil), received...)
	gotKeys := append([]string(nil), receivedKeys...)
	captureMu.Unlock()
	wantAttemptID := digestInstall([]string{testCaller.SpaceID, testCaller.UID, p.WorkspaceID, attempt.KeyDigest})
	if len(gotBodies) != 1 || !bytes.Equal(gotBodies[0], historicalWire) || !reflect.DeepEqual(gotKeys, []string{wantAttemptID}) {
		t.Fatalf("retry did not send the historical body under the stable attempt key: calls=%d keys=%v", len(gotBodies), gotKeys)
	}
	if out == nil || out.AgentID != "historical-agent" || !out.Replayed {
		t.Fatalf("historical replay result was not returned: %#v", out)
	}
}

func TestPinnedWorkspaceConnectorModeConflictsWithHistoricalDefaultModeKey(t *testing.T) {
	s, f, _ := fixturePinned()
	rootKey := pluginrepo.InstallVersionKey{PluginID: "expert-a", VersionID: "expert-v1"}
	version := f.versions[rootKey]
	relations, err := model.DecodeSnapshotRelations(version.Relations)
	if err != nil {
		t.Fatal(err)
	}
	version.Relations, err = model.EncodeSnapshotRelations(relationsOfType(relations, "expert_skill"))
	if err != nil {
		t.Fatal(err)
	}
	p := installationParams()
	attempt, _ := makeHistoricalPinnedAttempt(t, s, p, "expert-a", "expert-v1")
	f.attempts = map[string]*pluginrepo.InstallAttempt{p.WorkspaceID + attempt.KeyDigest: attempt}

	var callsMu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callsMu.Lock()
		calls++
		callsMu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	s.WithCapabilityInstaller(fleet.New(server.URL))
	p.WorkspaceConnectors = true

	if _, err = s.CreateInstallation(context.Background(), testCaller, "expert-a", p); !errors.Is(err, ErrConflict) {
		t.Fatalf("new connector mode did not conflict with the historical default-mode key: %v", err)
	}
	callsMu.Lock()
	callCount := calls
	callsMu.Unlock()
	if callCount != 0 {
		t.Fatalf("conflicting historical key reached Fleet %d times", callCount)
	}
}

func makeHistoricalPinnedAttempt(t *testing.T, s *Service, p InstallationParams, pluginID, rootVersionID string) (*pluginrepo.InstallAttempt, []byte) {
	t.Helper()
	plan, err := s.resolvePinnedInstallation(context.Background(), testCaller, pluginID, rootVersionID, false)
	if err != nil {
		t.Fatalf("resolve historical request fixture: %v", err)
	}
	for i := range plan.Definition.Skills {
		plan.Definition.Skills[i].Version = nil
		plan.Definition.Skills[i].MarketSkillID = ""
	}
	request, err := pinnedPlanRequest(plan, p)
	if err != nil {
		t.Fatalf("build historical request fixture: %v", err)
	}
	oldDefinition := historicalPinnedDefinition{
		SchemaVersion: request.Definition.SchemaVersion,
		Name:          request.Definition.Name,
		Connectors:    request.Definition.Connectors,
	}
	for _, expert := range request.Definition.Experts {
		oldDefinition.Experts = append(oldDefinition.Experts, historicalPinnedExpert{
			Name: expert.Name, Description: expert.Description, Instructions: expert.Instructions,
			SkillNames: expert.SkillNames, ConnectorNames: expert.ConnectorNames,
			CustomEnv: expert.CustomEnv, MCPConfig: expert.MCPConfig,
		})
	}
	if team := request.Definition.ExpertTeam; team != nil {
		oldDefinition.ExpertTeam = &historicalPinnedExpertTeam{
			Name: team.Name, Members: team.Members,
			Description: team.Description, Instructions: team.Instructions,
		}
	}
	oldRequest := historicalPinnedRequest{Definition: oldDefinition, Bindings: request.Bindings}
	for _, skill := range request.Definition.Skills {
		oldRequest.Definition.Skills = append(oldRequest.Definition.Skills, historicalPinnedSkill{
			Name: skill.Name, Description: skill.Description, Content: skill.Content,
			Config: skill.Config, Files: skill.Files,
		})
	}
	wire, err := json.Marshal(oldRequest)
	if err != nil {
		t.Fatalf("marshal historical request: %v", err)
	}
	var decoded struct {
		Definition struct {
			Skills     []map[string]json.RawMessage `json:"skills"`
			Experts    []map[string]json.RawMessage `json:"experts"`
			ExpertTeam map[string]json.RawMessage   `json:"expert_team"`
		} `json:"definition"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil || len(decoded.Definition.Skills) != len(request.Definition.Skills) {
		t.Fatalf("historical payload is not a real install request: err=%v skills=%d", err, len(decoded.Definition.Skills))
	}
	for _, skill := range decoded.Definition.Skills {
		if _, exists := skill["version"]; exists {
			t.Fatal("historical fixture contains the later Skill version field")
		}
		if _, exists := skill["market_skill_id"]; exists {
			t.Fatal("historical fixture contains the later Skill source field")
		}
	}
	for _, expert := range decoded.Definition.Experts {
		if _, exists := expert["version"]; exists {
			t.Fatal("historical fixture contains the later Expert version field")
		}
		if _, exists := expert["market_expert_id"]; exists {
			t.Fatal("historical fixture contains the later Expert source field")
		}
	}
	if _, exists := decoded.Definition.ExpertTeam["version"]; exists {
		t.Fatal("historical fixture contains the later ExpertTeam version field")
	}
	if _, exists := decoded.Definition.ExpertTeam["market_expert_team_id"]; exists {
		t.Fatal("historical fixture contains the later ExpertTeam source field")
	}
	intent := digestInstall(struct {
		Root, Space, Workspace, Runtime, Name string
		WorkspaceConnectors                   bool `json:"workspace_connectors,omitempty"`
	}{pluginID, testCaller.SpaceID, p.WorkspaceID, p.RuntimeID, p.ResourceName, p.WorkspaceConnectors})
	inputFingerprint, err := s.installationHMAC(s.installHMACKeyID, "input", struct {
		Intent string
		Env    map[string]string
	}{intent, p.CustomEnv})
	if err != nil {
		t.Fatalf("sign historical input: %v", err)
	}
	requestFingerprint, err := s.installationHMAC(s.installHMACKeyID, "request", json.RawMessage(wire))
	if err != nil {
		t.Fatalf("sign historical request: %v", err)
	}
	key := digestInstall(p.IdempotencyKey)
	retryUntil := time.Now().UTC().Add(time.Hour)
	attempt := &pluginrepo.InstallAttempt{
		WorkspaceID: p.WorkspaceID, KeyDigest: key, PluginID: pluginID, RootVersionID: rootVersionID,
		AdapterVersion: SkillInstallAdapterVersion, InputFingerprint: inputFingerprint,
		RequestFingerprint: requestFingerprint, HMACKeyID: s.installHMACKeyID, IntentDigest: intent,
		AgentID: "historical-agent", Completed: true, RetryUntil: &retryUntil, CreatedAt: time.Now().UTC(),
	}
	plan.EligibilityHMAC, err = s.installationEligibilityHMAC(testCaller, *attempt, plan)
	if err != nil {
		t.Fatalf("sign historical eligibility: %v", err)
	}
	attempt.FrozenPlan, err = json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal historical frozen plan: %v", err)
	}
	return attempt, wire
}

func historicalInstallResponse(t *testing.T, in fleet.CapabilityInstallRequest) []byte {
	t.Helper()
	var skills []fleet.CapabilitySkillResult
	skillIDs := make(map[string]string, len(in.Definition.Skills))
	for i, skill := range in.Definition.Skills {
		id := "historical-skill-" + string(rune('a'+i))
		skills = append(skills, fleet.CapabilitySkillResult{SkillID: id, Name: skill.Name})
		skillIDs[skill.Name] = id
	}
	experts := make([]fleet.CapabilityExpertResult, 0, len(in.Definition.Experts))
	for _, expert := range in.Definition.Experts {
		var boundSkills []string
		for _, name := range expert.SkillNames {
			boundSkills = append(boundSkills, skillIDs[name])
		}
		experts = append(experts, fleet.CapabilityExpertResult{ExpertID: "historical-agent", Name: expert.Name, SkillIDs: boundSkills})
	}
	result := fleet.CapabilityInstallResult{Type: "expert", ExpertID: "historical-agent", Experts: experts, Skills: skills}
	payload, err := json.Marshal(map[string]any{"data": result})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
func TestPinnedPlanRequestCanonicalizesOpaqueJSONAfterFrozenPlanJSONReorder(t *testing.T) {
	const (
		mcpBefore       = `{"extensions":{"z":2,"large":9007199254740993,"a":1},"mcpServers":{"server":{"z":3,"large":9007199254740995,"a":4}}}`
		mcpAfter        = `{"mcpServers":{"server":{"a":4,"large":9007199254740995,"z":3}},"extensions":{"a":1,"large":9007199254740993,"z":2}}`
		connectorBefore = `{"mcpServers":{"server":{"z":6,"large":9007199254740997,"a":5}},"extensions":{"z":8,"large":9007199254740999,"a":7}}`
		connectorAfter  = `{"extensions":{"a":7,"large":9007199254740999,"z":8},"mcpServers":{"server":{"a":5,"large":9007199254740997,"z":6}}}`
	)
	p := InstallationParams{RuntimeID: "550e8400-e29b-41d4-a716-446655440000", WorkspaceConnectors: true}
	makePlan := func(mcpConfig, connectorConfig string) pinnedInstallationPlan {
		return pinnedInstallationPlan{Definition: fleet.CapabilityDefinition{
			SchemaVersion: "1.0",
			Name:          "marketplace:root",
			Connectors: []fleet.CapabilityConnector{{
				Name: "Shared", MarketMCPID: "connector-1", Config: json.RawMessage(connectorConfig),
			}},
			Experts: []fleet.CapabilityExpert{{
				Name: "Expert", Instructions: "instructions", ConnectorNames: []string{"Shared"},
				MCPConfig: json.RawMessage(mcpConfig),
			}},
		}}
	}
	marshal := func(plan pinnedInstallationPlan) []byte {
		t.Helper()
		request, err := pinnedPlanRequest(&plan, p)
		if err != nil {
			t.Fatalf("build pinned request: %v", err)
		}
		wire, err := fleet.MarshalCapabilityInstallRequest(*request)
		if err != nil {
			t.Fatalf("marshal pinned request: %v", err)
		}
		return wire
	}
	before := marshal(makePlan(mcpBefore, connectorBefore))
	after := marshal(makePlan(mcpAfter, connectorAfter))
	if !bytes.Equal(before, after) {
		t.Fatalf("equivalent frozen JSON objects changed the Fleet request bytes:\nbefore: %s\nafter:  %s", before, after)
	}
	for _, number := range []string{"9007199254740993", "9007199254740995", "9007199254740997", "9007199254740999"} {
		if !bytes.Contains(before, []byte(number)) {
			t.Errorf("canonical request lost exact extension integer %s: %s", number, before)
		}
	}
}
