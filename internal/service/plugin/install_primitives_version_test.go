package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func TestPreparePinnedSkillsCarriesNullableVersionAndSourceID(t *testing.T) {
	version := "1.2.3"
	input := samplePinnedSkill("market-skill", "release-id")
	input.DeclaredVersion = &version

	prepared, err := PreparePinnedSkills([]PinnedSkillFiles{input})
	if err != nil || len(prepared) != 1 {
		t.Fatalf("prepare versioned skill: count=%d err=%v", len(prepared), err)
	}
	skill := prepared[0].Skill
	if skill.Version == nil || *skill.Version != version || skill.MarketSkillID != "market-skill" {
		t.Fatalf("prepared Marketplace metadata = version %v, source %q", skill.Version, skill.MarketSkillID)
	}

	input.DeclaredVersion = nil
	prepared, err = PreparePinnedSkills([]PinnedSkillFiles{input})
	if err != nil || len(prepared) != 1 {
		t.Fatalf("prepare versionless skill: count=%d err=%v", len(prepared), err)
	}
	raw, err := json.Marshal(prepared[0].Skill)
	if err != nil {
		t.Fatalf("marshal versionless skill: %v", err)
	}
	var wire struct {
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode versionless skill: %v", err)
	}
	if string(wire.Version) != "null" {
		t.Fatalf("versionless skill wire version = %s, want JSON null", wire.Version)
	}
}

func TestPinnedInstallationUsesImmutableSkillReleaseVersionAndSource(t *testing.T) {
	s, store, remote := fixturePinned()
	key := pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}
	store.versions[key].DeclaredVersion = pin("2.3.4")
	store.plugins[key.PluginID].CurrentVersion = pin("9.8.7")

	p := installationParams()
	p.ResourceName = ""
	if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); err != nil {
		t.Fatalf("create pinned installation: %v", err)
	}
	if remote.calls != 1 {
		t.Fatalf("Fleet calls = %d, want 1", remote.calls)
	}

	var skillA, skillB *fleet.CapabilitySkill
	for i := range remote.in.Definition.Skills {
		skill := &remote.in.Definition.Skills[i]
		switch skill.MarketSkillID {
		case "skill-a":
			skillA = skill
		case "skill-b":
			skillB = skill
		}
	}
	if skillA == nil || skillB == nil {
		t.Fatalf("Fleet skills do not carry both Marketplace source IDs: %+v", remote.in.Definition.Skills)
	}
	if skillA.Version == nil || *skillA.Version != "2.3.4" {
		t.Fatalf("skill-a version = %v, want immutable release label 2.3.4 (not draft label 9.8.7)", skillA.Version)
	}
	if skillB.Version != nil {
		t.Fatalf("unversioned skill-b version = %q, want nil", *skillB.Version)
	}
	if skillA.Name != "Search a" {
		t.Fatalf("skill-a name = %q, want its immutable Marketplace name", skillA.Name)
	}
	if skillA.Content != "---\nname: Search\ndescription: fixture\n---\nBody a" {
		t.Fatalf("skill-a content was rewritten instead of preserved byte-for-byte: %q", skillA.Content)
	}
	foundSourceFile := false
	for _, file := range skillA.Files {
		if file.Path == "references/guide.md" && file.Source.Type == "inline_text" && file.Source.Content == "guide a" {
			foundSourceFile = true
		}
	}
	if !foundSourceFile {
		t.Fatalf("skill-a immutable source file missing from request: %+v", skillA.Files)
	}

	requestJSON, err := json.Marshal(remote.in)
	if err != nil {
		t.Fatalf("marshal Fleet request: %v", err)
	}
	var wire struct {
		Definition struct {
			Skills []struct {
				MarketSkillID string          `json:"market_skill_id"`
				Version       json.RawMessage `json:"version"`
			} `json:"skills"`
		} `json:"definition"`
	}
	if err := json.Unmarshal(requestJSON, &wire); err != nil {
		t.Fatalf("decode Fleet request: %v", err)
	}
	for _, skill := range wire.Definition.Skills {
		if skill.MarketSkillID == "skill-b" {
			if string(skill.Version) != "null" {
				t.Fatalf("unversioned skill-b wire version = %s, want JSON null", skill.Version)
			}
			return
		}
	}
	t.Fatal("Fleet wire request omitted skill-b's Marketplace source ID")
}

func TestPinnedInstallationRejectsInvalidSkillReleaseVersionBeforeFleet(t *testing.T) {
	for _, label := range []string{"", " 2.3.4", "2.3.4 "} {
		t.Run(label, func(t *testing.T) {
			s, store, remote := fixturePinned()
			key := pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}
			store.versions[key].DeclaredVersion = pin(label)

			p := installationParams()
			p.ResourceName = ""
			if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); err == nil {
				t.Fatal("invalid provided release version was accepted")
			}
			if remote.calls != 0 {
				t.Fatalf("Fleet was called %d times for an invalid release version", remote.calls)
			}
		})
	}
}
