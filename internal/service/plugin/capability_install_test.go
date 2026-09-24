package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type fakeCapabilityInstaller struct {
	enabled                      bool
	calls                        int
	token, space, workspace, key string
	in                           fleet.CapabilityInstallRequest
	replay                       bool
	replayKnown                  bool
	err                          error
}

func (f *fakeCapabilityInstaller) CapabilityInstallEnabled() bool { return f.enabled }
func (f *fakeCapabilityInstaller) InstallCapability(_ context.Context, token, space, workspace, key string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	f.calls++
	f.token, f.space, f.workspace, f.key, f.in = token, space, workspace, key, in
	if f.err != nil {
		return nil, f.err
	}
	out := &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "created-expert", Replayed: f.replay, ReplayKnown: f.replayKnown}
	if in.Definition.ExpertTeam != nil {
		out.Type, out.ExpertID, out.ExpertTeamID = "expert_team", "", "created-team"
	}
	return out, nil
}

func installationParams() InstallationParams {
	return InstallationParams{WorkspaceID: "workspace-1", RuntimeID: "7f63db70-6b66-4c2d-a52f-fb6de1d78ad9", ResourceName: "Custom name", CustomEnv: map[string]string{"OCTOBUDDY_PROVIDER_ID": "provider-current"}, IdempotencyKey: "operation-key", Token: "user-token"}
}

func capabilityFixture() (*Service, *fakeStore, *fakeCapabilityInstaller) {
	store := installFixture()
	store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "# Deploy"), rawAtt("references/checklist.md", "check before deploy"))
	installer := &fakeCapabilityInstaller{enabled: true}
	return fixedService(store).WithCapabilityInstaller(installer), store, installer
}

func TestCapabilityInstallationPreservesSingleExpertAndCustomName(t *testing.T) {
	svc, store, installer := capabilityFixture()
	installer.replayKnown = true
	store.plugins["expert-1"].Package = packageWith(rawAtt("AGENTS.md", "do the work"), rawAtt("mcp.json", `{"mcpServers":{"repository":{"command":"uvx","args":["repository-mcp"]}}}`))
	tracker := &fakeTracker{}
	svc.WithMetrics(tracker)
	out, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
	if err != nil {
		t.Fatal(err)
	}
	if out.AgentID != "created-expert" || installer.calls != 1 {
		t.Fatalf("out=%+v calls=%d", out, installer.calls)
	}
	def := installer.in.Definition
	if def.Name != "marketplace:expert-1" || def.ExpertTeam != nil || len(def.Experts) != 1 {
		t.Fatalf("definition=%+v", def)
	}
	expert := def.Experts[0]
	if expert.Name != "Custom name" || expert.Instructions != "do the work" || !strings.Contains(string(expert.MCPConfig), `"command":"uvx"`) || expert.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider-current" {
		t.Fatalf("expert=%+v", expert)
	}
	if len(def.Skills) != 1 || def.Skills[0].Content != "# Deploy" || def.Skills[0].Files[0].Source.Content != "check before deploy" {
		t.Fatalf("skills=%+v", def.Skills)
	}
	if binding := installer.in.Bindings.Experts[0]; binding.ExpertName != expert.Name || binding.RuntimeID != installationParams().RuntimeID {
		t.Fatalf("binding=%+v", binding)
	}
	if store.plugins["expert-1"].Name != "Alice" {
		t.Fatal("catalog name mutated")
	}
	if installer.space != testCaller.SpaceID || installer.workspace != "workspace-1" || installer.token != "user-token" || installer.key != "operation-key" {
		t.Fatal("trusted routing/key not forwarded")
	}
	if tracker.id != "expert-1" {
		t.Fatal("install metric missing")
	}
}

func TestCapabilityTeamSharesSkillsAndBindsEveryMember(t *testing.T) {
	svc, store, installer := capabilityFixture()
	second := *store.plugins["expert-1"]
	second.ID, second.Name = "expert-2", "Bob"
	store.plugins[second.ID] = &second
	store.relations[second.ID] = []model.PluginRelation{{Type: "expert_skill", TargetPluginID: "skill-1", Status: 1}}
	store.relations["team-1"] = append(store.relations["team-1"], model.PluginRelation{Type: "expert_team_expert", TargetPluginID: second.ID, Status: 1, SortOrder: 1, Data: json.RawMessage(`{"role":"reviewer"}`)})
	out, err := svc.CreateInstallation(context.Background(), testCaller, "team-1", installationParams())
	if err != nil {
		t.Fatal(err)
	}
	def := installer.in.Definition
	if out.SquadID != "created-team" || len(def.Skills) != 1 || len(def.Experts) != 2 || len(installer.in.Bindings.Experts) != 2 {
		t.Fatalf("out=%+v definition=%+v", out, def)
	}
	if def.ExpertTeam.Name != "Custom name" || def.ExpertTeam.Instructions != "# Team\n\n## 协作方式\n1. first\n2. second" || def.ExpertTeam.Description != "team summary" {
		t.Fatalf("team=%+v", def.ExpertTeam)
	}
	for _, expert := range def.Experts {
		if !reflect.DeepEqual(expert.SkillNames, []string{"Deploy"}) || expert.Name == "Custom name" {
			t.Fatalf("expert=%+v", expert)
		}
	}
	for _, expert := range def.Experts {
		if expert.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider-current" {
			t.Fatal("member environment lost")
		}
	}
	before, _ := json.Marshal(installer.in)
	// Relation/attachment storage ordering must not change the transmitted bytes.
	store.relations["team-1"][0], store.relations["team-1"][1] = store.relations["team-1"][1], store.relations["team-1"][0]
	store.plugins["skill-1"].Package = packageWith(rawAtt("references/checklist.md", "check before deploy"), rawAtt("SKILL.md", "# Deploy"))
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team-1", installationParams()); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(installer.in)
	if string(before) != string(after) {
		t.Fatal("retry bytes changed after source ordering changed")
	}
}

func TestCapabilityInstallationRejectsInvalidGraphBeforeFleet(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fakeStore)
	}{
		{"hidden_dependency", func(f *fakeStore) { f.declaredCounts = map[string]int{"expert-1": 2} }},
		{"missing_skill", func(f *fakeStore) { delete(f.plugins, "skill-1") }},
		{"wrong_skill_type", func(f *fakeStore) { f.plugins["skill-1"].Type = model.PluginTypeExpert }},
		{"embedded_root", func(f *fakeStore) { f.plugins["expert-1"].IsEmbedded = true }},
		{"no_skill_md", func(f *fakeStore) { f.plugins["skill-1"].Package = packageWith(rawAtt("reference.md", "text")) }},
		{"traversal", func(f *fakeStore) {
			f.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "x"), rawAtt("../escape", "x"))
		}},
		{"nul_content", func(f *fakeStore) {
			f.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "x"), rawAtt("asset.bin", "x\x00"))
		}},
		{"duplicate_path", func(f *fakeStore) {
			f.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "x"), rawAtt("SKILL.md", "y"))
		}},
		{"bad_mcp", func(f *fakeStore) {
			f.plugins["expert-1"].Package = packageWith(rawAtt("AGENTS.md", "work"), rawAtt("mcp.json", "null"))
		}},
		{"cross_space", func(f *fakeStore) {
			f.scopeAware = true
			other := "other-space"
			f.plugins["skill-1"].SpaceID = &other
			f.plugins["skill-1"].Visibility = model.PluginVisibilityPrivate
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, store, installer := capabilityFixture()
			test.change(store)
			if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err == nil || installer.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, installer.calls)
			}
		})
	}
}

func TestCapabilityInstallationRejectsStorageMCPBeforeFleet(t *testing.T) {
	for _, pluginID := range []string{"expert-1", "team-1"} {
		t.Run(pluginID, func(t *testing.T) {
			svc, store, installer := capabilityFixture()
			key := "plugins/space-a/attachments/mcp.json"
			content := []byte(`{"mcpServers":{}}`)
			sum := sha256.Sum256(content)
			svc.storage = &importStorage{objects: map[string][]byte{key: content}}
			store.plugins["expert-1"].Package = packageWith(rawAtt("AGENTS.md", "work"), `{"path":"mcp.json","content_type":"storage","mime_type":"application/json","content_size":17,"content_hash":"sha256:`+hex.EncodeToString(sum[:])+`"}`)
			store.plugins["expert-1"].AttachmentKeys = json.RawMessage(`{"mcp.json":"` + key + `"}`)
			_, err := svc.CreateInstallation(context.Background(), testCaller, pluginID, installationParams())
			var invalid *InstallationValidationError
			if !errors.As(err, &invalid) || invalid.Field != "definition.experts.mcp_config" || invalid.Reason != "unsupported_source" || installer.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, installer.calls)
			}
		})
	}
}

func TestCapabilityInstallationAllowsExpertWithoutMCP(t *testing.T) {
	svc, store, installer := capabilityFixture()
	store.plugins["expert-1"].Package = packageWith(rawAtt("AGENTS.md", "work"))
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil || installer.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, installer.calls)
	}
	if len(installer.in.Definition.Experts[0].MCPConfig) != 0 {
		t.Fatal("absent MCP configuration must stay absent")
	}
}

func TestCapabilitySkillContentCollisionDoesNotReuseWrongSkill(t *testing.T) {
	svc, store, installer := capabilityFixture()
	second := *store.plugins["skill-1"]
	second.ID = "skill-2"
	second.Package = packageWith(rawAtt("SKILL.md", "different"))
	store.plugins[second.ID] = &second
	store.relations["expert-1"] = append(store.relations["expert-1"], model.PluginRelation{Type: "expert_skill", TargetPluginID: second.ID, Status: 1})
	_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
	var invalid *InstallationValidationError
	if !errors.As(err, &invalid) || invalid.Reason != "same_name_different_content" || installer.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, installer.calls)
	}
}

func TestCapabilityStorageFilesVerifyChecksumAndScope(t *testing.T) {
	svc, store, installer := capabilityFixture()
	key := "plugins/space-a/attachments/ref.md"
	content := []byte("verified text")
	sum := sha256.Sum256(content)
	svc.storage = &importStorage{objects: map[string][]byte{key: content}}
	store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "# skill"), `{"path":"ref.md","content_type":"storage","content_size":13,"content_hash":"sha256:`+hex.EncodeToString(sum[:])+`"}`)
	store.plugins["skill-1"].AttachmentKeys = json.RawMessage(`{"ref.md":"` + key + `"}`)
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatal(err)
	}
	installer.calls = 0
	svc.storage = &importStorage{objects: map[string][]byte{key: []byte("corrupt")}}
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); !errors.Is(err, ErrIntegrity) || installer.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, installer.calls)
	}
}

func TestCapabilityReplayAndDisabledMode(t *testing.T) {
	svc, _, installer := capabilityFixture()
	tracker := &fakeTracker{}
	svc.WithMetrics(tracker)
	installer.enabled = false
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); !errors.Is(err, fleet.ErrCapabilityInstallDisabled) || installer.calls != 0 {
		t.Fatalf("err=%v", err)
	}
	installer.enabled = true
	installer.replay = true
	installer.replayKnown = true
	out, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
	if err != nil || !out.Replayed || tracker.id != "" {
		t.Fatalf("out=%+v err=%v metric=%s", out, err, tracker.id)
	}
}

func TestCapabilityFailurePhaseMarksOnlyFleetAttempts(t *testing.T) {
	svc, store, installer := capabilityFixture()
	store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "# skill"), `{"path":"ref.md","content_type":"storage","storage_uri":"plugins/space-a/attachments/missing.md"}`)
	svc.storage = &importStorage{objects: map[string][]byte{}}
	_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
	var attempt *InstallationAttemptError
	if !errors.Is(err, ErrIntegrity) || errors.As(err, &attempt) || installer.calls != 0 {
		t.Fatalf("preparation phase not preserved: %v", err)
	}
	store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "# skill"))
	installer.err = errors.New("unknown transport outcome")
	_, err = svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
	if !errors.As(err, &attempt) || !errors.Is(err, installer.err) || installer.calls != 1 {
		t.Fatalf("attempt failure was not marked: %v", err)
	}
}
