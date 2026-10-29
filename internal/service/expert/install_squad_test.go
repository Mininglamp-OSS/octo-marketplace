package expert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// installSquadFixture wires a Service over an in-memory store holding one squad
// with the given members, and returns the caller that can see it.
func installSquadFixture(t *testing.T, fleetClient FleetProvisioner, members []model.SquadMember) (*Service, Caller, string) {
	return installSquadFixtureStrategies(t, fleetClient, members, nil)
}

// installSquadFixtureStrategies is installSquadFixture with squad-level
// dispatch strategies seeded for the capability request.
func installSquadFixtureStrategies(t *testing.T, fleetClient FleetProvisioner, members []model.SquadMember, strategies []string) (*Service, Caller, string) {
	t.Helper()
	store := newFakeStore()
	obj := newMemObjectStore()
	const squadID = "sq-1"
	store.squads[squadID] = &model.Squad{
		ID:         squadID,
		Name:       "Delivery Squad",
		Summary:    "ships features",
		Visibility: model.VisibilityPublic,
		SpaceID:    "space-1",
		OwnerUID:   "owner-9",
		Strategies: strategies,
		Members:    members,
	}
	for i := range members {
		for j := range members[i].Skills {
			if key := members[i].Skills[j].ObjectKey; key != "" {
				obj.objects[key] = []byte("# " + members[i].Skills[j].Name)
			}
		}
	}
	svc := New(store, obj, func() string { return "gen" }).WithFleet(fleetClient)
	caller := Caller{UID: "me", SpaceID: "space-1"}
	return svc, caller, squadID
}

// threeMembers is a leader (index 1) flanked by two workers, exercising
// leader selection, member ordering, and role defaults.
func threeMembers() []model.SquadMember {
	return []model.SquadMember{
		{MemberKey: "member_01", Name: "Planner", Role: "planner", Instruction: "plan things"},
		{MemberKey: "member_02", Name: "Lead", Role: "lead", IsLeader: true, Instruction: "lead the team", MCPConfig: `{"mcpServers":{}}`},
		{MemberKey: "member_03", Name: "Coder", Instruction: "write code"}, // no role → defaults to "member"
	}
}

// A successful squad install counts once under resource_type "squad" and a
// failed capability transaction must not count.
func TestInstallSquadTracksInstallOnlyForSquad(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert_team", ExpertTeamID: "squad-x", LeaderExpertID: "leader-x"}}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())
	tracker := &recordingTracker{}
	svc = svc.WithMetrics(tracker)

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if len(tracker.installs) != 1 || tracker.installs[0] != "squad/"+id {
		t.Fatalf("installs = %v, want exactly [squad/%s]", tracker.installs, id)
	}

	ffFail := &fakeFleet{capabilityErr: errors.New("capability rejected")}
	svcFail, callerFail, idFail := installSquadFixture(t, ffFail, threeMembers())
	trackerFail := &recordingTracker{}
	svcFail = svcFail.WithMetrics(trackerFail)
	if _, err := svcFail.InstallSquad(context.Background(), callerFail, idFail, baseInput()); err == nil {
		t.Fatal("expected install failure")
	}
	if len(trackerFail.installs) != 0 {
		t.Fatalf("failed install must not count, got %v", trackerFail.installs)
	}
}

func TestInstallSquadHappyPath(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{
		Type: "expert_team", ExpertTeamID: "squad-x", LeaderExpertID: "leader-x",
	}}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())

	res, err := svc.InstallSquad(context.Background(), caller, id, baseInput())
	if err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if res.SquadID != "squad-x" || res.LeaderAgentID != "leader-x" {
		t.Fatalf("result = %#v, want {squad-x, leader-x}", res)
	}
	assertOnlyCapabilityInstall(t, ff)
	if ff.capabilityToken != "tok" || ff.capabilitySpace != "space-1" || ff.capabilityWorkspace != "ws-1" || ff.capabilityKey == "" {
		t.Fatalf("capability routing = token:%q space:%q workspace:%q key:%q", ff.capabilityToken, ff.capabilitySpace, ff.capabilityWorkspace, ff.capabilityKey)
	}
	if team := ff.capabilityInput.Definition.ExpertTeam; team == nil || team.Name != "Delivery Squad" || len(team.Members) != 3 {
		t.Fatalf("capability team = %+v", team)
	}
}

func TestInstallSquadBindsSharedSkillsToEveryMember(t *testing.T) {
	members := []model.SquadMember{
		{
			MemberKey: "member_01",
			Name:      "Planner",
			IsLeader:  true,
			Skills: []model.SkillRef{
				{Name: "Shared Skill", ObjectKey: "members/1/shared"},
				{Name: "Planner Only", ObjectKey: "members/1/only"},
			},
		},
		{
			MemberKey: "member_02",
			Name:      "Coder",
			Skills: []model.SkillRef{
				{Name: "Shared Skill", ObjectKey: "members/2/shared"},
				{Name: "Coder Only", ObjectKey: "members/2/only"},
			},
		},
	}
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert_team", ExpertTeamID: "squad-x", LeaderExpertID: "a0"}}
	svc, caller, id := installSquadFixtureStrategies(t, ff, members, []string{"先分析", "再执行"})

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	assertOnlyCapabilityInstall(t, ff)
	definition := ff.capabilityInput.Definition
	wantNames := []string{"Shared Skill", "Planner Only", "Coder Only"}
	if len(definition.Skills) != len(wantNames) {
		t.Fatalf("capability skills = %+v, want names %v", definition.Skills, wantNames)
	}
	for i, want := range wantNames {
		if definition.Skills[i].Name != want {
			t.Fatalf("capability skills[%d].Name = %q, want %q", i, definition.Skills[i].Name, want)
		}
	}
	experts := make(map[string]fleet.CapabilityExpert, len(definition.Experts))
	for _, expert := range definition.Experts {
		experts[expert.Name] = expert
	}
	if got := experts["Planner"].SkillNames; len(got) != 2 || got[0] != "Shared Skill" || got[1] != "Planner Only" {
		t.Fatalf("Planner skill names = %#v", got)
	}
	if got := experts["Coder"].SkillNames; len(got) != 2 || got[0] != "Shared Skill" || got[1] != "Coder Only" {
		t.Fatalf("Coder skill names = %#v", got)
	}
	if team := definition.ExpertTeam; team == nil || team.Instructions != "1. 先分析\n2. 再执行" {
		t.Fatalf("team instructions = %+v", team)
	}
}

// Skill names remain byte-exact across the one capability definition.
func TestInstallSquadKeepsCaseAndWhitespaceVariantSkillNames(t *testing.T) {
	members := []model.SquadMember{
		{MemberKey: "member_01", Name: "Planner", IsLeader: true, Skills: []model.SkillRef{
			{Name: "Deploy", ObjectKey: "members/1/deploy"},
		}},
		{MemberKey: "member_02", Name: "Coder", Skills: []model.SkillRef{
			{Name: "deploy", ObjectKey: "members/2/deploy-lower"},
			{Name: "Deploy ", ObjectKey: "members/2/deploy-space"},
		}},
	}
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert_team", ExpertTeamID: "squad-x", LeaderExpertID: "a0"}}
	svc, caller, id := installSquadFixture(t, ff, members)

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	skills := ff.capabilityInput.Definition.Skills
	if len(skills) != 3 || skills[0].Name != "Deploy" || skills[1].Name != "deploy" || skills[2].Name != "Deploy " {
		t.Fatalf("capability skills = %+v, want exact names [Deploy deploy Deploy ]", skills)
	}
	if names := ff.capabilityInput.Definition.Experts[1].SkillNames; len(names) != 2 || names[0] != "deploy" || names[1] != "Deploy " {
		t.Fatalf("Coder skill names = %#v", names)
	}
}

func TestInstallSquadWritesStrategiesAsInstructions(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert_team", ExpertTeamID: "squad-x", LeaderExpertID: "a1"}}
	strategies := []string{"先分析需求", "  ", "再分派给成员"}
	svc, caller, id := installSquadFixtureStrategies(t, ff, threeMembers(), strategies)

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if team := ff.capabilityInput.Definition.ExpertTeam; team == nil || team.Instructions != "1. 先分析需求\n2. 再分派给成员" {
		t.Fatalf("capability team instructions = %+v", team)
	}
}

func TestInstallSquadSkipsInstructionsWithoutStrategies(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert_team", ExpertTeamID: "squad-x", LeaderExpertID: "a1"}}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if team := ff.capabilityInput.Definition.ExpertTeam; team == nil || team.Instructions != "" {
		t.Fatalf("capability team instructions = %+v, want empty", team)
	}
}

func TestInstallSquadDefaultsLeaderToFirstMember(t *testing.T) {
	members := []model.SquadMember{
		{MemberKey: "member_01", Name: "First", Role: "a", Instruction: "i"},
		{MemberKey: "member_02", Name: "Second", Role: "b", Instruction: "i"},
	}
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert_team", ExpertTeamID: "sq", LeaderExpertID: "a0"}}
	svc, caller, id := installSquadFixture(t, ff, members)

	res, err := svc.InstallSquad(context.Background(), caller, id, baseInput())
	if err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if res.LeaderAgentID != "a0" {
		t.Fatalf("leader = %q, want a0 (first member)", res.LeaderAgentID)
	}
	team := ff.capabilityInput.Definition.ExpertTeam
	if team == nil || len(team.Members) != 2 || !team.Members[0].IsLeader || team.Members[1].IsLeader ||
		team.Members[0].Role != "leader" || team.Members[1].Role != "b" {
		t.Fatalf("team members = %+v", team)
	}
}

func TestInstallSquadEmptyMembersIsInvalid(t *testing.T) {
	ff := &fakeFleet{}
	svc, caller, id := installSquadFixture(t, ff, nil)
	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if ff.capabilityCalls != 0 {
		t.Fatal("Fleet should not be called for a memberless squad")
	}
}

func TestInstallSquadNilFleetIsNotConfigured(t *testing.T) {
	store := newFakeStore()
	svc := New(store, newMemObjectStore(), func() string { return "gen" }) // no WithFleet
	_, err := svc.InstallSquad(context.Background(), Caller{UID: "me", SpaceID: "space-1"}, "sq-1", baseInput())
	if !errors.Is(err, ErrFleetNotConfigured) {
		t.Fatalf("err = %v, want ErrFleetNotConfigured", err)
	}
}

func TestInstallSquadRejectsMissingTarget(t *testing.T) {
	ff := &fakeFleet{}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())
	_, err := svc.InstallSquad(context.Background(), caller, id, InstallInput{RuntimeID: "rt-1", Token: "tok"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if ff.capabilityCalls != 0 {
		t.Fatal("Fleet should not be called without a Workspace")
	}
}

func TestInstallSquadHiddenSquadNotFound(t *testing.T) {
	ff := &fakeFleet{}
	svc, _, id := installSquadFixture(t, ff, threeMembers())
	other := Caller{UID: "stranger", SpaceID: "space-2"} // different space cannot see it
	if _, err := svc.InstallSquad(context.Background(), other, id, baseInput()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if ff.capabilityCalls != 0 {
		t.Fatal("Fleet should not be called for a hidden squad")
	}
}

func TestInstallSquadUsesSingleCapabilityTransaction(t *testing.T) {
	members := threeMembers()
	members[0].Skills = []model.SkillRef{{Name: "Research", ObjectKey: "k/research", ZipObjectKey: "k/research.zip"}}
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{
		Type: "expert_team", ExpertTeamID: "capability-team", LeaderExpertID: "capability-leader",
	}}
	svc, caller, id := installSquadFixtureStrategies(t, ff, members, []string{"Dispatch to owner", "Report status"})
	if err := svc.store.(*memObjectStore).PutObject(context.Background(), "k/research.zip", bytes.NewReader(makeSkillZip(t, map[string][]byte{
		"SKILL.md":            []byte("# Research"),
		"references/guide.md": []byte("Keep supporting content"),
	})), -1, "application/zip"); err != nil {
		t.Fatalf("store skill package: %v", err)
	}
	input := baseInput()
	input.CustomEnv = map[string]string{"OCTOBUDDY_PROVIDER_ID": "provider-1"}

	result, err := svc.InstallSquad(context.Background(), caller, id, input)
	if err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if result.SquadID != "capability-team" || result.LeaderAgentID != "capability-leader" {
		t.Fatalf("squad result = %+v, want Fleet capability IDs", result)
	}
	assertOnlyCapabilityInstall(t, ff)
	if ff.capabilityToken != "tok" || ff.capabilitySpace != "space-1" || ff.capabilityWorkspace != "ws-1" || ff.capabilityKey == "" {
		t.Fatalf("capability routing = token:%q space:%q workspace:%q key:%q", ff.capabilityToken, ff.capabilitySpace, ff.capabilityWorkspace, ff.capabilityKey)
	}
	definition := ff.capabilityInput.Definition
	team := definition.ExpertTeam
	if team == nil || team.Name != "Delivery Squad" || team.MarketExpertTeamID != id || team.Description != "ships features" ||
		team.Instructions != "1. Dispatch to owner\n2. Report status" || len(team.Members) != 3 {
		t.Fatalf("legacy squad content changed in capability definition: %+v", team)
	}
	wantMembers := map[string]struct {
		role   string
		leader bool
	}{
		"Planner": {role: "planner"},
		"Lead":    {role: "leader", leader: true},
		"Coder":   {role: "member"},
	}
	for _, member := range team.Members {
		want, ok := wantMembers[member.ExpertName]
		if !ok || member.Role != want.role || member.IsLeader != want.leader {
			t.Fatalf("squad member wiring = %+v, want role/leader for %q", member, member.ExpertName)
		}
	}
	experts := make(map[string]fleet.CapabilityExpert, len(definition.Experts))
	for _, expert := range definition.Experts {
		experts[expert.Name] = expert
	}
	if len(experts) != 3 {
		t.Fatalf("member experts = %+v", definition.Experts)
	}
	for name, wantInstruction := range map[string]string{
		"Planner": "plan things",
		"Lead":    "lead the team",
		"Coder":   "write code",
	} {
		expert := experts[name]
		if expert.Name != name || expert.Instructions != wantInstruction || expert.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider-1" {
			t.Fatalf("member expert %q lost its content or environment: %+v", name, expert)
		}
	}
	if string(experts["Lead"].MCPConfig) != `{"mcpServers":{}}` || len(experts["Planner"].SkillNames) != 1 || experts["Planner"].SkillNames[0] != "Research" {
		t.Fatalf("member MCP/skill relationships changed: %+v", experts)
	}
	if len(definition.Skills) != 1 || definition.Skills[0].Name != "Research" ||
		definition.Skills[0].Content != "# Research" || len(definition.Skills[0].Files) != 1 ||
		definition.Skills[0].Files[0].Path != "references/guide.md" ||
		definition.Skills[0].Files[0].Source.Type != "inline_text" ||
		definition.Skills[0].Files[0].Source.Content != "Keep supporting content" {
		t.Fatalf("legacy squad skill content changed: %+v", definition.Skills)
	}
	if len(ff.capabilityInput.Bindings.Experts) != 3 {
		t.Fatalf("runtime bindings = %+v", ff.capabilityInput.Bindings.Experts)
	}
	for _, binding := range ff.capabilityInput.Bindings.Experts {
		if _, ok := experts[binding.ExpertName]; !ok || binding.RuntimeID != "rt-1" {
			t.Fatalf("runtime binding = %+v", binding)
		}
	}

	wire := capabilityWireDefinition(t, ff.capabilityInput)
	requireNullVersion(t, capabilityWireObject(t, wire["expert_team"]))
	var wireExperts []json.RawMessage
	if err := json.Unmarshal(wire["experts"], &wireExperts); err != nil || len(wireExperts) != 3 {
		t.Fatalf("decode wire experts: %v (%s)", err, wire["experts"])
	}
	for _, raw := range wireExperts {
		requireNullVersion(t, capabilityWireObject(t, raw))
	}
	var wireSkills []json.RawMessage
	if err := json.Unmarshal(wire["skills"], &wireSkills); err != nil || len(wireSkills) != 1 {
		t.Fatalf("decode wire skills: %v (%s)", err, wire["skills"])
	}
	requireNullVersion(t, capabilityWireObject(t, wireSkills[0]))
}

func TestInstallSquadCapabilityFailureCreatesNoSeparateResources(t *testing.T) {
	wantErr := errors.New("capability transaction rejected")
	ff := &fakeFleet{capabilityErr: wantErr}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())

	_, err := svc.InstallSquad(context.Background(), caller, id, baseInput())
	if !errors.Is(err, wantErr) || !errors.Is(err, ErrFleetInstallUnavailable) {
		t.Fatalf("InstallSquad error = %v, want Fleet capability error marked unavailable", err)
	}
	assertOnlyCapabilityInstall(t, ff)
}
