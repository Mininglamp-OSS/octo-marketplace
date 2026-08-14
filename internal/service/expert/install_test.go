package expert

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// fakeFleet records the calls InstallExpert makes and lets a test inject
// per-method errors and the ids CreateSkill hands back in sequence.
type fakeFleet struct {
	agentID     string
	agentIDs    []string // when set, CreateAgent returns these in sequence (squad)
	agentIdx    int
	skillIDs    []string // returned by successive CreateSkill calls
	skillIdx    int
	agentErr    error
	failAgentAt int   // when agentIDs is set, CreateAgent fails at this index (-1 = never)
	skillErr    error // fails the CreateSkill at index failSkillAt
	failSkillAt int
	setErr      error
	fileErr     error // fails UpsertSkillFile

	agentSpec     fleet.AgentSpec
	createdSkills []fleet.SkillSpec
	upsertedFiles []upsertedFile
	boundAgent    string
	boundSkills   []string
	deletedAgents []string
	deletedSkills []string

	// Squad recorders + injectable errors (InstallSquad).
	squadID       string
	squadSpec     fleet.SquadSpec
	squadErr      error
	addedMembers  []fleet.SquadMemberSpec
	memberErr     error
	failMemberAt  int // index (over non-leader members) AddSquadMember fails at (-1 = never)
	memberIdx     int
	deletedSquads []string

	// onCreateSkill, when set, fires at the start of each CreateSkill call — a
	// test uses it to cancel the request context mid-install. The delete paths
	// record the ctx error they observed so a test can assert rollback ran on a
	// context detached from that cancellation.
	onCreateSkill      func()
	deleteAgentCtxErr  error
	deleteSkillCtxErrs []error
	deleteSquadCtxErr  error
}

type upsertedFile struct {
	skillID, path, content string
}

func (f *fakeFleet) CreateAgent(_ context.Context, _, _, _ string, spec fleet.AgentSpec) (string, error) {
	f.agentSpec = spec
	if len(f.agentIDs) > 0 {
		idx := f.agentIdx
		f.agentIdx++
		if f.agentErr != nil && idx == f.failAgentAt {
			return "", f.agentErr
		}
		if idx < len(f.agentIDs) {
			return f.agentIDs[idx], nil
		}
		return "agent-extra", nil
	}
	if f.agentErr != nil {
		return "", f.agentErr
	}
	return f.agentID, nil
}

func (f *fakeFleet) CreateSkill(_ context.Context, _, _, _ string, spec fleet.SkillSpec) (string, error) {
	if f.onCreateSkill != nil {
		f.onCreateSkill()
	}
	idx := f.skillIdx
	f.skillIdx++
	f.createdSkills = append(f.createdSkills, spec)
	if f.skillErr != nil && idx == f.failSkillAt {
		return "", f.skillErr
	}
	if idx < len(f.skillIDs) {
		return f.skillIDs[idx], nil
	}
	return "skill-extra", nil
}

func (f *fakeFleet) UpsertSkillFile(_ context.Context, _, _, _, skillID, path, content string) error {
	f.upsertedFiles = append(f.upsertedFiles, upsertedFile{skillID, path, content})
	return f.fileErr
}

func (f *fakeFleet) SetAgentSkills(_ context.Context, _, _, _, agentID string, skillIDs []string) error {
	f.boundAgent = agentID
	f.boundSkills = skillIDs
	return f.setErr
}

func (f *fakeFleet) DeleteAgent(ctx context.Context, _, _, _, agentID string) error {
	f.deleteAgentCtxErr = ctx.Err()
	f.deletedAgents = append(f.deletedAgents, agentID)
	return nil
}

func (f *fakeFleet) DeleteSkill(ctx context.Context, _, _, _, skillID string) error {
	f.deleteSkillCtxErrs = append(f.deleteSkillCtxErrs, ctx.Err())
	f.deletedSkills = append(f.deletedSkills, skillID)
	return nil
}

func (f *fakeFleet) CreateSquad(_ context.Context, _, _, _ string, spec fleet.SquadSpec) (string, error) {
	f.squadSpec = spec
	if f.squadErr != nil {
		return "", f.squadErr
	}
	return f.squadID, nil
}

func (f *fakeFleet) AddSquadMember(_ context.Context, _, _, _, _ string, m fleet.SquadMemberSpec) error {
	idx := f.memberIdx
	f.memberIdx++
	f.addedMembers = append(f.addedMembers, m)
	if f.memberErr != nil && idx == f.failMemberAt {
		return f.memberErr
	}
	return nil
}

func (f *fakeFleet) DeleteSquad(ctx context.Context, _, _, _, squadID string) error {
	f.deleteSquadCtxErr = ctx.Err()
	f.deletedSquads = append(f.deletedSquads, squadID)
	return nil
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

func baseInput() InstallInput {
	return InstallInput{WorkspaceID: "ws-1", RuntimeID: "rt-1", SpaceID: "space-1", Token: "tok"}
}

// recordingTracker records TrackInstall calls for asserting install counting.
type recordingTracker struct {
	installs []string // "resourceType/resourceID"
}

func (r *recordingTracker) TrackInstall(_ context.Context, resourceType, resourceID string) error {
	r.installs = append(r.installs, resourceType+"/"+resourceID)
	return nil
}

// A successful expert install bumps install_count exactly once, under
// resource_type "expert", even when the request is canceled right after the
// provision (detached tracking context). A failed install must not count.
func TestInstallExpertTracksInstall(t *testing.T) {
	ff := &fakeFleet{agentID: "agent-1", failSkillAt: -1}
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

	// Failure path: fleet errors → no count.
	ffFail := &fakeFleet{agentErr: errors.New("boom"), failSkillAt: -1}
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
	ff := &fakeFleet{agentID: "agent-1", skillIDs: []string{"s1", "s2"}, failSkillAt: -1}
	svc, caller, id := installFixture(t, ff, []model.SkillRef{
		{Name: "Research", ObjectKey: "k/0"},
		{Name: "Format", ObjectKey: "k/1"},
	})

	res, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	if res.AgentID != "agent-1" {
		t.Fatalf("AgentID = %q, want agent-1", res.AgentID)
	}
	if ff.agentSpec.Name != "Code Helper" || ff.agentSpec.Instructions != "You are a coding expert." || ff.agentSpec.RuntimeID != "rt-1" {
		t.Fatalf("agent spec = %#v", ff.agentSpec)
	}
	if string(ff.agentSpec.MCPConfig) != `{"mcpServers":{}}` {
		t.Fatalf("mcp_config = %q", ff.agentSpec.MCPConfig)
	}
	if len(ff.createdSkills) != 2 {
		t.Fatalf("created %d skills, want 2", len(ff.createdSkills))
	}
	if ff.boundAgent != "agent-1" || len(ff.boundSkills) != 2 || ff.boundSkills[0] != "s1" || ff.boundSkills[1] != "s2" {
		t.Fatalf("bind agent=%q skills=%#v", ff.boundAgent, ff.boundSkills)
	}
	if len(ff.deletedAgents) != 0 || len(ff.deletedSkills) != 0 {
		t.Fatalf("unexpected rollback: agents=%#v skills=%#v", ff.deletedAgents, ff.deletedSkills)
	}
}

func TestInstallExpertNoSkillsSkipsBinding(t *testing.T) {
	ff := &fakeFleet{agentID: "agent-1", failSkillAt: -1}
	svc, caller, id := installFixture(t, ff, nil)

	res, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	if res.AgentID != "agent-1" {
		t.Fatalf("AgentID = %q", res.AgentID)
	}
	if len(ff.createdSkills) != 0 {
		t.Fatalf("created skills = %#v, want none", ff.createdSkills)
	}
	if ff.boundAgent != "" {
		t.Fatalf("SetAgentSkills should not be called with no skills")
	}
}

func TestInstallExpertSkipsNameOnlySkills(t *testing.T) {
	ff := &fakeFleet{agentID: "agent-1", skillIDs: []string{"s1"}, failSkillAt: -1}
	svc, caller, id := installFixture(t, ff, []model.SkillRef{
		{Name: "Named only"},             // no ObjectKey → skipped
		{Name: "Real", ObjectKey: "k/1"}, // installed
	})

	if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	if len(ff.createdSkills) != 1 || ff.createdSkills[0].Name != "Real" {
		t.Fatalf("created skills = %#v, want only Real", ff.createdSkills)
	}
}

func TestInstallExpertRollsBackOnSkillFailure(t *testing.T) {
	ff := &fakeFleet{
		agentID:     "agent-1",
		skillIDs:    []string{"s1"},
		skillErr:    errors.New("boom"),
		failSkillAt: 1, // first skill ok (s1), second fails
	}
	svc, caller, id := installFixture(t, ff, []model.SkillRef{
		{Name: "First", ObjectKey: "k/0"},
		{Name: "Second", ObjectKey: "k/1"},
	})

	_, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if err == nil {
		t.Fatal("expected error on skill failure")
	}
	if len(ff.deletedSkills) != 1 || ff.deletedSkills[0] != "s1" {
		t.Fatalf("deleted skills = %#v, want [s1]", ff.deletedSkills)
	}
	if len(ff.deletedAgents) != 1 || ff.deletedAgents[0] != "agent-1" {
		t.Fatalf("deleted agents = %#v, want [agent-1]", ff.deletedAgents)
	}
}

func TestInstallExpertRollsBackOnBindFailure(t *testing.T) {
	ff := &fakeFleet{agentID: "agent-1", skillIDs: []string{"s1"}, failSkillAt: -1, setErr: errors.New("bind failed")}
	svc, caller, id := installFixture(t, ff, []model.SkillRef{{Name: "Only", ObjectKey: "k/0"}})

	if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err == nil {
		t.Fatal("expected error on bind failure")
	}
	if len(ff.deletedSkills) != 1 || len(ff.deletedAgents) != 1 {
		t.Fatalf("rollback incomplete: skills=%#v agents=%#v", ff.deletedSkills, ff.deletedAgents)
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

func TestInstallExpertRejectsMissingTarget(t *testing.T) {
	ff := &fakeFleet{agentID: "agent-1", failSkillAt: -1}
	svc, caller, id := installFixture(t, ff, nil)
	_, err := svc.InstallExpert(context.Background(), caller, id, InstallInput{RuntimeID: "rt-1", Token: "tok"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestInstallExpertHiddenExpertNotFound(t *testing.T) {
	ff := &fakeFleet{agentID: "agent-1", failSkillAt: -1}
	svc, _, id := installFixture(t, ff, nil)
	// A caller in a different space cannot see the public expert.
	other := Caller{UID: "stranger", SpaceID: "space-2"}
	_, err := svc.InstallExpert(context.Background(), other, id, baseInput())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if ff.agentSpec.Name != "" {
		t.Fatalf("fleet should not be called for a hidden expert")
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

// A packaged skill's supporting text files are attached via UpsertSkillFile;
// SKILL.md and binary (non-UTF-8) files are not.
func TestInstallExpertAttachesSupportingTextFiles(t *testing.T) {
	ff := &fakeFleet{agentID: "agent-1", skillIDs: []string{"s1"}, failSkillAt: -1}
	store := newFakeStore()
	obj := newMemObjectStore()
	const expertID = "exp-files"
	mdKey := "k/skill/SKILL.md"
	zipKey := "k/skill/skill.zip"
	store.experts[expertID] = &model.Expert{
		ID: expertID, Name: "Packager", Summary: "s", Instruction: "i",
		Visibility: model.VisibilityPublic, SpaceID: "space-1", OwnerUID: "o",
		Skills: []model.SkillRef{{Name: "packaged", ObjectKey: mdKey, ZipObjectKey: zipKey}},
	}
	obj.objects[mdKey] = []byte("# packaged")
	obj.objects[zipKey] = makeSkillZip(t, map[string][]byte{
		"SKILL.md":     []byte("# packaged"),                       // reserved → skipped
		"reference.md": []byte("# Reference\nnotes"),               // text → attached
		"logo.png":     {0x89, 0x50, 0x4e, 0x47, 0x00, 0xff, 0xfe}, // binary → skipped
	})
	svc := New(store, obj, func() string { return "gen" }).WithFleet(ff)

	res, err := svc.InstallExpert(context.Background(), Caller{UID: "me", SpaceID: "space-1"}, expertID, baseInput())
	if err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	if res.AgentID != "agent-1" {
		t.Fatalf("AgentID = %q", res.AgentID)
	}
	if len(ff.upsertedFiles) != 1 {
		t.Fatalf("upserted %d files, want 1 (reference.md only): %#v", len(ff.upsertedFiles), ff.upsertedFiles)
	}
	got := ff.upsertedFiles[0]
	if got.skillID != "s1" || got.path != "reference.md" || got.content != "# Reference\nnotes" {
		t.Fatalf("unexpected upserted file: %#v", got)
	}
}

// A failing UpsertSkillFile rolls back the created skill and agent.
func TestInstallExpertRollsBackOnFileFailure(t *testing.T) {
	ff := &fakeFleet{agentID: "agent-1", skillIDs: []string{"s1"}, failSkillAt: -1, fileErr: errors.New("file boom")}
	store := newFakeStore()
	obj := newMemObjectStore()
	const expertID = "exp-files2"
	mdKey, zipKey := "k2/SKILL.md", "k2/skill.zip"
	store.experts[expertID] = &model.Expert{
		ID: expertID, Name: "P2", Summary: "s", Instruction: "i",
		Visibility: model.VisibilityPublic, SpaceID: "space-1", OwnerUID: "o",
		Skills: []model.SkillRef{{Name: "packaged", ObjectKey: mdKey, ZipObjectKey: zipKey}},
	}
	obj.objects[mdKey] = []byte("# p2")
	obj.objects[zipKey] = makeSkillZip(t, map[string][]byte{
		"SKILL.md":     []byte("# p2"),
		"reference.md": []byte("notes"),
	})
	svc := New(store, obj, func() string { return "gen" }).WithFleet(ff)

	if _, err := svc.InstallExpert(context.Background(), Caller{UID: "me", SpaceID: "space-1"}, expertID, baseInput()); err == nil {
		t.Fatal("expected error on file upsert failure")
	}
	if len(ff.deletedSkills) != 1 || ff.deletedSkills[0] != "s1" {
		t.Fatalf("deleted skills = %#v, want [s1]", ff.deletedSkills)
	}
	if len(ff.deletedAgents) != 1 || ff.deletedAgents[0] != "agent-1" {
		t.Fatalf("deleted agents = %#v, want [agent-1]", ff.deletedAgents)
	}
}

// When the install fails *because* the request context was canceled mid-flight
// (client disconnect / write-timeout), the rollback must still delete the
// created skill + agent — on a context detached from that cancellation, not the
// dead request context (which would make every cleanup delete a no-op and leave
// exactly the orphaned resources rollback exists to prevent).
func TestInstallExpertRollbackRunsAfterContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ff := &fakeFleet{
		agentID:     "agent-1",
		skillIDs:    []string{"s1"},
		skillErr:    context.Canceled,
		failSkillAt: 1, // first skill ok (s1), second fails
	}
	// Cancel the request context the moment the second CreateSkill runs, then
	// have that call fail — mimicking the request dying mid-install.
	calls := 0
	ff.onCreateSkill = func() {
		calls++
		if calls == 2 {
			cancel()
		}
	}

	svc, caller, id := installFixture(t, ff, []model.SkillRef{
		{Name: "First", ObjectKey: "k/0"},
		{Name: "Second", ObjectKey: "k/1"},
	})

	if _, err := svc.InstallExpert(ctx, caller, id, baseInput()); err == nil {
		t.Fatal("expected error on canceled install")
	}
	if len(ff.deletedSkills) != 1 || ff.deletedSkills[0] != "s1" {
		t.Fatalf("deleted skills = %#v, want [s1]", ff.deletedSkills)
	}
	if len(ff.deletedAgents) != 1 || ff.deletedAgents[0] != "agent-1" {
		t.Fatalf("deleted agents = %#v, want [agent-1]", ff.deletedAgents)
	}
	// The detached cleanup context must be live, not carrying the canceled
	// request's error.
	if ff.deleteAgentCtxErr != nil {
		t.Fatalf("DeleteAgent ran on canceled ctx (%v); want detached live ctx", ff.deleteAgentCtxErr)
	}
	for i, e := range ff.deleteSkillCtxErrs {
		if e != nil {
			t.Fatalf("DeleteSkill[%d] ran on canceled ctx (%v); want detached live ctx", i, e)
		}
	}
}

// fileBudget caps the aggregate supporting-file fan-out per install.
func TestFileBudgetTake(t *testing.T) {
	b := &fileBudget{remaining: 2}
	if !b.take() || !b.take() {
		t.Fatal("first two takes should succeed")
	}
	if b.take() {
		t.Fatal("third take should fail once budget is exhausted")
	}
	// A nil budget is unbounded (defensive: no caller passes nil today).
	var nb *fileBudget
	if !nb.take() {
		t.Fatal("nil budget should be unbounded")
	}
}

// A package whose supporting files exceed the remaining install budget makes
// attachSkillFiles fail with ErrInstallTooLarge, so the install rolls back
// rather than fanning out unbounded UpsertSkillFile calls.
func TestAttachSkillFilesRejectsWhenBudgetExhausted(t *testing.T) {
	ff := &fakeFleet{}
	store := newFakeStore()
	obj := newMemObjectStore()
	zipKey := "k/skill.zip"
	obj.objects[zipKey] = makeSkillZip(t, map[string][]byte{
		"SKILL.md":     []byte("# s"),
		"reference.md": []byte("notes"), // one supporting text file
	})
	svc := New(store, obj, func() string { return "gen" }).WithFleet(ff)

	ref := model.SkillRef{Name: "packaged", ObjectKey: "k/SKILL.md", ZipObjectKey: zipKey}
	err := svc.attachSkillFiles(context.Background(), baseInput(), ref, "s1", &fileBudget{remaining: 0})
	if !errors.Is(err, ErrInstallTooLarge) {
		t.Fatalf("err = %v, want ErrInstallTooLarge", err)
	}
	if len(ff.upsertedFiles) != 0 {
		t.Fatalf("no files should be upserted once the budget is exhausted, got %#v", ff.upsertedFiles)
	}
}
