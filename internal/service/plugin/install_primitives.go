package plugin

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"gopkg.in/yaml.v3"
)

// SkillInstallAdapterVersion identifies serialization, never a runtime alias.
const SkillInstallAdapterVersion = "original-v1"

// PinnedSkillFiles contains a verified, authorized immutable snapshot, not the
// current display name or an unresolved storage reference. Files are root-relative
// text files, including SKILL.md. Loading and digest verification belong to I1's
// resolver; this offline adapter does not perform I/O or authorize references.
type PinnedSkillFiles struct {
	PluginID    string
	VersionID   string
	Name        string
	Description string
	Files       map[string]string
}

// PreparedSkill retains source identity for callers' bindings/result mapping.
// Directory and Fleet Name preserve the market name; file bytes are unchanged.
type PreparedSkill struct {
	PluginID       string
	VersionID      string
	AdapterVersion string
	Directory      string
	Skill          fleet.CapabilitySkill
}

// PreparePinnedSkills produces temporary installation copies, never mutating
// input files. Repeat references to identical snapshots are deduplicated; an
// identity reused for different content is rejected, never silently overwritten.
func PreparePinnedSkills(inputs []PinnedSkillFiles) ([]PreparedSkill, error) {
	if len(inputs) > 600 {
		return nil, ErrTooLarge
	}
	inputs = append([]PinnedSkillFiles(nil), inputs...)
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].PluginID != inputs[j].PluginID {
			return inputs[i].PluginID < inputs[j].PluginID
		}
		return inputs[i].VersionID < inputs[j].VersionID
	})
	b := capabilityBuilder{remaining: maxCapabilityBytes}
	out := make([]PreparedSkill, 0, len(inputs))
	seen := map[string]string{}
	identities := map[string]string{}
	for _, input := range inputs {
		name, err := pinnedSkillName(input)
		if err != nil {
			return nil, err
		}
		identityBytes, _ := json.Marshal([2]string{input.PluginID, input.VersionID})
		identity := string(identityBytes)
		original, _ := json.Marshal(input)
		if previous, exists := identities[identity]; exists {
			if previous != string(original) {
				return nil, installationInvalid("definition.skills", "identity_content_conflict")
			}
			continue
		}
		contentBytes, _ := json.Marshal(struct {
			Description string
			Files       map[string]string
		}{input.Description, input.Files})
		if previous, exists := seen[name]; exists {
			if previous != string(contentBytes) {
				return nil, installationInvalid("definition.skills", "same_install_identity_different_content")
			}
			identities[identity] = string(original)
			continue
		}
		if len(input.Files) == 0 || len(input.Files) > 51 {
			return nil, installationInvalid("definition.skills.files", "invalid_count")
		}
		content, exists := input.Files["SKILL.md"]
		if !exists || strings.TrimSpace(content) == "" {
			return nil, installationInvalid("definition.skills.content", "required")
		}
		skill := fleet.CapabilitySkill{Name: name, Description: input.Description, Config: map[string]any{}}
		if err := b.boundedText("definition.skills.description", input.Description, 512); err != nil {
			return nil, err
		}
		paths := make([]string, 0, len(input.Files))
		for p := range input.Files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			if err := validateInstallFilePath(p); err != nil {
				return nil, err
			}
			value := input.Files[p]
			// Validate and forward the original bytes without rewriting metadata.
			if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return nil, installationInvalid("definition.skills.files", "text_only")
			}
			if len(value) > 1<<20 {
				return nil, ErrTooLarge
			}
			if p == "SKILL.md" {
				_, err = rewriteInstallFrontmatter(value, name)
				if err != nil {
					return nil, err
				}
				skill.Content = value
			} else {
				b.totalFiles++
				if b.totalFiles > 500 {
					return nil, ErrTooLarge
				}
				skill.Files = append(skill.Files, fleet.CapabilitySkillFile{Path: p, Source: fleet.CapabilityInlineTextSource{Type: "inline_text", Content: value}})
			}
			if err := b.text(value); err != nil {
				return nil, err
			}
		}
		seen[name], identities[identity] = string(contentBytes), string(original)
		out = append(out, PreparedSkill{PluginID: input.PluginID, VersionID: input.VersionID, AdapterVersion: SkillInstallAdapterVersion, Directory: name, Skill: skill})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Skill.Name < out[j].Skill.Name })
	return out, nil
}

func pinnedSkillName(input PinnedSkillFiles) (string, error) {
	for _, id := range []string{input.PluginID, input.VersionID} {
		if strings.TrimSpace(id) != id || id == "" || len(id) > 128 || !utf8.ValidString(id) || strings.ContainsRune(id, 0) {
			return "", installationInvalid("definition.skills", "immutable_identity_required")
		}
	}
	if !installationName(input.Name, 128) {
		return "", installationInvalid("definition.skills", "invalid_name")
	}
	return input.Name, nil
}

func validateInstallFilePath(p string) error {
	clean, ok := normalizedArchivePath(p)
	if !ok || len(clean) > 512 || strings.ContainsRune(clean, ':') {
		return installationInvalid("definition.skills.files", "unsafe_path")
	}
	if clean != "SKILL.md" && strings.EqualFold(clean, "SKILL.md") {
		return installationInvalid("definition.skills.files", "reserved_path")
	}
	for _, segment := range strings.Split(clean, "/") {
		if strings.EqualFold(segment, ".git") {
			return installationInvalid("definition.skills.files", "reserved_path")
		}
	}
	return nil
}

func rewriteInstallFrontmatter(content, name string) (string, error) {
	// Keep the Markdown body byte-for-byte. Only the YAML header is serialized.
	if strings.HasPrefix(content, "\ufeff") {
		return "", installationInvalid("definition.skills.content", "unsupported_frontmatter")
	}
	lines := strings.SplitAfter(content, "\n")
	if strings.TrimSuffix(strings.TrimSuffix(lines[0], "\n"), "\r") != "---" {
		return "---\nname: " + name + "\n---\n" + content, nil
	}
	offset := len(lines[0])
	end := -1
	bodyStart := 0
	for _, line := range lines[1:] {
		if strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r") == "---" {
			end = offset
			bodyStart = offset + len(line)
			break
		}
		offset += len(line)
	}
	if end < 0 {
		return "", installationInvalid("definition.skills.content", "invalid_frontmatter")
	}
	header := content[len(lines[0]):end]
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(header), &doc); err != nil {
		return "", installationInvalid("definition.skills.content", "invalid_frontmatter")
	}
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if len(doc.Content) > 0 {
		mapping = doc.Content[0]
	}
	if mapping.Kind != yaml.MappingNode {
		return "", installationInvalid("definition.skills.content", "invalid_frontmatter")
	}
	// Reject aliases/anchors rather than changing other metadata through a name
	// anchor. Duplicate or non-string keys also cannot be safely interpreted.
	if !plainInstallYAML(mapping) {
		return "", installationInvalid("definition.skills.content", "unsupported_frontmatter")
	}
	var check map[string]any
	if err := mapping.Decode(&check); err != nil {
		return "", installationInvalid("definition.skills.content", "invalid_frontmatter")
	}
	found := false
	for i := 0; i < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return "", installationInvalid("definition.skills.content", "unsupported_frontmatter")
		}
		if key.Value == "name" {
			mapping.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
			found = true
		}
	}
	if !found {
		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
	}
	encoded, err := yaml.Marshal(mapping)
	if err != nil {
		return "", installationInvalid("definition.skills.content", "invalid_frontmatter")
	}
	return "---\n" + string(encoded) + "---\n" + content[bodyStart:], nil
}

func plainInstallYAML(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return false
	}
	for _, child := range node.Content {
		if !plainInstallYAML(child) {
			return false
		}
	}
	return true
}

// CurrentConnectorMCP is an already-authorized current effective configuration.
// ConnectorID is the resource identity, never a version pin.
type CurrentConnectorMCP struct {
	ConnectorID string
	Config      string
}

// MergeCurrentConnectorMCP accepts only already-authorized effective connector
// configs. It never reads an expert's embedded mcp.json or guesses a version.
// Values are transient and must not be persisted/logged by the caller.
func MergeCurrentConnectorMCP(configs []CurrentConnectorMCP) (json.RawMessage, error) {
	if len(configs) > 600 {
		return nil, ErrTooLarge
	}
	merged := map[string]json.RawMessage{}
	seenConnectors := map[string]string{}
	remaining := maxCapabilityBytes
	for _, input := range configs {
		if input.ConnectorID == "" || strings.TrimSpace(input.ConnectorID) != input.ConnectorID || len(input.ConnectorID) > 128 || !utf8.ValidString(input.ConnectorID) || strings.ContainsRune(input.ConnectorID, 0) {
			return nil, invalidCapabilityMCP("connector_identity_required")
		}
		raw := input.Config
		remaining -= len(raw)
		if remaining < 0 {
			return nil, ErrTooLarge
		}
		normalized, err := normalizeCapabilityMCP(raw)
		if err != nil {
			return nil, err
		}
		if previous, exists := seenConnectors[input.ConnectorID]; exists {
			if previous != string(normalized) {
				return nil, invalidCapabilityMCP("connector_content_conflict")
			}
			continue
		}
		seenConnectors[input.ConnectorID] = string(normalized)
		if len(normalized) == 0 {
			continue
		}
		object, err := capabilityMCPObject(normalized)
		if err != nil {
			return nil, err
		}
		servers, err := capabilityMCPObject(object["mcpServers"])
		if err != nil {
			return nil, err
		}
		for name, server := range servers {
			if _, exists := merged[name]; exists {
				return nil, invalidCapabilityMCP("duplicate_server")
			}
			merged[name] = server
		}
		if len(merged) > 100 {
			return nil, ErrTooLarge
		}
	}
	if len(merged) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(map[string]any{"mcpServers": merged})
	if err != nil {
		return nil, invalidCapabilityMCP("invalid_json")
	}
	return normalizeCapabilityMCP(string(raw))
}
