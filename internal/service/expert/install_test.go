package expert

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// fakeFleet records the atomic capability request and allows Fleet failures or
// results to be supplied by each test.
type fakeFleet struct {
	capabilityCalls                                                      int
	capabilityToken, capabilitySpace, capabilityWorkspace, capabilityKey string
	capabilityInput                                                      fleet.CapabilityInstallRequest
	capabilityResult                                                     *fleet.CapabilityInstallResult
	capabilityErr                                                        error
}

func (f *fakeFleet) InstallCapability(_ context.Context, token, spaceID, workspaceID, key string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	f.capabilityCalls++
	f.capabilityToken, f.capabilitySpace, f.capabilityWorkspace, f.capabilityKey, f.capabilityInput = token, spaceID, workspaceID, key, in
	if f.capabilityErr != nil {
		return nil, f.capabilityErr
	}
	if f.capabilityResult != nil {
		return f.capabilityResult, nil
	}
	return &fleet.CapabilityInstallResult{}, nil
}
func (*fakeFleet) HasCapabilityInstallationReceipt(context.Context, string, string, string, string) (bool, error) {
	return false, nil
}

func (*fakeFleet) ReplayCapability(context.Context, string, string, string, string, fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	return nil, nil
}

func (f *statefulCapabilityFleet) HasCapabilityInstallationReceipt(_ context.Context, _, _, _, key string) (bool, error) {
	_, exists := f.receipts[key]
	return exists, nil
}

func (f *statefulCapabilityFleet) ReplayCapability(_ context.Context, _, spaceID, workspaceID, key string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	previous, exists := f.receipts[key]
	if !exists {
		return nil, nil
	}
	fingerprint, err := json.Marshal(struct {
		SpaceID     string
		WorkspaceID string
		Request     fleet.CapabilityInstallRequest
	}{SpaceID: spaceID, WorkspaceID: workspaceID, Request: in})
	if err != nil {
		return nil, err
	}
	if previous.fingerprint != string(fingerprint) {
		return nil, &fleet.CapabilityAPIError{
			Status: http.StatusConflict, Code: "DUPLICATE", IdempotencyKeyReused: true,
		}
	}
	result := previous.result
	return &result, nil
}

type capabilityReceipt struct {
	fingerprint string
	result      fleet.CapabilityInstallResult
}

// statefulCapabilityFleet models Fleet's persisted idempotency receipt: it
// records the resource graph before optionally losing the first response.
type statefulCapabilityFleet struct {
	keys               []string
	receipts           map[string]capabilityReceipt
	commits            int
	createdResources   int
	timeoutFirstCommit bool
}

func (f *statefulCapabilityFleet) InstallCapability(_ context.Context, _, spaceID, workspaceID, key string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	if f.receipts == nil {
		f.receipts = make(map[string]capabilityReceipt)
	}
	f.keys = append(f.keys, key)
	fingerprint, err := json.Marshal(struct {
		SpaceID     string
		WorkspaceID string
		Request     fleet.CapabilityInstallRequest
	}{SpaceID: spaceID, WorkspaceID: workspaceID, Request: in})
	if err != nil {
		return nil, err
	}
	if previous, ok := f.receipts[key]; ok {
		if previous.fingerprint != string(fingerprint) {
			return nil, &fleet.CapabilityAPIError{
				Status: http.StatusConflict, Code: "DUPLICATE", IdempotencyKeyReused: true,
			}
		}
		result := previous.result
		return &result, nil
	}

	f.commits++
	result := fleet.CapabilityInstallResult{}
	if in.Definition.ExpertTeam == nil {
		result.Type = "expert"
		result.ExpertID = fmt.Sprintf("agent-%d", f.commits)
		f.createdResources += len(in.Definition.Experts)
	} else {
		result.Type = "expert_team"
		result.ExpertTeamID = fmt.Sprintf("team-%d", f.commits)
		result.LeaderExpertID = fmt.Sprintf("agent-%d", f.commits)
		f.createdResources += len(in.Definition.Experts) + 1
	}
	f.receipts[key] = capabilityReceipt{fingerprint: string(fingerprint), result: result}
	if f.timeoutFirstCommit && f.commits == 1 {
		return nil, context.DeadlineExceeded
	}
	return &result, nil
}

func assertCapabilityKeys(t *testing.T, ff *statefulCapabilityFleet, want ...string) {
	t.Helper()
	if len(ff.keys) != len(want) {
		t.Fatalf("Fleet keys = %q, want %q", ff.keys, want)
	}
	for i := range want {
		if ff.keys[i] != want[i] {
			t.Fatalf("Fleet key[%d] = %q, want exact opaque value %q", i, ff.keys[i], want[i])
		}
	}
}

// installFixture wires a Service over an in-memory store + object store holding
// one expert, and returns the caller that can see it.
func installFixture(t *testing.T, fleetClient FleetProvisioner, skills []model.SkillRef) (*Service, Caller, string) {
	t.Helper()
	store := newFakeStore()
	obj := newMemObjectStore()
	const expertID = "exp-1"
	store.experts[expertID] = &model.Expert{
		ID:          expertID,
		Name:        "Code Helper",
		Summary:     "helps with code",
		Instruction: "You are a coding expert.",
		MCPConfig:   `{"mcpServers":{}}`,
		Visibility:  model.VisibilityPublic,
		SpaceID:     "space-1",
		OwnerUID:    "owner-9",
		Skills:      skills,
	}
	for i := range skills {
		if skills[i].ObjectKey != "" {
			obj.objects[skills[i].ObjectKey] = []byte("# " + skills[i].Name)
		}
	}
	svc := New(store, obj, func() string { return "gen" }).WithFleet(fleetClient)
	caller := Caller{UID: "me", SpaceID: "space-1"}
	return svc, caller, expertID
}

func assertOnlyCapabilityInstall(t *testing.T, ff *fakeFleet) {
	t.Helper()
	if ff.capabilityCalls != 1 {
		t.Fatalf("capability calls = %d, want exactly one", ff.capabilityCalls)
	}
}

func capabilityWireDefinition(t *testing.T, in fleet.CapabilityInstallRequest) map[string]json.RawMessage {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal capability request: %v", err)
	}
	var request struct {
		Definition map[string]json.RawMessage `json:"definition"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode capability request: %v", err)
	}
	return request.Definition
}

func capabilityWireObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode capability object: %v", err)
	}
	return object
}

func requireNullVersion(t *testing.T, object map[string]json.RawMessage) {
	t.Helper()
	version, ok := object["version"]
	if !ok || string(version) != "null" {
		t.Fatalf("version = %s, want explicit null", version)
	}
}

func capabilityWireExpert(t *testing.T, in fleet.CapabilityInstallRequest, name string) map[string]json.RawMessage {
	t.Helper()
	definition := capabilityWireDefinition(t, in)
	var experts []json.RawMessage
	if err := json.Unmarshal(definition["experts"], &experts); err != nil {
		t.Fatalf("decode wire experts: %v (%s)", err, definition["experts"])
	}
	for _, raw := range experts {
		expert := capabilityWireObject(t, raw)
		var gotName string
		if err := json.Unmarshal(expert["name"], &gotName); err != nil {
			t.Fatalf("decode expert name: %v", err)
		}
		if gotName == name {
			return expert
		}
	}
	t.Fatalf("capability definition has no expert %q", name)
	return nil
}

func capabilityConnectorForExpert(t *testing.T, in fleet.CapabilityInstallRequest, expertName string) map[string]json.RawMessage {
	t.Helper()
	definition := capabilityWireDefinition(t, in)
	expert := capabilityWireExpert(t, in, expertName)
	var connectorNames []string
	if err := json.Unmarshal(expert["connector_names"], &connectorNames); err != nil {
		t.Fatalf("decode %s connector names: %v", expertName, err)
	}
	if len(connectorNames) != 1 || connectorNames[0] == "" {
		t.Fatalf("%s connector names = %q, want one bound MCP connector", expertName, connectorNames)
	}
	var connectors []json.RawMessage
	if err := json.Unmarshal(definition["connectors"], &connectors); err != nil {
		t.Fatalf("decode wire connectors: %v (%s)", err, definition["connectors"])
	}
	if len(connectors) != 1 {
		t.Fatalf("capability connectors = %s, want one connector", definition["connectors"])
	}
	connector := capabilityWireObject(t, connectors[0])
	var connectorName string
	if err := json.Unmarshal(connector["name"], &connectorName); err != nil {
		t.Fatalf("decode connector name: %v", err)
	}
	if connectorName != connectorNames[0] {
		t.Fatalf("%s connector name = %q, binding = %q", expertName, connectorName, connectorNames[0])
	}
	return connector
}

func requireJSONValue(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode actual JSON value: %v (%s)", err, got)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode expected JSON value: %v (%s)", err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON value = %s, want complete value %s", got, want)
	}
}

func TestInstallExpertUsesWorkspaceConnectorForMCPConfig(t *testing.T) {
	const config = `{"mcpServers":{"local":{"type":"stdio","command":"npx","args":["tool"],"env":{"X":"fixture"},"x-server":{"mode":"strict"}}},"x-vendor":{"enabled":true,"labels":["one","two"]}}`
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "agent-1"}}
	svc, caller, id := installFixture(t, ff, nil)
	svc.repo.(*fakeStore).experts[id].MCPConfig = config

	if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	expert := capabilityWireExpert(t, ff.capabilityInput, "Code Helper")
	if _, ok := expert["mcp_config"]; ok {
		t.Fatalf("expert still carries legacy mcp_config: %s", expert["mcp_config"])
	}
	requireNullVersion(t, expert)
	connector := capabilityConnectorForExpert(t, ff.capabilityInput, "Code Helper")
	requireNullVersion(t, connector)
	if _, ok := connector["market_mcp_id"]; ok {
		t.Fatalf("config without marketplace source identity gained market_mcp_id: %s", connector["market_mcp_id"])
	}
	requireJSONValue(t, connector["config"], json.RawMessage(config))
}

func TestInstallExpertWithoutMCPConfigDoesNotCreateConnector(t *testing.T) {
	for _, raw := range []string{"", `{}`, `{"mcpServers":{}}`} {
		t.Run(fmt.Sprintf("config_%q", raw), func(t *testing.T) {
			ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "agent-1"}}
			svc, caller, id := installFixture(t, ff, nil)
			svc.repo.(*fakeStore).experts[id].MCPConfig = raw

			if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err != nil {
				t.Fatalf("InstallExpert: %v", err)
			}
			definition := capabilityWireDefinition(t, ff.capabilityInput)
			if _, ok := definition["connectors"]; ok {
				t.Fatalf("expert without MCP emitted connectors: %s", definition["connectors"])
			}
			expert := capabilityWireExpert(t, ff.capabilityInput, "Code Helper")
			requireNullVersion(t, expert)
			if _, ok := expert["connector_names"]; ok {
				t.Fatalf("expert without MCP emitted connector_names: %s", expert["connector_names"])
			}
			if _, ok := expert["mcp_config"]; ok {
				t.Fatalf("expert without MCP emitted mcp_config: %s", expert["mcp_config"])
			}
		})
	}
}

func TestInstallExpertRejectsMalformedMCPConnectorBeforeFleet(t *testing.T) {
	ff := &fakeFleet{}
	svc, caller, id := installFixture(t, ff, nil)
	svc.repo.(*fakeStore).experts[id].MCPConfig = `{"mcpServers":{"local":"not-a-server-object"}}`

	if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err == nil {
		t.Fatal("InstallExpert accepted malformed MCP server configuration")
	}
	if ff.capabilityCalls != 0 {
		t.Fatalf("Fleet capability calls = %d, want no remote install for malformed MCP", ff.capabilityCalls)
	}
}

func TestInstallExpertReplaysUncertainCapabilityInstallByIdempotencyKey(t *testing.T) {
	ff := &statefulCapabilityFleet{timeoutFirstCommit: true}
	svc, caller, id := installFixture(t, ff, nil)
	nextGeneratedKey := 0
	svc.idGen = func() string {
		nextGeneratedKey++
		return fmt.Sprintf("generated-%d", nextGeneratedKey)
	}
	input := baseInput()
	const firstKey = "marketplace/expert-install:v1+opaque_key"
	input.IdempotencyKey = firstKey

	if _, err := svc.InstallExpert(context.Background(), caller, id, input); !errors.Is(err, context.DeadlineExceeded) ||
		!errors.Is(err, ErrFleetInstallUnavailable) {
		t.Fatalf("first InstallExpert error = %v, want uncertain deadline error", err)
	}
	retried, err := svc.InstallExpert(context.Background(), caller, id, input)
	if err != nil {
		t.Fatalf("retry InstallExpert: %v", err)
	}
	if retried.AgentID != "agent-1" || ff.createdResources != 1 {
		t.Fatalf("retry result/resources = %+v/%d, want original agent-1 and one remote resource", retried, ff.createdResources)
	}

	input.IdempotencyKey = "marketplace/expert-install:v1+different"
	different, err := svc.InstallExpert(context.Background(), caller, id, input)
	if err != nil {
		t.Fatalf("InstallExpert with a different key: %v", err)
	}
	if different.AgentID != "agent-2" || ff.createdResources != 2 {
		t.Fatalf("different-key result/resources = %+v/%d, want new agent-2 and two remote resources", different, ff.createdResources)
	}
	assertCapabilityKeys(t, ff, firstKey, input.IdempotencyKey)
}

func TestInstallExpertGeneratesFreshKeysWhenIdempotencyKeyIsAbsent(t *testing.T) {
	ff := &statefulCapabilityFleet{}
	svc, caller, id := installFixture(t, ff, nil)
	nextKey := 0
	svc.idGen = func() string {
		nextKey++
		return fmt.Sprintf("generated-%d", nextKey)
	}

	first, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if err != nil {
		t.Fatalf("first InstallExpert: %v", err)
	}
	second, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if err != nil {
		t.Fatalf("second InstallExpert: %v", err)
	}
	if first.AgentID != "agent-1" || second.AgentID != "agent-2" || ff.createdResources != 2 {
		t.Fatalf("keyless results/resources = %q, %q / %d, want distinct installs", first.AgentID, second.AgentID, ff.createdResources)
	}
	assertCapabilityKeys(t, ff, "generated-1", "generated-2")
}

func baseInput() InstallInput {
	return InstallInput{WorkspaceID: "ws-1", RuntimeID: "rt-1", SpaceID: "space-1", Token: "tok"}
}

// recordingTracker records TrackInstall calls for asserting install counting.
type recordingTracker struct {
	installs []string
}

func (r *recordingTracker) TrackInstall(_ context.Context, resourceType, resourceID string) error {
	r.installs = append(r.installs, resourceType+"/"+resourceID)
	return nil
}

// A successful expert install bumps install_count exactly once under the
// source expert ID; a failed capability transaction must not count.
func TestInstallExpertTracksInstall(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "agent-1"}}
	svc, caller, id := installFixture(t, ff, nil)
	tracker := &recordingTracker{}
	svc = svc.WithMetrics(tracker)

	res, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	if res.AgentID != "agent-1" {
		t.Fatalf("AgentID = %q", res.AgentID)
	}
	if len(tracker.installs) != 1 || tracker.installs[0] != "expert/"+id {
		t.Fatalf("installs = %v, want [expert/%s]", tracker.installs, id)
	}

	ffFail := &fakeFleet{capabilityErr: errors.New("capability rejected")}
	svcFail, callerFail, idFail := installFixture(t, ffFail, nil)
	trackerFail := &recordingTracker{}
	svcFail = svcFail.WithMetrics(trackerFail)
	if _, err := svcFail.InstallExpert(context.Background(), callerFail, idFail, baseInput()); err == nil {
		t.Fatal("expected install failure")
	}
	if len(trackerFail.installs) != 0 {
		t.Fatalf("failed install must not count, got %v", trackerFail.installs)
	}
}

func TestInstallExpertHappyPathWithSkills(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "agent-1"}}
	svc, caller, id := installFixture(t, ff, []model.SkillRef{
		{Name: "Research", ObjectKey: "k/0"},
		{Name: "Format", ObjectKey: "k/1"},
	})

	input := baseInput()
	input.CustomEnv = map[string]string{"OCTOBUDDY_PROVIDER_ID": "provider-1"}
	res, err := svc.InstallExpert(context.Background(), caller, id, input)
	if err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	if res.AgentID != "agent-1" {
		t.Fatalf("AgentID = %q, want agent-1", res.AgentID)
	}
	assertOnlyCapabilityInstall(t, ff)
	expert := ff.capabilityInput.Definition.Experts[0]
	if expert.Name != "Code Helper" || expert.MarketExpertID != id || expert.Description != "helps with code" ||
		expert.Instructions != "You are a coding expert." ||
		expert.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider-1" ||
		len(expert.SkillNames) != 2 || expert.SkillNames[0] != "Research" || expert.SkillNames[1] != "Format" {
		t.Fatalf("capability expert = %+v", expert)
	}
	if binding := ff.capabilityInput.Bindings.Experts; len(binding) != 1 ||
		binding[0].ExpertName != "Code Helper" || binding[0].RuntimeID != "rt-1" {
		t.Fatalf("runtime bindings = %+v", binding)
	}
	if skills := ff.capabilityInput.Definition.Skills; len(skills) != 2 ||
		skills[0].Name != "Research" || skills[0].Content != "# Research" ||
		skills[1].Name != "Format" || skills[1].Content != "# Format" {
		t.Fatalf("capability skills = %+v", skills)
	}
}

func TestInstallExpertNoSkills(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "agent-1"}}
	svc, caller, id := installFixture(t, ff, nil)

	if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	assertOnlyCapabilityInstall(t, ff)
	if len(ff.capabilityInput.Definition.Skills) != 0 || len(ff.capabilityInput.Definition.Experts[0].SkillNames) != 0 {
		t.Fatalf("capability unexpectedly contains skills: %+v", ff.capabilityInput.Definition)
	}
}

func TestInstallExpertSkipsNameOnlySkills(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "agent-1"}}
	svc, caller, id := installFixture(t, ff, []model.SkillRef{
		{Name: "Named only"},
		{Name: "Real", ObjectKey: "k/1"},
	})

	if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	skills := ff.capabilityInput.Definition.Skills
	if len(skills) != 1 || skills[0].Name != "Real" {
		t.Fatalf("capability skills = %#v, want only Real", skills)
	}
	if names := ff.capabilityInput.Definition.Experts[0].SkillNames; len(names) != 1 || names[0] != "Real" {
		t.Fatalf("expert skill names = %#v, want [Real]", names)
	}
}

func TestInstallExpertRejectsMissingTarget(t *testing.T) {
	ff := &fakeFleet{}
	svc, caller, id := installFixture(t, ff, nil)
	_, err := svc.InstallExpert(context.Background(), caller, id, InstallInput{RuntimeID: "rt-1", Token: "tok"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if ff.capabilityCalls != 0 {
		t.Fatal("Fleet should not be called without a Workspace")
	}
}

func TestInstallExpertHiddenExpertNotFound(t *testing.T) {
	ff := &fakeFleet{}
	svc, _, id := installFixture(t, ff, nil)
	// A caller in a different space cannot see the public expert.
	other := Caller{UID: "stranger", SpaceID: "space-2"}
	_, err := svc.InstallExpert(context.Background(), other, id, baseInput())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if ff.capabilityCalls != 0 {
		t.Fatal("Fleet should not be called for a hidden expert")
	}
}
func TestInstallExpertNilFleetIsNotConfigured(t *testing.T) {
	store := newFakeStore()
	svc := New(store, newMemObjectStore(), func() string { return "gen" }) // no WithFleet
	_, err := svc.InstallExpert(context.Background(), Caller{UID: "me", SpaceID: "space-1"}, "exp-1", baseInput())
	if !errors.Is(err, ErrFleetNotConfigured) {
		t.Fatalf("err = %v, want ErrFleetNotConfigured", err)
	}
}

// makeSkillZip builds an in-memory .zip whose entries are (path, content).
func makeSkillZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for path, content := range entries {
		w, err := zw.Create(path)
		if err != nil {
			t.Fatalf("zip create %s: %v", path, err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatalf("zip write %s: %v", path, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestInstallExpertUsesSingleCapabilityTransaction(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "capability-agent"}}
	svc, caller, id := installFixture(t, ff, []model.SkillRef{{Name: "Research", ObjectKey: "k/research", ZipObjectKey: "k/research.zip"}})
	if err := svc.store.(*memObjectStore).PutObject(context.Background(), "k/research.zip", bytes.NewReader(makeSkillZip(t, map[string][]byte{
		"SKILL.md":            []byte("# Research"),
		"references/guide.md": []byte("Keep supporting content"),
	})), -1, "application/zip"); err != nil {
		t.Fatalf("store skill package: %v", err)
	}
	input := baseInput()
	input.CustomEnv = map[string]string{"OCTOBUDDY_PROVIDER_ID": "provider-1"}

	result, err := svc.InstallExpert(context.Background(), caller, id, input)
	if err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	if result.AgentID != "capability-agent" {
		t.Fatalf("AgentID = %q, want Fleet's capability result", result.AgentID)
	}
	assertOnlyCapabilityInstall(t, ff)
	if ff.capabilityToken != "tok" || ff.capabilitySpace != "space-1" || ff.capabilityWorkspace != "ws-1" || ff.capabilityKey == "" {
		t.Fatalf("capability routing = token:%q space:%q workspace:%q key:%q", ff.capabilityToken, ff.capabilitySpace, ff.capabilityWorkspace, ff.capabilityKey)
	}
	if len(ff.capabilityInput.Definition.Experts) != 1 || len(ff.capabilityInput.Bindings.Experts) != 1 {
		t.Fatalf("capability request omitted the expert or its binding: %+v", ff.capabilityInput)
	}
	expert := ff.capabilityInput.Definition.Experts[0]
	if expert.Name != "Code Helper" || expert.Description != "helps with code" || expert.Instructions != "You are a coding expert." ||
		expert.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider-1" ||
		len(expert.SkillNames) != 1 || expert.SkillNames[0] != "Research" {
		t.Fatalf("legacy expert content changed in capability definition: %+v", expert)
	}
	if binding := ff.capabilityInput.Bindings.Experts[0]; binding.ExpertName != "Code Helper" || binding.RuntimeID != "rt-1" {
		t.Fatalf("capability runtime binding = %+v", binding)
	}
	if len(ff.capabilityInput.Definition.Skills) != 1 {
		t.Fatalf("capability skills = %+v", ff.capabilityInput.Definition.Skills)
	}
	skill := ff.capabilityInput.Definition.Skills[0]
	if skill.Name != "Research" || skill.Content != "# Research" || len(skill.Files) != 1 ||
		skill.Files[0].Path != "references/guide.md" || skill.Files[0].Source.Type != "inline_text" ||
		skill.Files[0].Source.Content != "Keep supporting content" {
		t.Fatalf("legacy skill content changed: %+v", skill)
	}
	wire := capabilityWireDefinition(t, ff.capabilityInput)
	var wireExperts []json.RawMessage
	if err := json.Unmarshal(wire["experts"], &wireExperts); err != nil || len(wireExperts) != 1 {
		t.Fatalf("decode wire experts: %v (%s)", err, wire["experts"])
	}
	requireNullVersion(t, capabilityWireObject(t, wireExperts[0]))
	var wireSkills []json.RawMessage
	if err := json.Unmarshal(wire["skills"], &wireSkills); err != nil || len(wireSkills) != 1 {
		t.Fatalf("decode wire skills: %v (%s)", err, wire["skills"])
	}
	requireNullVersion(t, capabilityWireObject(t, wireSkills[0]))
}

func TestInstallExpertCapabilityFailureCreatesNoSeparateResources(t *testing.T) {
	wantErr := errors.New("capability transaction rejected")
	ff := &fakeFleet{capabilityErr: wantErr}
	svc, caller, id := installFixture(t, ff, []model.SkillRef{{Name: "Research", ObjectKey: "k/research"}})

	_, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if !errors.Is(err, wantErr) || !errors.Is(err, ErrFleetInstallUnavailable) {
		t.Fatalf("InstallExpert error = %v, want Fleet capability error marked unavailable", err)
	}
	assertOnlyCapabilityInstall(t, ff)
}
