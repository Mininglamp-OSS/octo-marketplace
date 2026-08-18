package expert

import (
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// installSquadFixture wires a Service over an in-memory store holding one squad
// with the given members, and returns the caller that can see it.
func installSquadFixture(t *testing.T, fleetClient FleetProvisioner, members []model.SquadMember) (*Service, Caller, string) {
	return installSquadFixtureStrategies(t, fleetClient, members, nil)
}

// installSquadFixtureStrategies is installSquadFixture with squad-level
// dispatch strategies seeded (the instructions-write path).
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

// threeMembers is a leader (index 1) flanked by two workers, exercising the
// "skip the leader when attaching members" and role-passthrough paths.
func threeMembers() []model.SquadMember {
	return []model.SquadMember{
		{MemberKey: "member_01", Name: "Planner", Role: "planner", Instruction: "plan things"},
		{MemberKey: "member_02", Name: "Lead", Role: "lead", IsLeader: true, Instruction: "lead the team", MCPConfig: `{"mcpServers":{}}`},
		{MemberKey: "member_03", Name: "Coder", Instruction: "write code"}, // no role → defaults to "member"
	}
}

// A successful squad install counts once under resource_type "squad" — and
// never counts the member experts, which are snapshots inside the squad. A
// failed squad install must not count.
func TestInstallSquadTracksInstallOnlyForSquad(t *testing.T) {
	ff := &fakeFleet{
		agentIDs:     []string{"a0", "a1", "a2"},
		failAgentAt:  -1,
		failSkillAt:  -1,
		failMemberAt: -1,
		squadID:      "squad-x",
	}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())
	tracker := &recordingTracker{}
	svc = svc.WithMetrics(tracker)

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if len(tracker.installs) != 1 || tracker.installs[0] != "squad/"+id {
		t.Fatalf("installs = %v, want exactly [squad/%s] (no member expert counts)", tracker.installs, id)
	}

	// Failure path: squad formation fails → rollback, no count.
	ffFail := &fakeFleet{
		agentIDs:     []string{"a0", "a1", "a2"},
		failAgentAt:  -1,
		failSkillAt:  -1,
		failMemberAt: -1,
		squadErr:     errors.New("boom"),
	}
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
	ff := &fakeFleet{
		agentIDs:     []string{"a0", "a1", "a2"},
		failAgentAt:  -1,
		failSkillAt:  -1,
		failMemberAt: -1,
		squadID:      "squad-x",
	}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())

	res, err := svc.InstallSquad(context.Background(), caller, id, baseInput())
	if err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if res.SquadID != "squad-x" || res.LeaderAgentID != "a1" {
		t.Fatalf("result = %#v, want {squad-x, a1}", res)
	}
	if ff.agentIdx != 3 {
		t.Fatalf("created %d agents, want 3", ff.agentIdx)
	}
	// The squad is formed led by the leader member's agent.
	if ff.squadSpec.LeaderAgentID != "a1" || ff.squadSpec.Name != "Delivery Squad" {
		t.Fatalf("squad spec = %#v", ff.squadSpec)
	}
	// Only the two non-leader members are attached (leader auto-added by fleet).
	if len(ff.addedMembers) != 2 {
		t.Fatalf("added %d members, want 2: %#v", len(ff.addedMembers), ff.addedMembers)
	}
	if m := ff.addedMembers[0]; m.MemberType != "agent" || m.MemberID != "a0" || m.Role != "planner" {
		t.Fatalf("member[0] = %#v", ff.addedMembers[0])
	}
	// Empty member role defaults to "member".
	if m := ff.addedMembers[1]; m.MemberType != "agent" || m.MemberID != "a2" || m.Role != "member" {
		t.Fatalf("member[1] = %#v", ff.addedMembers[1])
	}
	if len(ff.deletedAgents) != 0 || len(ff.deletedSquads) != 0 {
		t.Fatalf("unexpected rollback: agents=%#v squads=%#v", ff.deletedAgents, ff.deletedSquads)
	}
}

func TestInstallSquadSkipsDuplicateSkillNamesAcrossMembers(t *testing.T) {
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
				{Name: " shared skill ", ObjectKey: "members/2/shared"},
				{Name: "Coder Only", ObjectKey: "members/2/only"},
			},
		},
	}
	ff := &fakeFleet{
		agentIDs:     []string{"a0", "a1"},
		skillIDs:     []string{"shared", "planner", "coder"},
		failAgentAt:  -1,
		failSkillAt:  -1,
		failMemberAt: -1,
		squadID:      "squad-x",
	}
	svc, caller, id := installSquadFixtureStrategies(t, ff, members, []string{"先分析", "再执行"})

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if len(ff.createdSkills) != 3 {
		t.Fatalf("created skills = %#v, want three unique names", ff.createdSkills)
	}
	wantNames := []string{"Shared Skill", "Planner Only", "Coder Only"}
	for i, want := range wantNames {
		if ff.createdSkills[i].Name != want {
			t.Fatalf("createdSkills[%d].Name = %q, want %q", i, ff.createdSkills[i].Name, want)
		}
	}
	if got := ff.bindings["a0"]; len(got) != 2 || got[0] != "shared" || got[1] != "planner" {
		t.Fatalf("leader bindings = %#v, want [shared planner]", got)
	}
	if got := ff.bindings["a1"]; len(got) != 1 || got[0] != "coder" {
		t.Fatalf("second member bindings = %#v, want [coder]", got)
	}
	if ff.instrCalls != 1 || ff.instrValue != "1. 先分析\n2. 再执行" {
		t.Fatalf("instruction update calls=%d value=%q", ff.instrCalls, ff.instrValue)
	}
}

// The squad's dispatch strategies must land in fleet as the squad's
// instructions — one numbered line per rule, blank rules skipped.
func TestInstallSquadWritesStrategiesAsInstructions(t *testing.T) {
	ff := &fakeFleet{
		agentIDs:     []string{"a0", "a1", "a2"},
		failAgentAt:  -1,
		failSkillAt:  -1,
		failMemberAt: -1,
		squadID:      "squad-x",
	}
	strategies := []string{"先分析需求", "  ", "再分派给成员"}
	svc, caller, id := installSquadFixtureStrategies(t, ff, threeMembers(), strategies)

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if ff.instrCalls != 1 || ff.instrSquadID != "squad-x" {
		t.Fatalf("instructions calls = %d for %q, want 1 for squad-x", ff.instrCalls, ff.instrSquadID)
	}
	if want := "1. 先分析需求\n2. 再分派给成员"; ff.instrValue != want {
		t.Fatalf("instructions = %q, want %q", ff.instrValue, want)
	}
}

func TestInstallSquadSkipsInstructionsWithoutStrategies(t *testing.T) {
	ff := &fakeFleet{
		agentIDs:     []string{"a0", "a1", "a2"},
		failAgentAt:  -1,
		failSkillAt:  -1,
		failMemberAt: -1,
		squadID:      "squad-x",
	}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if ff.instrCalls != 0 {
		t.Fatalf("instructions calls = %d, want 0 (no strategies)", ff.instrCalls)
	}
}

func TestInstallSquadRollsBackOnInstructionsFailure(t *testing.T) {
	ff := &fakeFleet{
		agentIDs:     []string{"a0", "a1", "a2"},
		failAgentAt:  -1,
		failSkillAt:  -1,
		failMemberAt: -1,
		squadID:      "squad-x",
		instrErr:     errors.New("instructions boom"),
	}
	svc, caller, id := installSquadFixtureStrategies(t, ff, threeMembers(), []string{"规则一"})

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err == nil {
		t.Fatal("expected error on instructions-write failure")
	}
	if len(ff.deletedSquads) != 1 || ff.deletedSquads[0] != "squad-x" {
		t.Fatalf("deleted squads = %#v, want [squad-x]", ff.deletedSquads)
	}
	if len(ff.deletedAgents) != 3 {
		t.Fatalf("deleted agents = %#v, want all 3", ff.deletedAgents)
	}
	if len(ff.addedMembers) != 0 {
		t.Fatalf("no members should be attached: %#v", ff.addedMembers)
	}
}

func TestInstallSquadDefaultsLeaderToFirstMember(t *testing.T) {
	members := []model.SquadMember{
		{MemberKey: "member_01", Name: "First", Role: "a", Instruction: "i"},
		{MemberKey: "member_02", Name: "Second", Role: "b", Instruction: "i"},
	}
	ff := &fakeFleet{agentIDs: []string{"a0", "a1"}, failAgentAt: -1, failSkillAt: -1, failMemberAt: -1, squadID: "sq"}
	svc, caller, id := installSquadFixture(t, ff, members)

	res, err := svc.InstallSquad(context.Background(), caller, id, baseInput())
	if err != nil {
		t.Fatalf("InstallSquad: %v", err)
	}
	if res.LeaderAgentID != "a0" || ff.squadSpec.LeaderAgentID != "a0" {
		t.Fatalf("leader = %q, want a0 (first member)", res.LeaderAgentID)
	}
	if len(ff.addedMembers) != 1 || ff.addedMembers[0].MemberID != "a1" {
		t.Fatalf("added members = %#v, want only a1", ff.addedMembers)
	}
}

func TestInstallSquadRollsBackEarlierAgentsOnMemberProvisionFailure(t *testing.T) {
	ff := &fakeFleet{
		agentIDs:    []string{"a0", "a1", "a2"},
		agentErr:    errors.New("create boom"),
		failAgentAt: 1, // first member ok (a0), second CreateAgent fails
		failSkillAt: -1,
	}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err == nil {
		t.Fatal("expected error when a member fails to provision")
	}
	// The earlier member agent is unwound; no squad was ever created.
	if len(ff.deletedAgents) != 1 || ff.deletedAgents[0] != "a0" {
		t.Fatalf("deleted agents = %#v, want [a0]", ff.deletedAgents)
	}
	if len(ff.deletedSquads) != 0 {
		t.Fatalf("no squad should be created/deleted: %#v", ff.deletedSquads)
	}
	if ff.squadSpec.LeaderAgentID != "" {
		t.Fatalf("CreateSquad should not be called: %#v", ff.squadSpec)
	}
}

func TestInstallSquadRollsBackAllAgentsOnCreateSquadFailure(t *testing.T) {
	ff := &fakeFleet{
		agentIDs:    []string{"a0", "a1", "a2"},
		failAgentAt: -1,
		failSkillAt: -1,
		squadErr:    errors.New("squad boom"),
	}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err == nil {
		t.Fatal("expected error on CreateSquad failure")
	}
	if len(ff.deletedAgents) != 3 {
		t.Fatalf("deleted agents = %#v, want all 3", ff.deletedAgents)
	}
	if len(ff.deletedSquads) != 0 {
		t.Fatalf("squad create failed, nothing to delete: %#v", ff.deletedSquads)
	}
	if len(ff.addedMembers) != 0 {
		t.Fatalf("no members should be attached: %#v", ff.addedMembers)
	}
}

func TestInstallSquadRollsBackSquadAndAgentsOnAddMemberFailure(t *testing.T) {
	ff := &fakeFleet{
		agentIDs:     []string{"a0", "a1", "a2"},
		failAgentAt:  -1,
		failSkillAt:  -1,
		squadID:      "squad-x",
		memberErr:    errors.New("member boom"),
		failMemberAt: 0, // first AddSquadMember fails
	}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())

	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err == nil {
		t.Fatal("expected error on AddSquadMember failure")
	}
	if len(ff.deletedSquads) != 1 || ff.deletedSquads[0] != "squad-x" {
		t.Fatalf("deleted squads = %#v, want [squad-x]", ff.deletedSquads)
	}
	if len(ff.deletedAgents) != 3 {
		t.Fatalf("deleted agents = %#v, want all 3", ff.deletedAgents)
	}
	// Rollback deletes must run on a live (detached) context, not a dead one.
	if ff.deleteSquadCtxErr != nil {
		t.Fatalf("DeleteSquad ran on canceled ctx (%v); want detached live ctx", ff.deleteSquadCtxErr)
	}
}

func TestInstallSquadEmptyMembersIsInvalid(t *testing.T) {
	ff := &fakeFleet{failAgentAt: -1, failSkillAt: -1}
	svc, caller, id := installSquadFixture(t, ff, nil)
	if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if ff.agentIdx != 0 {
		t.Fatal("fleet should not be called for a memberless squad")
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
	ff := &fakeFleet{failAgentAt: -1, failSkillAt: -1}
	svc, caller, id := installSquadFixture(t, ff, threeMembers())
	_, err := svc.InstallSquad(context.Background(), caller, id, InstallInput{RuntimeID: "rt-1", Token: "tok"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestInstallSquadHiddenSquadNotFound(t *testing.T) {
	ff := &fakeFleet{failAgentAt: -1, failSkillAt: -1}
	svc, _, id := installSquadFixture(t, ff, threeMembers())
	other := Caller{UID: "stranger", SpaceID: "space-2"} // different space cannot see it
	if _, err := svc.InstallSquad(context.Background(), other, id, baseInput()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if ff.agentIdx != 0 {
		t.Fatal("fleet should not be called for a hidden squad")
	}
}
