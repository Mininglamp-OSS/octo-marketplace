package expert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// errBudgetReadGuard fails a regression run as soon as legacy assembly reads
// past the last Skill whose 2 MiB content can still fit the aggregate request
// budget. It prevents the pre-fix code from retaining ~1.2 GiB in the test.
var errBudgetReadGuard = errors.New("budget regression: skill read past aggregate limit")

// guardedReadStore serves one shared Skill object and trips the guard instead
// of letting the old implementation keep accumulating content forever.
type guardedReadStore struct {
	*memObjectStore
	content []byte
	limit   int
	reads   int
}

func (s *guardedReadStore) GetObject(_ context.Context, _ string) (io.ReadCloser, error) {
	s.reads++
	if s.reads > s.limit {
		return nil, errBudgetReadGuard
	}
	return io.NopCloser(bytes.NewReader(s.content)), nil
}

// maxSquadSkills builds the largest squad-shaped reference graph: every member
// carries 20 uniquely named Skills that all point at one shared object.
func maxSquadSkills() []model.SquadMember {
	members := make([]model.SquadMember, 0, model.MaxSquadMembers)
	for mi := range model.MaxSquadMembers {
		member := model.SquadMember{
			MemberKey:   fmt.Sprintf("member_%02d", mi+1),
			Name:        fmt.Sprintf("Member %02d", mi+1),
			Role:        "worker",
			Instruction: "do the assigned work",
		}
		for si := range model.MaxExpertSkills {
			member.Skills = append(member.Skills, model.SkillRef{
				Name:      fmt.Sprintf("Skill %02d-%02d", mi, si),
				ObjectKey: "shared/max-squad-skill",
			})
		}
		members = append(members, member)
	}
	members[0].IsLeader = true
	return members
}

// A max-sized squad must stop reading before the aggregate request can exceed
// its byte budget; the ninth 2 MiB read is already over budget once JSON
// metadata is included.
func TestInstallSquadRejectsOverBudgetRequestBeforeNinthSkillRead(t *testing.T) {
	ff := &fakeFleet{}
	svc, caller, id := installSquadFixture(t, ff, maxSquadSkills())
	guard := &guardedReadStore{
		memObjectStore: svc.store.(*memObjectStore),
		content:        []byte(strings.Repeat("a", int(maxSkillReadBytes))),
		limit:          8,
	}
	svc.store = guard
	tracker := &recordingTracker{}
	svc = svc.WithMetrics(tracker)

	_, err := svc.InstallSquad(context.Background(), caller, id, baseInput())
	if !errors.Is(err, ErrInstallTooLarge) {
		t.Fatalf("InstallSquad error = %v, want ErrInstallTooLarge", err)
	}
	if errors.Is(err, errBudgetReadGuard) {
		t.Fatal("InstallSquad reached the ninth skill read instead of rejecting on the aggregate budget")
	}
	if guard.reads > 8 {
		t.Fatalf("skill reads = %d, want at most 8 before rejecting", guard.reads)
	}
	if ff.capabilityCalls != 0 {
		t.Fatalf("Fleet capability calls = %d, want no request for an oversized install", ff.capabilityCalls)
	}
	if len(tracker.installs) != 0 {
		t.Fatalf("oversized squad was tracked as installed: %v", tracker.installs)
	}
}

// A single expert whose raw Skill bytes fit but whose JSON-escaped content does
// not must be rejected; escaping is part of the outgoing request size.
func TestInstallExpertRejectsOverBudgetJSONEscaping(t *testing.T) {
	const objectKey = "shared/escaped-skill"
	skills := make([]model.SkillRef, 0, 5)
	for i := range 5 {
		skills = append(skills, model.SkillRef{
			Name:      fmt.Sprintf("Escaped %d", i),
			ObjectKey: objectKey,
		})
	}
	ff := &fakeFleet{}
	svc, caller, id := installFixture(t, ff, skills)
	svc.store.(*memObjectStore).objects[objectKey] = []byte(strings.Repeat("\"", int(maxSkillReadBytes)))
	tracker := &recordingTracker{}
	svc = svc.WithMetrics(tracker)

	_, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if !errors.Is(err, ErrInstallTooLarge) {
		t.Fatalf("InstallExpert error = %v, want ErrInstallTooLarge", err)
	}
	if ff.capabilityCalls != 0 {
		t.Fatalf("Fleet capability calls = %d, want no request for an oversized install", ff.capabilityCalls)
	}
	if len(tracker.installs) != 0 {
		t.Fatalf("oversized expert was tracked as installed: %v", tracker.installs)
	}
}

// Supporting files are part of the same aggregate request budget.
func TestInstallExpertRejectsOverBudgetSupportingFiles(t *testing.T) {
	files := make([]model.SkillFile, 0, 17)
	for i := range 17 {
		files = append(files, model.SkillFile{
			Path:    fmt.Sprintf("references/%02d.md", i),
			Content: strings.Repeat("a", model.MaxSkillContentBytes),
		})
	}
	ff := &fakeFleet{}
	svc, caller, id := installFixture(t, ff, []model.SkillRef{{
		Name:            "Tree Skill",
		Markdown:        "# Tree Skill",
		SupportingFiles: files,
	}})
	tracker := &recordingTracker{}
	svc = svc.WithMetrics(tracker)

	_, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if !errors.Is(err, ErrInstallTooLarge) {
		t.Fatalf("InstallExpert error = %v, want ErrInstallTooLarge", err)
	}
	if ff.capabilityCalls != 0 {
		t.Fatalf("Fleet capability calls = %d, want no request for an oversized install", ff.capabilityCalls)
	}
	if len(tracker.installs) != 0 {
		t.Fatalf("oversized expert was tracked as installed: %v", tracker.installs)
	}
}

// A valid ASCII request below the aggregate budget must still install; the
// budget may not apply a blanket multiplier to text.
func TestInstallExpertAllowsValidCapabilityRequestBelowLimit(t *testing.T) {
	const objectKey = "shared/valid-skill"
	skills := make([]model.SkillRef, 0, 7)
	for i := range 7 {
		skills = append(skills, model.SkillRef{
			Name:      fmt.Sprintf("Valid %d", i),
			ObjectKey: objectKey,
		})
	}
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "agent-1"}}
	svc, caller, id := installFixture(t, ff, skills)
	svc.store.(*memObjectStore).objects[objectKey] = []byte(strings.Repeat("a", int(maxSkillReadBytes)))

	if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatalf("InstallExpert: %v", err)
	}
	assertOnlyCapabilityInstall(t, ff)
	if got := len(ff.capabilityInput.Definition.Skills); got != 7 {
		t.Fatalf("installed skills = %d, want 7", got)
	}
}

// Legal metadata that escapes larger plus near-budget Skill bodies must be
// rejected from the encoded size, not the raw text size.
func TestInstallExpertRejectsOverBudgetMetadataEscaping(t *testing.T) {
	const objectKey = "shared/metadata-skill"
	skills := make([]model.SkillRef, 0, 14)
	for i := range 14 {
		skills = append(skills, model.SkillRef{
			Name:      fmt.Sprintf("Metadata %d", i),
			ObjectKey: objectKey,
		})
	}
	ff := &fakeFleet{}
	svc, caller, id := installFixture(t, ff, skills)
	svc.repo.(*fakeStore).experts[id].Instruction = strings.Repeat("\"", model.MaxSkillContentBytes)
	svc.store.(*memObjectStore).objects[objectKey] = []byte(strings.Repeat("a", model.MaxSkillContentBytes))
	tracker := &recordingTracker{}
	svc = svc.WithMetrics(tracker)

	_, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if !errors.Is(err, ErrInstallTooLarge) {
		t.Fatalf("InstallExpert error = %v, want ErrInstallTooLarge", err)
	}
	if ff.capabilityCalls != 0 {
		t.Fatalf("Fleet capability calls = %d, want no request for an oversized install", ff.capabilityCalls)
	}
	if len(tracker.installs) != 0 {
		t.Fatalf("oversized expert was tracked as installed: %v", tracker.installs)
	}
}

// Member metadata counts toward the same budget as Skill bodies: 30 legal
// 40 KiB instructions plus the first member's seven 2 MiB ASCII Skills fit raw
// but exceed the encoded request limit.
func TestInstallSquadMemberMetadataCountsTowardBudget(t *testing.T) {
	const objectKey = "shared/member-skill"
	instruction := strings.Repeat("\"", 40<<10)
	members := make([]model.SquadMember, 0, model.MaxSquadMembers)
	for i := range model.MaxSquadMembers {
		member := model.SquadMember{
			MemberKey:   fmt.Sprintf("member_%02d", i+1),
			Name:        fmt.Sprintf("Member %02d", i+1),
			Role:        "worker",
			Instruction: instruction,
		}
		if i == 0 {
			for j := range 7 {
				member.Skills = append(member.Skills, model.SkillRef{
					Name:      fmt.Sprintf("Member skill %d", j),
					ObjectKey: objectKey,
				})
			}
		}
		members = append(members, member)
	}
	members[0].IsLeader = true
	ff := &fakeFleet{}
	svc, caller, id := installSquadFixture(t, ff, members)
	guard := &guardedReadStore{
		memObjectStore: svc.store.(*memObjectStore),
		content:        []byte(strings.Repeat("a", int(maxSkillReadBytes))),
		limit:          7,
	}
	svc.store = guard
	tracker := &recordingTracker{}
	svc = svc.WithMetrics(tracker)

	_, err := svc.InstallSquad(context.Background(), caller, id, baseInput())
	if !errors.Is(err, ErrInstallTooLarge) {
		t.Fatalf("InstallSquad error = %v, want ErrInstallTooLarge", err)
	}
	if guard.reads > 7 {
		t.Fatalf("skill reads = %d, want at most 7 before rejecting", guard.reads)
	}
	if ff.capabilityCalls != 0 {
		t.Fatalf("Fleet capability calls = %d, want no request for an oversized install", ff.capabilityCalls)
	}
	if len(tracker.installs) != 0 {
		t.Fatalf("oversized squad was tracked as installed: %v", tracker.installs)
	}
}

// escapedBudgetSkillName is a legal Skill name (well under the existing
// 128-character limit) whose JSON form is longer than its raw text: quotes, a
// backslash, and HTML-sensitive characters all force escape sequences, so a
// meter that charges raw name lengths under-counts the outgoing request.
func escapedBudgetSkillName(i int) string {
	return fmt.Sprintf(`Skill %02d "quoted" <tag> & \path`, i)
}

// wireCapabilityNames is the decoded wire form of the captured request. Name
// and body assertions run against the serialized bytes Fleet would receive, so
// a field dropped during assembly cannot leave the boundary measurement
// self-consistent.
type wireCapabilityNames struct {
	Definition struct {
		Skills []struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"skills"`
		Experts []struct {
			SkillNames []string `json:"skill_names"`
		} `json:"experts"`
	} `json:"definition"`
}

// capabilityPayload serializes the captured request with standard JSON — the
// same bytes the real client puts on the wire — and returns its exact size plus
// the decoded names and bodies. Expected sizes never come from the assembly
// meter under test.
func capabilityPayload(t *testing.T, in fleet.CapabilityInstallRequest) (int, wireCapabilityNames) {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal captured capability request: %v", err)
	}
	var wire wireCapabilityNames
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode captured capability request: %v", err)
	}
	return len(body), wire
}

// budgetBoundaryBody picks the single Skill body that lands the next payload
// exactly on the request limit. ASCII body bytes encode 1:1, so the delta from
// the measured probe payload is exact; keeping one byte of headroom under the
// per-Skill content cap makes the rejected run fail on the aggregate budget
// alone.
func budgetBoundaryBody(t *testing.T, probeBody, probeSize int) int {
	t.Helper()
	if probeSize > fleet.MaxCapabilityRequestBytes {
		t.Fatalf("probe payload = %d bytes, want at most %d", probeSize, fleet.MaxCapabilityRequestBytes)
	}
	body := probeBody + fleet.MaxCapabilityRequestBytes - probeSize
	if body+1 > model.MaxSkillContentBytes {
		t.Fatalf("calibrated Skill body = %d bytes, want single-byte headroom under %d", body, model.MaxSkillContentBytes)
	}
	return body
}

// budgetAttempt runs one install with the given boundary Skill body and returns
// the provisioner plus the standard JSON size of what it received.
type budgetAttempt func(bodyLen int) (*fakeFleet, int, wireCapabilityNames, error)

// assertOneByteOverBudget is the other half of the boundary contract: the
// payload that is one wire byte past the limit must be rejected by the install
// call itself, before Fleet sees any request.
func assertOneByteOverBudget(t *testing.T, install budgetAttempt, boundaryBody int) {
	t.Helper()
	overFleet, _, _, err := install(boundaryBody + 1)
	if !errors.Is(err, ErrInstallTooLarge) {
		t.Fatalf("install one wire byte over the limit: err = %v, want ErrInstallTooLarge", err)
	}
	if overFleet.capabilityCalls != 0 {
		t.Fatalf("Fleet capability calls = %d, want no dispatch past the budget", overFleet.capabilityCalls)
	}
}

// The final serialized request is the contract, including the expert's
// skill_names list filled in after its metadata was charged. Twenty escaped
// Skill names — the per-expert maximum — plus their bodies must be accepted at
// exactly the 16 MiB limit, and one further body byte, exactly one wire byte,
// must be rejected before dispatch. Accepting the exact limit fails a meter
// that charges skill_names twice; rejecting the next byte fails a meter that
// under-counts their encoded length.
func TestInstallExpertEscapedSkillsExactBudgetBoundary(t *testing.T) {
	const (
		skillCount = 20
		sharedBody = 838_000
		probeBody  = 512_000
		sharedKey  = "shared/budget-body"
		soloKey    = "shared/budget-boundary-body"
	)
	name := escapedBudgetSkillName(0)
	encodedName, err := json.Marshal(name)
	if err != nil {
		t.Fatalf("marshal fixture Skill name: %v", err)
	}
	if len(encodedName) <= len(name)+2 {
		t.Fatalf("fixture Skill name %q does not inflate under JSON escaping", name)
	}

	refs := make([]model.SkillRef, 0, skillCount)
	for i := range skillCount {
		key := sharedKey
		if i == skillCount-1 {
			key = soloKey
		}
		refs = append(refs, model.SkillRef{Name: escapedBudgetSkillName(i), ObjectKey: key})
	}
	svc, caller, id := installFixture(t, &fakeFleet{}, refs)
	objects := svc.store.(*memObjectStore).objects
	objects[sharedKey] = []byte(strings.Repeat("a", sharedBody))
	install := func(bodyLen int) (*fakeFleet, int, wireCapabilityNames, error) {
		objects[soloKey] = []byte(strings.Repeat("a", bodyLen))
		attempt := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "agent-1"}}
		svc.WithFleet(attempt)
		if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err != nil {
			return attempt, 0, wireCapabilityNames{}, err
		}
		size, wire := capabilityPayload(t, attempt.capabilityInput)
		return attempt, size, wire, nil
	}

	_, probeSize, _, err := install(probeBody)
	if err != nil {
		t.Fatalf("InstallExpert probe: %v", err)
	}
	boundaryBody := budgetBoundaryBody(t, probeBody, probeSize)

	boundaryFleet, boundarySize, wire, err := install(boundaryBody)
	if err != nil {
		t.Fatalf("InstallExpert at calibrated body %d: %v", boundaryBody, err)
	}
	assertOnlyCapabilityInstall(t, boundaryFleet)
	if boundarySize != fleet.MaxCapabilityRequestBytes {
		t.Fatalf("accepted payload = %d bytes, want exactly %d", boundarySize, fleet.MaxCapabilityRequestBytes)
	}
	if growth := boundarySize - probeSize; growth != boundaryBody-probeBody {
		t.Fatalf("payload grew %d bytes for %d body bytes, want 1:1 ASCII encoding", growth, boundaryBody-probeBody)
	}

	if got := len(wire.Definition.Skills); got != skillCount {
		t.Fatalf("wire skills = %d, want %d", got, skillCount)
	}
	if got := len(wire.Definition.Experts); got != 1 {
		t.Fatalf("wire experts = %d, want 1", got)
	}
	if got := len(wire.Definition.Experts[0].SkillNames); got != skillCount {
		t.Fatalf("expert skill_names = %d entries, want %d", got, skillCount)
	}
	defined := make(map[string]int, skillCount)
	bodies := make(map[string]int, skillCount)
	for _, skill := range wire.Definition.Skills {
		defined[skill.Name]++
		bodies[skill.Name] = len(skill.Content)
	}
	referenced := make(map[string]int, skillCount)
	for _, skillName := range wire.Definition.Experts[0].SkillNames {
		referenced[skillName]++
	}
	for i := range skillCount {
		skillName := escapedBudgetSkillName(i)
		wantBody := sharedBody
		if i == skillCount-1 {
			wantBody = boundaryBody
		}
		if defined[skillName] != 1 {
			t.Fatalf("definition skills[%q] = %d entries, want exactly 1", skillName, defined[skillName])
		}
		if bodies[skillName] != wantBody {
			t.Fatalf("definition skills[%q] body = %d bytes, want %d", skillName, bodies[skillName], wantBody)
		}
		if referenced[skillName] != 1 {
			t.Fatalf("expert skill_names[%q] = %d entries, want exactly 1", skillName, referenced[skillName])
		}
	}

	assertOneByteOverBudget(t, install, boundaryBody)
}

// A squad shares one budget across members: each member repeats the same
// escaped Skill names, so the bodies are deduplicated into one definitions list
// while every member still pays for its own skill_names references. The summed
// payload must be accepted at exactly the limit and rejected one byte later.
func TestInstallSquadSharedEscapedSkillsExactBudgetBoundary(t *testing.T) {
	const (
		skillCount = 20
		sharedBody = 838_000
		probeBody  = 512_000
		sharedKey  = "shared/squad-budget-body"
		soloKey    = "shared/squad-boundary-body"
	)
	members := make([]model.SquadMember, 0, model.MaxSquadMembers)
	for mi := range model.MaxSquadMembers {
		member := model.SquadMember{
			MemberKey:   fmt.Sprintf("member_%02d", mi+1),
			Name:        fmt.Sprintf("Member %02d", mi+1),
			Role:        "worker",
			Instruction: "share the plan",
		}
		for si := range skillCount {
			key := sharedKey
			if si == skillCount-1 {
				key = soloKey
			}
			member.Skills = append(member.Skills, model.SkillRef{Name: escapedBudgetSkillName(si), ObjectKey: key})
		}
		members = append(members, member)
	}
	members[0].IsLeader = true

	svc, caller, id := installSquadFixture(t, &fakeFleet{}, members)
	objects := svc.store.(*memObjectStore).objects
	objects[sharedKey] = []byte(strings.Repeat("a", sharedBody))
	install := func(bodyLen int) (*fakeFleet, int, wireCapabilityNames, error) {
		objects[soloKey] = []byte(strings.Repeat("a", bodyLen))
		attempt := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{
			Type: "expert_team", ExpertTeamID: "squad-x", LeaderExpertID: "leader-x",
		}}
		svc.WithFleet(attempt)
		if _, err := svc.InstallSquad(context.Background(), caller, id, baseInput()); err != nil {
			return attempt, 0, wireCapabilityNames{}, err
		}
		size, wire := capabilityPayload(t, attempt.capabilityInput)
		return attempt, size, wire, nil
	}

	_, probeSize, _, err := install(probeBody)
	if err != nil {
		t.Fatalf("InstallSquad probe: %v", err)
	}
	boundaryBody := budgetBoundaryBody(t, probeBody, probeSize)

	boundaryFleet, boundarySize, wire, err := install(boundaryBody)
	if err != nil {
		t.Fatalf("InstallSquad at calibrated body %d: %v", boundaryBody, err)
	}
	assertOnlyCapabilityInstall(t, boundaryFleet)
	if boundarySize != fleet.MaxCapabilityRequestBytes {
		t.Fatalf("accepted payload = %d bytes, want exactly %d", boundarySize, fleet.MaxCapabilityRequestBytes)
	}
	if growth := boundarySize - probeSize; growth != boundaryBody-probeBody {
		t.Fatalf("payload grew %d bytes for %d body bytes, want 1:1 ASCII encoding", growth, boundaryBody-probeBody)
	}

	if got := len(wire.Definition.Skills); got != skillCount {
		t.Fatalf("wire skills = %d, want %d shared definitions", got, skillCount)
	}
	if got := len(wire.Definition.Experts); got != model.MaxSquadMembers {
		t.Fatalf("wire experts = %d, want %d members", got, model.MaxSquadMembers)
	}
	defined := make(map[string]int, skillCount)
	bodies := make(map[string]int, skillCount)
	for _, skill := range wire.Definition.Skills {
		defined[skill.Name]++
		bodies[skill.Name] = len(skill.Content)
	}
	for i := range skillCount {
		skillName := escapedBudgetSkillName(i)
		wantBody := sharedBody
		if i == skillCount-1 {
			wantBody = boundaryBody
		}
		if defined[skillName] != 1 {
			t.Fatalf("definition skills[%q] = %d entries, want exactly 1", skillName, defined[skillName])
		}
		if bodies[skillName] != wantBody {
			t.Fatalf("definition skills[%q] body = %d bytes, want %d", skillName, bodies[skillName], wantBody)
		}
	}
	for i, expert := range wire.Definition.Experts {
		if len(expert.SkillNames) != skillCount {
			t.Fatalf("member %d skill_names = %d entries, want %d", i, len(expert.SkillNames), skillCount)
		}
		for j := range skillCount {
			if got, want := expert.SkillNames[j], escapedBudgetSkillName(j); got != want {
				t.Fatalf("member %d skill_names[%d] = %q, want %q", i, j, got, want)
			}
		}
	}

	assertOneByteOverBudget(t, install, boundaryBody)
}
