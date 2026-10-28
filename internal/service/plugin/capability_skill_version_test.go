package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func TestCapabilitySkillSourceMetadata(t *testing.T) {
	for _, root := range []string{"expert-1", "team-1"} {
		for _, version := range []*string{nil, pin("release/1.2.0")} {
			t.Run(root+"/"+sourceVersionLabel(version), func(t *testing.T) {
				svc, store, remote := capabilityFixture()
				store.plugins["skill-1"].CurrentVersion = version
				store.plugins[root].CurrentVersion = pin("expert-version-must-not-leak")
				if _, err := svc.CreateInstallation(context.Background(), testCaller, root, installationParams()); err != nil {
					t.Fatal(err)
				}
				skills := remote.in.Definition.Skills
				if len(skills) != 1 || skills[0].MarketSkillID != "skill-1" || sourceVersionLabel(skills[0].Version) != sourceVersionLabel(version) {
					t.Fatalf("incorrect source metadata: %+v", skills)
				}
				raw, err := json.Marshal(skills[0])
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]json.RawMessage
				if err = json.Unmarshal(raw, &wire); err != nil {
					t.Fatal(err)
				}
				want, _ := json.Marshal(version)
				if string(wire["version"]) != string(want) || string(wire["market_skill_id"]) != `"skill-1"` {
					t.Fatalf("source metadata lost in JSON: %s", raw)
				}
			})
		}
	}
}

func sourceVersionLabel(version *string) string {
	if version == nil {
		return "null"
	}
	return *version
}

func TestPinnedSkillMetadataComesFromSelectedSnapshot(t *testing.T) {
	for _, version := range []*string{nil, pin("released-1.2.0")} {
		t.Run(sourceVersionLabel(version), func(t *testing.T) {
			svc, store, remote := fixturePinned()
			for _, id := range []string{"skill-a", "skill-b"} {
				store.plugins[id].CurrentVersion = pin("unpublished-draft")
				store.versions[pluginrepo.InstallVersionKey{PluginID: id, VersionID: "skill-v1"}].DeclaredVersion = version
			}
			if _, err := svc.CreateInstallation(context.Background(), testCaller, "team", installationParams()); err != nil {
				t.Fatal(err)
			}
			if len(remote.in.Definition.Skills) != 2 {
				t.Fatalf("wrong skill count: %+v", remote.in.Definition.Skills)
			}
			for _, skill := range remote.in.Definition.Skills {
				if (skill.MarketSkillID != "skill-a" && skill.MarketSkillID != "skill-b") || sourceVersionLabel(skill.Version) != sourceVersionLabel(version) {
					t.Fatalf("live draft metadata leaked: %+v", skill)
				}
			}
			if *store.plugins["skill-a"].CurrentVersion != "unpublished-draft" {
				t.Fatal("source row was mutated")
			}
		})
	}
}

func TestCapabilitySkillInvalidVersionStopsBeforeFleet(t *testing.T) {
	for _, version := range []string{"", " ", " v1", "v1\t", "v\x001", string([]byte{0xff})} {
		for _, pinned := range []bool{false, true} {
			t.Run(sourceVersionLabel(&version)+map[bool]string{false: "/compatibility", true: "/pinned"}[pinned], func(t *testing.T) {
				svc, store, remote := capabilityFixture()
				root := "expert-1"
				if pinned {
					var frozen *pinnedFixtureStore
					svc, frozen, remote = fixturePinned()
					frozen.versions[pluginrepo.InstallVersionKey{PluginID: "skill-a", VersionID: "skill-v1"}].DeclaredVersion = &version
					root = "team"
				} else {
					store.plugins["skill-1"].CurrentVersion = &version
				}
				_, err := svc.CreateInstallation(context.Background(), testCaller, root, installationParams())
				var invalid *InstallationValidationError
				if !errors.As(err, &invalid) || invalid.Field != "definition.skills.version" || remote.calls != 0 {
					t.Fatalf("invalid version reached Fleet: err=%v calls=%d", err, remote.calls)
				}
			})
		}
	}
}

func TestCapabilitySkillSourceCollisionStopsBeforeFleet(t *testing.T) {
	for _, version := range []*string{nil, pin("same-version"), pin("different-version")} {
		t.Run(sourceVersionLabel(version), func(t *testing.T) {
			svc, store, remote := capabilityFixture()
			store.plugins["skill-1"].CurrentVersion = pin("same-version")
			other := *store.plugins["skill-1"]
			other.ID, other.CurrentVersion = "skill-2", version
			store.plugins[other.ID] = &other
			store.relations["expert-1"] = append(store.relations["expert-1"], model.PluginRelation{Type: "expert_skill", TargetPluginID: other.ID, Status: 1})
			_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
			var invalid *InstallationValidationError
			if !errors.As(err, &invalid) || invalid.Reason != "same_name_different_content" || remote.calls != 0 {
				t.Fatalf("same-name source identity was silently dropped: err=%v calls=%d", err, remote.calls)
			}
		})
	}
}
