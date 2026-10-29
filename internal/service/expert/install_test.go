package expert

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
