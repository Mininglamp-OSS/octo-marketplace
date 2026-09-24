package plugin

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

const maxCapabilityBytes = 16 << 20

type capabilityBuilder struct {
	service      *Service
	caller       Caller
	remaining    int64
	skillsByID   map[string]string
	skillsByName map[string]fleet.CapabilitySkill
	expertNames  map[string]bool
	totalFiles   int
}

func (s *Service) buildCapabilityInstall(ctx context.Context, caller Caller, pluginID string, p InstallationParams) (*fleet.CapabilityInstallRequest, error) {
	root, err := s.resolveInstallDetail(ctx, caller, pluginID)
	if err != nil {
		return nil, err
	}
	if root.Plugin.IsEmbedded {
		return nil, ErrNotFound
	}
	b := capabilityBuilder{service: s, caller: caller, remaining: min(s.maxArchiveBytes, maxCapabilityBytes), skillsByID: map[string]string{}, skillsByName: map[string]fleet.CapabilitySkill{}, expertNames: map[string]bool{}}
	def := fleet.CapabilityDefinition{SchemaVersion: "1.0", Name: "marketplace:" + root.Plugin.ID}
	if !installationName(def.Name, maxInstallationNameRunes) {
		return nil, installationInvalid("definition.name", "invalid")
	}
	name := strings.TrimSpace(root.Plugin.Name)
	if p.ResourceName != "" {
		name = p.ResourceName
	}
	if !installationName(name, maxInstallationNameRunes) {
		return nil, installationInvalid("definition.name", "invalid")
	}
	summary := manifestDescription(root.Plugin.Manifest)
	switch root.Plugin.Type {
	case model.PluginTypeExpert:
		expert, err := b.expert(ctx, root, name, summary)
		if err != nil {
			return nil, err
		}
		def.Experts = []fleet.CapabilityExpert{*expert}
	case model.PluginTypeExpertTeam:
		relations := relationsOfType(root.Relations, "expert_team_expert")
		// A tied display order must not select a different fallback leader on
		// retry when the store returns the same graph in a different order.
		sort.Slice(relations, func(i, j int) bool {
			if relations[i].SortOrder != relations[j].SortOrder {
				return relations[i].SortOrder < relations[j].SortOrder
			}
			return relations[i].TargetPluginID < relations[j].TargetPluginID
		})
		if len(relations) < 1 || len(relations) > 30 {
			return nil, installationInvalid("definition.expert_team.members", "invalid_count")
		}
		instructions, ok := rawAttachmentContent(root.Plugin.Package, "AGENTS.md")
		if !ok {
			return nil, installationInvalid("definition.expert_team.instructions", "required")
		}
		if err := b.text(instructions); err != nil {
			return nil, err
		}
		if err := b.boundedText("definition.expert_team.description", summary, 255); err != nil {
			return nil, err
		}
		team := &fleet.CapabilityExpertTeam{Name: name, Description: summary, Instructions: instructions}
		leader := -1
		for i, rel := range relations {
			detail, err := s.resolveInstallDetail(ctx, caller, rel.TargetPluginID)
			if err != nil {
				return nil, err
			}
			expert, err := b.expert(ctx, detail, strings.TrimSpace(detail.Plugin.Name), summary)
			if err != nil {
				return nil, err
			}
			def.Experts = append(def.Experts, *expert)
			var wiring struct {
				Role      string `json:"role"`
				IsLeader  bool   `json:"is_leader"`
				MemberKey string `json:"member_key"`
			}
			if len(rel.Data) > 0 && json.Unmarshal(rel.Data, &wiring) != nil {
				return nil, installationInvalid("definition.expert_team.members", "invalid_wiring")
			}
			if wiring.MemberKey == "" && wiring.Role == "" {
				if raw, ok := rawAttachmentContent(detail.Plugin.Package, "expert/context.json"); ok && json.Unmarshal([]byte(raw), &wiring) != nil {
					return nil, installationInvalid("definition.expert_team.members", "invalid_wiring")
				}
			}
			if wiring.IsLeader && leader == -1 {
				leader = i
			}
			team.Members = append(team.Members, fleet.CapabilityTeamMember{ExpertName: expert.Name, Role: strings.TrimSpace(wiring.Role)})
		}
		// Preserve legacy first-flagged / first-member leader selection, then emit
		// exactly one leader as required by Fleet's stricter Definition contract.
		if leader == -1 {
			leader = 0
		}
		for i := range team.Members {
			member := &team.Members[i]
			member.IsLeader = i == leader
			if member.IsLeader {
				member.Role = "leader"
			} else if member.Role == "" {
				member.Role = "member"
			}
			if !member.IsLeader && member.Role == "leader" {
				return nil, installationInvalid("definition.expert_team.members", "reserved_role")
			}
			if err := b.boundedText("definition.expert_team.members.role", member.Role, 500); err != nil {
				return nil, err
			}
		}
		sort.Slice(team.Members, func(i, j int) bool { return team.Members[i].ExpertName < team.Members[j].ExpertName })
		def.ExpertTeam = team
	default:
		return nil, installationInvalid("plugin_id", "expert_or_expert_team_required")
	}
	for _, skill := range b.skillsByName {
		def.Skills = append(def.Skills, skill)
	}
	sort.Slice(def.Skills, func(i, j int) bool { return def.Skills[i].Name < def.Skills[j].Name })
	sort.Slice(def.Experts, func(i, j int) bool { return def.Experts[i].Name < def.Experts[j].Name })
	in := &fleet.CapabilityInstallRequest{Definition: def}
	for i := range in.Definition.Experts {
		expert := &in.Definition.Experts[i]
		expert.CustomEnv = p.CustomEnv
		in.Bindings.Experts = append(in.Bindings.Experts, fleet.CapabilityExpertBinding{ExpertName: expert.Name, RuntimeID: p.RuntimeID})
	}
	return in, nil
}

func (b *capabilityBuilder) expert(ctx context.Context, detail *Detail, name, summary string) (*fleet.CapabilityExpert, error) {
	if detail.Plugin.Type != model.PluginTypeExpert {
		return nil, installationInvalid("definition.experts", "wrong_dependency_type")
	}
	if !installationName(name, maxInstallationNameRunes) || b.expertNames[name] {
		return nil, installationInvalid("definition.experts", "invalid_or_duplicate_name")
	}
	b.expertNames[name] = true
	instructions, ok := rawAttachmentContent(detail.Plugin.Package, "AGENTS.md")
	if !ok || strings.TrimSpace(instructions) == "" {
		return nil, installationInvalid("definition.experts.instructions", "required")
	}
	if err := b.text(instructions); err != nil {
		return nil, err
	}
	if err := b.boundedText("definition.experts.description", summary, 255); err != nil {
		return nil, err
	}
	out := &fleet.CapabilityExpert{Name: name, Description: summary, Instructions: instructions}
	for _, attachment := range decodePackageAttachments(detail.Plugin.Package) {
		if attachment.Path != "mcp.json" {
			continue
		}
		if attachment.ContentType != "raw" {
			return nil, installationInvalid("definition.experts.mcp_config", "unsupported_source")
		}
		config, err := normalizeCapabilityMCP(attachment.RawContent)
		if err != nil {
			return nil, err
		}
		if err := b.text(string(config)); err != nil {
			return nil, err
		}
		out.MCPConfig = config
	}
	relations := relationsOfType(detail.Relations, "expert_skill")
	if len(relations) > 20 {
		return nil, ErrTooLarge
	}
	names := map[string]bool{}
	for _, rel := range relations {
		name, err := b.skill(ctx, rel.TargetPluginID)
		if err != nil {
			return nil, err
		}
		if !names[name] {
			out.SkillNames = append(out.SkillNames, name)
			names[name] = true
		}
	}
	sort.Strings(out.SkillNames)
	return out, nil
}

func (b *capabilityBuilder) skill(ctx context.Context, id string) (string, error) {
	if name, ok := b.skillsByID[id]; ok {
		return name, nil
	}
	if len(b.skillsByID) >= 600 {
		return "", ErrTooLarge
	}
	detail, err := b.service.Detail(ctx, b.caller, id, false)
	if err != nil {
		return "", err
	}
	p := detail.Plugin
	name := strings.TrimSpace(p.Name)
	if p.Type != model.PluginTypeSkill || !installationName(name, 128) {
		return "", installationInvalid("definition.skills", "invalid_dependency")
	}
	files, err := b.skillFiles(ctx, p)
	if err != nil {
		return "", err
	}
	content, ok := files["SKILL.md"]
	if !ok || content == "" {
		return "", installationInvalid("definition.skills.content", "required")
	}
	skill := fleet.CapabilitySkill{Name: name, Description: manifestDescription(p.Manifest), Content: content, Config: map[string]any{}}
	if err := b.boundedText("definition.skills.description", skill.Description, 512); err != nil {
		return "", err
	}
	for filePath, content := range files {
		if filePath == "SKILL.md" {
			continue
		}
		skill.Files = append(skill.Files, fleet.CapabilitySkillFile{Path: filePath, Source: fleet.CapabilityInlineTextSource{Type: "inline_text", Content: content}})
	}
	sort.Slice(skill.Files, func(i, j int) bool { return skill.Files[i].Path < skill.Files[j].Path })
	if previous, exists := b.skillsByName[name]; exists {
		before, _ := json.Marshal(previous)
		after, _ := json.Marshal(skill)
		if string(before) != string(after) {
			return "", installationInvalid("definition.skills", "same_name_different_content")
		}
	} else {
		b.skillsByName[name] = skill
	}
	b.skillsByID[id] = name
	return name, nil
}

func (b *capabilityBuilder) boundedText(field, value string, maxRunes int) error {
	if utf8.RuneCountInString(value) > maxRunes {
		return installationInvalid(field, "too_long")
	}
	return b.text(value)
}
