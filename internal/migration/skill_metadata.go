package migration

import (
	"encoding/json"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"gopkg.in/yaml.v3"
)

// Legacy embedded Skills often have an empty catalog description although their
// original SKILL.md contains it. Derive only missing display metadata, leaving
// source evidence, file bytes and curated descriptions untouched.
func completeSkillDescription(n *Node) string {
	if n.Plugin.Type != model.PluginTypeSkill {
		return ""
	}
	var manifest map[string]json.RawMessage
	if json.Unmarshal(n.Plugin.Manifest, &manifest) != nil {
		return "skill_description_unavailable"
	}
	var current string
	if raw, ok := manifest["description"]; ok && json.Unmarshal(raw, &current) != nil {
		return "skill_description_unavailable"
	}
	if strings.TrimSpace(current) != "" {
		return ""
	}
	for _, f := range n.Files {
		if f.Path != "SKILL.md" {
			continue
		}
		description := originalSkillDescription(string(f.Data))
		if description == "" {
			return "skill_description_unavailable"
		}
		manifest["description"], _ = json.Marshal(description)
		n.Plugin.Manifest, _ = json.Marshal(manifest)
		return ""
	}
	return "skill_description_unavailable"
}
func originalSkillDescription(content string) string {
	// Normalize a parsing copy only; the archived/installable bytes stay exact.
	content = strings.TrimPrefix(content, "\ufeff")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	first, rest, ok := strings.Cut(content, "\n")
	if !ok || strings.TrimSpace(first) != "---" {
		return ""
	}
	var header strings.Builder
	closed := false
	for _, line := range strings.Split(rest, "\n") {
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		if header.Len()+len(line)+1 > 64<<10 {
			return ""
		}
		header.WriteString(line)
		header.WriteByte('\n')
	}
	if !closed {
		return ""
	}
	var document yaml.Node
	if yaml.Unmarshal([]byte(header.String()), &document) != nil || len(document.Content) != 1 {
		return ""
	}
	mapping := document.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return ""
	}
	seen := map[string]bool{}
	description := ""
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i], mapping.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
			return ""
		}
		seen[key.Value] = true
		if key.Value == "description" {
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return ""
			}
			description = strings.TrimSpace(value.Value)
		}
	}
	return description
}
