package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func capabilityDefinitionWire(t *testing.T, in any) map[string]json.RawMessage {
	t.Helper()
	var request struct {
		Definition json.RawMessage `json:"definition"`
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal capability request: %v", err)
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode capability request: %v", err)
	}
	var definition map[string]json.RawMessage
	if err := json.Unmarshal(request.Definition, &definition); err != nil {
		t.Fatalf("decode capability definition: %v", err)
	}
	return definition
}

func capabilityObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode capability object: %v", err)
	}
	return object
}

func requireWireString(t *testing.T, object map[string]json.RawMessage, field, want string) {
	t.Helper()
	var got string
	if raw, ok := object[field]; !ok || json.Unmarshal(raw, &got) != nil || got != want {
		t.Fatalf("%s = %s, want %q", field, object[field], want)
	}
}

func requireWireVersion(t *testing.T, object map[string]json.RawMessage, want *string) {
	t.Helper()
	raw, ok := object["version"]
	if !ok {
		t.Fatal("version field is missing from capability resource")
	}
	if want == nil {
		if string(raw) != "null" {
			t.Fatalf("version = %s, want explicit null", raw)
		}
		return
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil || got != *want {
		t.Fatalf("version = %s, want %q", raw, *want)
	}
}

func capabilityExpertsByMarketID(t *testing.T, definition map[string]json.RawMessage) map[string]map[string]json.RawMessage {
	t.Helper()
	var rawExperts []json.RawMessage
	if err := json.Unmarshal(definition["experts"], &rawExperts); err != nil {
		t.Fatalf("decode definition.experts: %v", err)
	}
	out := make(map[string]map[string]json.RawMessage, len(rawExperts))
	for _, raw := range rawExperts {
		expert := capabilityObject(t, raw)
		var marketID string
		if err := json.Unmarshal(expert["market_expert_id"], &marketID); err != nil || marketID == "" {
			t.Fatalf("expert market_expert_id = %s, want a source ID", expert["market_expert_id"])
		}
		if _, exists := out[marketID]; exists {
			t.Fatalf("duplicate expert source ID %q in capability definition", marketID)
		}
		out[marketID] = expert
	}
	return out
}

func TestCapabilityExpertRootUsesItsOwnMarketplaceVersion(t *testing.T) {
	svc, store, installer := capabilityFixture()
	store.plugins["expert-1"].CurrentVersion = stringPtr("4.2.0")

	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatal(err)
	}

	wireDefinition := capabilityDefinitionWire(t, installer.in)
	if len(wireDefinition) == 0 {
		t.Fatal("capability definition missing")
	}
	experts := capabilityExpertsByMarketID(t, wireDefinition)
	expert := experts["expert-1"]
	if expert == nil {
		t.Fatalf("root expert source ID was not preserved: %+v", experts)
	}
	requireWireVersion(t, expert, stringPtr("4.2.0"))
}

func TestCapabilityTeamUsesEachSourceExpertsOwnVersion(t *testing.T) {
	svc, store, installer := capabilityFixture()
	store.plugins["team-1"].CurrentVersion = stringPtr("team-7.0")
	store.plugins["expert-1"].CurrentVersion = stringPtr("expert-1.0")
	second := *store.plugins["expert-1"]
	second.ID = "expert-2"
	second.Name = "Bob"
	second.CurrentVersion = stringPtr("expert-2.0")
	store.plugins[second.ID] = &second
	store.relations[second.ID] = []model.PluginRelation{{Type: "expert_skill", TargetPluginID: "skill-1", Status: 1}}
	store.relations["team-1"] = append(store.relations["team-1"], model.PluginRelation{
		Type: "expert_team_expert", TargetPluginID: second.ID, Status: 1, SortOrder: 1,
		Data: json.RawMessage(`{"role":"reviewer"}`),
	})

	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team-1", installationParams()); err != nil {
		t.Fatal(err)
	}

	definition := capabilityDefinitionWire(t, installer.in)
	team := capabilityObject(t, definition["expert_team"])
	requireWireString(t, team, "market_expert_team_id", "team-1")
	requireWireVersion(t, team, stringPtr("team-7.0"))
	members := capabilityExpertsByMarketID(t, definition)
	requireWireVersion(t, members["expert-1"], stringPtr("expert-1.0"))
	requireWireVersion(t, members["expert-2"], stringPtr("expert-2.0"))
}

func TestCapabilityExpertWithoutMarketplaceVersionSendsNull(t *testing.T) {
	svc, store, installer := capabilityFixture()
	store.plugins["expert-1"].CurrentVersion = nil

	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams()); err != nil {
		t.Fatal(err)
	}

	experts := capabilityExpertsByMarketID(t, capabilityDefinitionWire(t, installer.in))
	requireWireVersion(t, experts["expert-1"], nil)
}

func TestPinnedTeamUsesEachSelectedSnapshotDeclaredVersion(t *testing.T) {
	svc, store, installer := fixturePinned()
	teamKey := pluginrepo.InstallVersionKey{PluginID: "team", VersionID: "root-v1"}
	firstKey := pluginrepo.InstallVersionKey{PluginID: "expert-a", VersionID: "expert-v1"}
	secondKey := pluginrepo.InstallVersionKey{PluginID: "expert-b", VersionID: "expert-v1"}
	store.versions[teamKey].DeclaredVersion = stringPtr("team-7.0")
	store.versions[firstKey].DeclaredVersion = stringPtr("expert-a-1.0")
	store.versions[secondKey].DeclaredVersion = nil

	// The root and dependencies also have newer published content. The pinned
	// team snapshot still selects root-v1 and expert-a/expert-v1; a dependency
	// without a declared snapshot version must not borrow its current version.
	store.plugins["team"].CurrentVersion = stringPtr("team-99.0")
	store.plugins["team"].CurrentVersionID = stringPtr("root-latest")
	latestTeam := *store.versions[teamKey]
	latestTeam.ID = "root-latest"
	latestTeam.Version = "root-latest"
	latestTeam.DeclaredVersion = stringPtr("team-99.0")
	latestTeam.Release = &model.PluginVersionRelease{PluginID: "team", VersionID: "root-latest"}
	store.versions[pluginrepo.InstallVersionKey{PluginID: "team", VersionID: "root-latest"}] = &latestTeam
	store.plugins["expert-a"].CurrentVersion = stringPtr("expert-a-9.0")
	store.plugins["expert-a"].CurrentVersionID = stringPtr("expert-a-latest")
	store.plugins["expert-b"].CurrentVersion = stringPtr("expert-b-9.0")
	store.plugins["expert-b"].CurrentVersionID = stringPtr("expert-b-latest")
	for pluginID, key := range map[string]pluginrepo.InstallVersionKey{
		"expert-a": firstKey,
		"expert-b": secondKey,
	} {
		latest := *store.versions[key]
		latest.ID = pluginID + "-latest"
		latest.Version = pluginID + "-latest"
		latest.DeclaredVersion = stringPtr(pluginID + "-9.0")
		latest.Release = &model.PluginVersionRelease{PluginID: pluginID, VersionID: latest.ID}
		store.versions[pluginrepo.InstallVersionKey{PluginID: pluginID, VersionID: latest.ID}] = &latest
	}

	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team", installationParams()); err != nil {
		t.Fatal(err)
	}

	definition := capabilityDefinitionWire(t, installer.in)
	team := capabilityObject(t, definition["expert_team"])
	requireWireString(t, team, "market_expert_team_id", "team")
	requireWireVersion(t, team, stringPtr("team-7.0"))
	members := capabilityExpertsByMarketID(t, definition)
	requireWireVersion(t, members["expert-a"], stringPtr("expert-a-1.0"))
	requireWireVersion(t, members["expert-b"], nil)
}

func TestPinnedExpertRootUsesItsSelectedSnapshotDeclaredVersion(t *testing.T) {
	svc, store, installer := fixturePinned()
	store.state.PublishedVersionID = stringPtr("expert-v1")
	selected := pluginrepo.InstallVersionKey{PluginID: "expert-a", VersionID: "expert-v1"}
	store.versions[selected].DeclaredVersion = stringPtr("expert-a-1.0")
	store.plugins["expert-a"].CurrentVersion = stringPtr("expert-a-9.0")
	store.plugins["expert-a"].CurrentVersionID = stringPtr("expert-a-latest")
	latest := *store.versions[selected]
	latest.ID = "expert-a-latest"
	latest.Version = "expert-a-latest"
	latest.DeclaredVersion = stringPtr("expert-a-9.0")
	latest.Release = &model.PluginVersionRelease{PluginID: "expert-a", VersionID: "expert-a-latest"}
	store.versions[pluginrepo.InstallVersionKey{PluginID: "expert-a", VersionID: "expert-a-latest"}] = &latest

	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-a", installationParams()); err != nil {
		t.Fatal(err)
	}

	experts := capabilityExpertsByMarketID(t, capabilityDefinitionWire(t, installer.in))
	requireWireVersion(t, experts["expert-a"], stringPtr("expert-a-1.0"))
}

func setCapabilitySkillSnapshot(t *testing.T, store *publishedGraphVersionStore, versionID, name, declaredVersion, content, guide string) {
	t.Helper()
	key := pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: versionID}
	version := store.versions[key]
	if version == nil {
		base := store.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}]
		if base == nil {
			t.Fatalf("missing base skill snapshot for %q", versionID)
		}
		copy := *base
		copy.ID = versionID
		copy.Version = versionID
		copy.Release = &model.PluginVersionRelease{PluginID: "skill-a", VersionID: versionID}
		version = &copy
		store.versions[key] = version
	}
	manifest, err := json.Marshal(map[string]string{"plugin_name": name, "description": "description for " + name})
	if err != nil {
		t.Fatal(err)
	}
	version.Manifest = manifest
	version.DeclaredVersion = stringPtr(declaredVersion)
	version.Package = packageWith(rawAtt("SKILL.md", content), rawAtt("references/guide.md", guide))
}

func useCurrentPublishedSkillSnapshot(store *publishedGraphVersionStore, versionID string) {
	store.published["skill-a"] = versionID
	store.plugins["skill-a"].Name = "Draft renamed skill"
	store.plugins["skill-a"].CurrentVersion = stringPtr("draft-skill-4.0")
}

func requireCapabilitySkill(t *testing.T, skills []fleet.CapabilitySkill, name, version, content, guide string) {
	t.Helper()
	for _, skill := range skills {
		if skill.Name != name {
			continue
		}
		if skill.MarketSkillID != "skill-a" || skill.Version == nil || *skill.Version != version || skill.Content != content {
			t.Fatalf("skill %q has wrong source snapshot: %+v", name, skill)
		}
		if len(skill.Files) != 1 || skill.Files[0].Path != "references/guide.md" || skill.Files[0].Source.Type != "inline_text" || skill.Files[0].Source.Content != guide {
			t.Fatalf("skill %q has wrong snapshot files: %+v", name, skill.Files)
		}
		return
	}
	t.Fatalf("missing skill %q in capability definition", name)
}

func requireExpertSkillName(t *testing.T, experts []fleet.CapabilityExpert, expertID, skillName string) {
	t.Helper()
	for _, expert := range experts {
		if expert.MarketExpertID != expertID {
			continue
		}
		if len(expert.SkillNames) != 1 || expert.SkillNames[0] != skillName {
			t.Fatalf("expert %q references skills %v, want [%q]", expertID, expert.SkillNames, skillName)
		}
		return
	}
	t.Fatalf("missing expert %q in capability definition", expertID)
}

func TestCapabilityExpertSkillPinRequiresVersionManagement(t *testing.T) {
	svc, store := publishedGraphVersionsFixture(t, pin("skill-v1"), nil)
	svc.WithVersionManagement(false).WithPinnedInstallation(false)
	setCapabilitySkillSnapshot(t, store, "skill-v1", "Pinned skill v1", "skill-1.0", "body from skill v1", "guide from skill v1")
	setCapabilitySkillSnapshot(t, store, "skill-v2", "Current skill v2", "skill-2.0", "body from skill v2", "guide from skill v2")

	currentSkill := store.plugins["skill-a"]
	currentVersion := store.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v2"}]
	currentSkill.Name = "Current skill v2"
	currentSkill.CurrentVersion = stringPtr("skill-2.0")
	currentSkill.CurrentVersionID = stringPtr("skill-v2")
	currentSkill.Manifest = currentVersion.Manifest
	currentSkill.Package = currentVersion.Package
	store.published["skill-a"] = "skill-v2"

	store.Store.(*fakeStore).relations = map[string][]model.PluginRelation{
		"expert-a": {{
			Type:            "expert_skill",
			TargetPluginID:  "skill-a",
			TargetVersionID: pin("skill-v1"),
			Status:          1,
		}},
	}
	installer := svc.capabilityInstaller.(*fakeCapabilityInstaller)
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-a", installationParams()); !errors.Is(err, ErrVersionBaselineUnavailable) || installer.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, installer.calls)
	}
}

func TestCurrentTeamInstallUsesMemberAndSkillRelationSnapshots(t *testing.T) {
	svc, store := publishedGraphVersionsFixture(t, pin("skill-v1"), nil)
	svc.WithPinnedInstallation(false)
	setCapabilitySkillSnapshot(t, store, "skill-v1", "Pinned skill v1", "skill-1.0", "body from skill v1", "guide from skill v1")
	setCapabilitySkillSnapshot(t, store, "skill-v2", "Current published skill v2", "skill-2.0", "body from skill v2", "guide from skill v2")
	useCurrentPublishedSkillSnapshot(store, "skill-v2")

	selectedKey := pluginrepo.InstallVersionKey{PluginID: "expert-a", VersionID: "expert-v1"}
	selected := store.versions[selectedKey]
	selected.DeclaredVersion = stringPtr("expert-a-1.0")
	latest := *selected
	latest.ID = "expert-a-v2"
	latest.Version = "expert-a-v2"
	latest.DeclaredVersion = stringPtr("expert-a-2.0")
	latest.Package = packageWith(
		rawAtt("AGENTS.md", "current latest instructions"),
		rawAtt("mcp.json", `{"mcpServers":{"latest":{"command":"latest-tool"}}}`),
	)
	latest.Release = &model.PluginVersionRelease{PluginID: "expert-a", VersionID: "expert-a-v2"}
	store.versions[pluginrepo.InstallVersionKey{PluginID: "expert-a", VersionID: "expert-a-v2"}] = &latest
	store.published["expert-a"] = "expert-a-v2"
	installer := svc.capabilityInstaller.(*fakeCapabilityInstaller)

	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team", installationParams()); err != nil {
		t.Fatal(err)
	}

	definition := capabilityDefinitionWire(t, installer.in)
	var rawExperts []json.RawMessage
	if err := json.Unmarshal(definition["experts"], &rawExperts); err != nil {
		t.Fatalf("decode definition.experts: %v", err)
	}
	var selectedExpert map[string]json.RawMessage
	for _, raw := range rawExperts {
		expert := capabilityObject(t, raw)
		var name string
		if err := json.Unmarshal(expert["name"], &name); err != nil {
			t.Fatalf("decode expert name: %v", err)
		}
		if name == "Expert a" {
			if selectedExpert != nil {
				t.Fatal("team contains duplicate same-name expert definitions")
			}
			selectedExpert = expert
		}
	}
	if selectedExpert == nil {
		t.Fatal("team did not resolve the source expert")
	}
	requireWireString(t, selectedExpert, "instructions", "old expert a")
	requireWireVersion(t, selectedExpert, stringPtr("expert-a-1.0"))

	capability := installer.in.Definition
	if len(capability.Skills) != 2 {
		t.Fatalf("definition has %d skills, want the pinned historical and current published snapshots: %+v", len(capability.Skills), capability.Skills)
	}
	requireCapabilitySkill(t, capability.Skills, "Pinned skill v1", "skill-1.0", "body from skill v1", "guide from skill v1")
	requireCapabilitySkill(t, capability.Skills, "Current published skill v2", "skill-2.0", "body from skill v2", "guide from skill v2")
	requireExpertSkillName(t, capability.Experts, "expert-a", "Pinned skill v1")
	requireExpertSkillName(t, capability.Experts, "expert-b", "Current published skill v2")
}

func TestCurrentTeamKeepsDistinctHistoricalSkillSnapshotsForDifferentMembers(t *testing.T) {
	svc, store := publishedGraphVersionsFixture(t, pin("skill-v1"), pin("skill-v2"))
	svc.WithPinnedInstallation(false)
	setCapabilitySkillSnapshot(t, store, "skill-v1", "Historical skill v1", "skill-1.0", "body from skill v1", "guide from skill v1")
	setCapabilitySkillSnapshot(t, store, "skill-v2", "Historical skill v2", "skill-2.0", "body from skill v2", "guide from skill v2")
	setCapabilitySkillSnapshot(t, store, "skill-v3", "Current published skill v3", "skill-3.0", "body from skill v3", "guide from skill v3")
	useCurrentPublishedSkillSnapshot(store, "skill-v3")

	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team", installationParams()); err != nil {
		t.Fatal(err)
	}

	capability := svc.capabilityInstaller.(*fakeCapabilityInstaller).in.Definition
	if len(capability.Skills) != 2 {
		t.Fatalf("definition has %d skills, want both historical snapshots of skill-a: %+v", len(capability.Skills), capability.Skills)
	}
	requireCapabilitySkill(t, capability.Skills, "Historical skill v1", "skill-1.0", "body from skill v1", "guide from skill v1")
	requireCapabilitySkill(t, capability.Skills, "Historical skill v2", "skill-2.0", "body from skill v2", "guide from skill v2")
	requireExpertSkillName(t, capability.Experts, "expert-a", "Historical skill v1")
	requireExpertSkillName(t, capability.Experts, "expert-b", "Historical skill v2")
}
