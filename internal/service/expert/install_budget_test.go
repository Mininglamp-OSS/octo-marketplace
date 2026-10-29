package expert

import (
	"bytes"
	"context"
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
