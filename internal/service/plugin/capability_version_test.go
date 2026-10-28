package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestCapabilityBuildSerializesNullableSkillVersionsAndMarketIDs(t *testing.T) {
	svc, store, _ := capabilityFixture()
	store.plugins["skill-1"].CurrentVersion = nil
	secondVersion := "2.0"
	second := *store.plugins["skill-1"]
	second.ID, second.Name, second.CurrentVersion = "skill-2", "Review", &secondVersion
	store.plugins[second.ID] = &second
	store.relations["expert-1"] = append(store.relations["expert-1"], model.PluginRelation{Type: "expert_skill", TargetPluginID: second.ID, Status: 1})

	in, err := svc.buildCapabilityInstall(context.Background(), testCaller, "expert-1", installationParams())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Definition struct {
			Skills []map[string]any `json:"skills"`
		} `json:"definition"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	wantVersions := map[string]any{"Deploy": nil, "Review": "2.0"}
	wantIDs := map[string]string{"Deploy": "skill-1", "Review": "skill-2"}
	if len(request.Definition.Skills) != 2 {
		t.Fatalf("skills=%v", request.Definition.Skills)
	}
	for _, skill := range request.Definition.Skills {
		name, ok := skill["name"].(string)
		if !ok {
			t.Fatalf("skill has invalid name: %v", skill)
		}
		version, exists := skill["version"]
		if !exists || version != wantVersions[name] || skill["market_skill_id"] != wantIDs[name] {
			t.Fatalf("skill %s has version=%v source=%v", name, version, skill["market_skill_id"])
		}
	}
}

func TestCapabilityBuildRejectsInvalidProvidedSkillVersion(t *testing.T) {
	for _, version := range []*string{stringPtr(""), stringPtr(" 1.0.0"), stringPtr("1.0.0 ")} {
		svc, store, _ := capabilityFixture()
		store.plugins["skill-1"].CurrentVersion = version
		_, err := svc.buildCapabilityInstall(context.Background(), testCaller, "expert-1", installationParams())
		var invalid *InstallationValidationError
		if !errors.As(err, &invalid) || invalid.Field != "definition.skills.version" {
			t.Fatalf("version=%v error=%v; want skill version validation error", version, err)
		}
	}
}

func TestCapabilityBuildRejectsSameNameDifferentVersions(t *testing.T) {
	svc, store, _ := capabilityFixture()
	firstVersion, secondVersion := "1.0.0", "2.0.0"
	store.plugins["skill-1"].CurrentVersion = &firstVersion
	second := *store.plugins["skill-1"]
	second.ID, second.CurrentVersion = "skill-2", &secondVersion
	store.plugins[second.ID] = &second
	store.relations["expert-1"] = append(store.relations["expert-1"], model.PluginRelation{Type: "expert_skill", TargetPluginID: second.ID, Status: 1})
	_, err := svc.buildCapabilityInstall(context.Background(), testCaller, "expert-1", installationParams())
	var invalid *InstallationValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("different versions of one name cannot share a request definition: %v", err)
	}
}
