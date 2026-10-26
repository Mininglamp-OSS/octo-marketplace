// Export a synthetic payload through Marketplace's actual stable-name adapter.
package main

import (
	"encoding/json"
	"os"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func main() {
	inputs := []pluginsvc.PinnedSkillFiles{}
	for _, identity := range [][2]string{{"fixture-skill-a", "release-1"}, {"fixture-skill-b", "release-1"}, {"fixture-skill-a", "release-2"}} {
		marker := identity[0] + "@" + identity[1]
		inputs = append(inputs, pluginsvc.PinnedSkillFiles{PluginID: identity[0], VersionID: identity[1], Name: "Search", Description: "Synthetic rehearsal", Files: map[string]string{"SKILL.md": "---\nname: Search\ndescription: Synthetic rehearsal\n---\nFixture " + marker + "\n", "references/guide.md": "Guide " + marker + "\n"}})
	}
	prepared, err := pluginsvc.PreparePinnedSkills(inputs)
	if err != nil {
		panic("prepare rehearsal payload failed")
	}
	def := fleet.CapabilityDefinition{SchemaVersion: "1.0", Name: "marketplace:isolated-rehearsal"}
	byIdentity := map[string]string{}
	for _, p := range prepared {
		def.Skills = append(def.Skills, p.Skill)
		byIdentity[p.PluginID+"@"+p.VersionID] = p.Skill.Name
	}
	def.Experts = []fleet.CapabilityExpert{{Name: "Rehearsal analyst", Instructions: "Use exact pinned skills.", SkillNames: []string{byIdentity["fixture-skill-a@release-1"], byIdentity["fixture-skill-a@release-2"]}}, {Name: "Rehearsal reviewer", Instructions: "Review using the shared first skill and a different author's Search.", SkillNames: []string{byIdentity["fixture-skill-a@release-1"], byIdentity["fixture-skill-b@release-1"]}}}
	def.ExpertTeam = &fleet.CapabilityExpertTeam{Name: "Isolated rehearsal", Instructions: "Synthetic capability installation only.", Members: []fleet.CapabilityTeamMember{{ExpertName: def.Experts[0].Name, Role: "leader", IsLeader: true}, {ExpertName: def.Experts[1].Name, Role: "reviewer"}}}
	req := fleet.CapabilityInstallRequest{Definition: def}
	for _, e := range def.Experts {
		req.Bindings.Experts = append(req.Bindings.Experts, fleet.CapabilityExpertBinding{ExpertName: e.Name, RuntimeID: "00000000-0000-0000-0000-000000000001"})
	}
	if json.NewEncoder(os.Stdout).Encode(req) != nil {
		panic("encode rehearsal payload failed")
	}
}
