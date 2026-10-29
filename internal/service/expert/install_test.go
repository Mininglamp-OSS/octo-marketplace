package expert

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

func capabilityMCPConfigForExpert(t *testing.T, in fleet.CapabilityInstallRequest, name string) json.RawMessage {
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
			return expert["mcp_config"]
		}
	}
	t.Fatalf("capability definition has no expert %q", name)
	return nil
}

func capabilityMCPServers(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var config struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("decode MCP config: %v (%s)", err, raw)
	}
	return config.MCPServers
}

func requireLegacyStdioMCPServer(t *testing.T, raw json.RawMessage) {
	t.Helper()
	servers := capabilityMCPServers(t, raw)
	var server map[string]json.RawMessage
	if err := json.Unmarshal(servers["local"], &server); err != nil {
		t.Fatalf("decode local MCP server: %v (%s)", err, servers["local"])
	}
	if _, ok := server["type"]; ok {
		t.Fatalf("stdio capability server still has legacy type field: %s", servers["local"])
	}
	var command string
	var args []string
	var env map[string]string
	if err := json.Unmarshal(server["command"], &command); err != nil {
		t.Fatalf("decode stdio command: %v", err)
	}
	if err := json.Unmarshal(server["args"], &args); err != nil {
		t.Fatalf("decode stdio args: %v", err)
	}
	if err := json.Unmarshal(server["env"], &env); err != nil {
		t.Fatalf("decode stdio env: %v", err)
	}
	if command != "npx" || len(args) != 1 || args[0] != "tool" || len(env) != 1 || env["X"] != "fixture" {
		t.Fatalf("stdio capability config = %s, want command npx, args [tool], env {X:fixture}", servers["local"])
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
	assertCapabilityKeys(t, ff, firstKey, firstKey, input.IdempotencyKey)
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

func TestInstallExpertNormalizesLegacyStdioMCPConfig(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "agent-1"}}
	svc, caller, id := installFixture(t, ff, nil)
	svc.repo.(*fakeStore).experts[id].MCPConfig = `{"mcpServers":{"local":{"type":"stdio","command":"npx","args":["tool"],"env":{"X":"fixture"}}}}`

	if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	requireLegacyStdioMCPServer(t, capabilityMCPConfigForExpert(t, ff.capabilityInput, "Code Helper"))
}

func TestInstallExpertPreservesLegacyMCPConfigOverridesAndRemoteServers(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "explicit empty", raw: `{}`},
		{name: "wrapped empty", raw: `{"mcpServers":{}}`},
		{name: "remote servers", raw: `{"mcpServers":{"local":{"type":"stdio","command":"npx","args":["tool"],"env":{"X":"fixture"}},"http":{"type":"http","url":"https://mcp.example.test/mcp","headers":{"X-Fixture":"header"}},"url_only":{"url":"https://mcp.example.test/events"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "agent-1"}}
			svc, caller, id := installFixture(t, ff, nil)
			svc.repo.(*fakeStore).experts[id].MCPConfig = tc.raw

			if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err != nil {
				t.Fatalf("InstallExpert: %v", err)
			}
			got := capabilityMCPConfigForExpert(t, ff.capabilityInput, "Code Helper")
			if len(got) == 0 {
				t.Fatal("explicit legacy MCP config was omitted from capability definition")
			}
			switch tc.name {
			case "explicit empty":
				var object map[string]json.RawMessage
				if err := json.Unmarshal(got, &object); err != nil || len(object) != 0 {
					t.Fatalf("explicit empty config = %s, want {}", got)
				}
			case "wrapped empty":
				if servers := capabilityMCPServers(t, got); servers == nil || len(servers) != 0 {
					t.Fatalf("wrapped empty config = %s, want an explicit empty mcpServers object", got)
				}
			case "remote servers":
				requireLegacyStdioMCPServer(t, got)
				servers := capabilityMCPServers(t, got)
				for name, want := range map[string]struct {
					typeName string
					url      string
				}{
					"http":     {typeName: "http", url: "https://mcp.example.test/mcp"},
					"url_only": {url: "https://mcp.example.test/events"},
				} {
					var server map[string]json.RawMessage
					if err := json.Unmarshal(servers[name], &server); err != nil {
						t.Fatalf("decode %s server: %v", name, err)
					}
					if want.typeName == "" {
						if _, ok := server["type"]; ok {
							t.Fatalf("remote MCP server %q gained type: %s", name, servers[name])
						}
					} else {
						var gotType string
						if err := json.Unmarshal(server["type"], &gotType); err != nil || gotType != want.typeName {
							t.Fatalf("remote MCP server %q type = %q, want %q", name, gotType, want.typeName)
						}
					}
					var gotURL string
					if err := json.Unmarshal(server["url"], &gotURL); err != nil || gotURL != want.url {
						t.Fatalf("remote MCP server %q URL = %q, want %q", name, gotURL, want.url)
					}
					if name == "http" {
						var headers map[string]string
						if err := json.Unmarshal(server["headers"], &headers); err != nil || headers["X-Fixture"] != "header" {
							t.Fatalf("remote MCP server headers = %s, want X-Fixture=header", server["headers"])
						}
					}
				}
			}
		})
	}
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
		expert.Instructions != "You are a coding expert." || string(expert.MCPConfig) != `{"mcpServers":{}}` ||
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
		string(expert.MCPConfig) != `{"mcpServers":{}}` || expert.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider-1" ||
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
