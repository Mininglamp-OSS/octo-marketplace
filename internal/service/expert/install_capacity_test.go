package expert

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestInstallExpertPreservesEntryAboveReadLimit(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "installed"}}
	svc, caller, id := installFixture(t, ff, []model.SkillRef{{Name: "large entry", ObjectKey: "large-entry"}})
	content := strings.Repeat("a", 2_100_000)
	svc.store.(*memObjectStore).objects["large-entry"] = []byte(content)
	if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err != nil {
		t.Fatal(err)
	}
	if ff.capabilityInput.Definition.Skills[0].Content != content {
		t.Fatal("entry bytes were altered")
	}
}

func TestInstallExpertRejectsUnreadablePackageWithoutInstallingPartialSkill(t *testing.T) {
	ff := &fakeFleet{capabilityResult: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "installed"}}
	svc, caller, id := installFixture(t, ff, []model.SkillRef{{Name: "broken package", ObjectKey: "entry", ZipObjectKey: "broken.zip"}})
	objects := svc.store.(*memObjectStore)
	objects.objects["entry"] = []byte("# Skill")
	objects.objects["broken.zip"] = []byte("not a ZIP archive")
	if _, err := svc.InstallExpert(context.Background(), caller, id, baseInput()); err == nil {
		t.Fatal("unreadable declared package produced a partial installation")
	}
	if ff.capabilityCalls != 0 {
		t.Fatal("invalid package reached Fleet")
	}
}

func TestInstallExpertReportsCapacityDiagnostics(t *testing.T) {
	ff := &fakeFleet{}
	refs := []model.SkillRef{{Name: "one", ObjectKey: "escaped"}, {Name: "two", ObjectKey: "escaped"}, {Name: "three", ObjectKey: "escaped"}, {Name: "four", ObjectKey: "escaped"}}
	svc, caller, id := installFixture(t, ff, refs)
	svc.store.(*memObjectStore).objects["escaped"] = []byte(strings.Repeat("<", 900_000))
	_, err := svc.InstallExpert(context.Background(), caller, id, baseInput())
	if !errors.Is(err, ErrInstallTooLarge) {
		t.Fatalf("error=%v", err)
	}
	var diagnostic interface{ CapacityDetails() map[string]any }
	if !errors.As(err, &diagnostic) {
		t.Fatalf("missing capacity details: %v", err)
	}
	details := diagnostic.CapacityDetails()
	if details["limit_bytes"] != int64(20_000_000) || details["actual_bytes"].(int64) <= 20_000_000 || details["field"] != "definition" {
		t.Fatalf("details=%v", details)
	}
}
