package migration

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/artifactscope"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// Check again at the persistence boundary, even for caller-built or resumed plans.
func validateFinalContracts(p Plan) []Issue {
	var issues []Issue
	nodes := map[string]Node{}
	for _, n := range p.Nodes {
		nodes[n.Plugin.ID] = n
	}
	for _, n := range p.Nodes {
		if pluginrepo.ValidateReferenceMetadata(n.Plugin.Type, n.Relations) != nil {
			issues = append(issues, Issue{n.SourceID, "relation_metadata_invalid"})
		}
		for _, edge := range n.Relations {
			target, ok := nodes[edge.TargetPluginID]
			if !ok || edge.SourcePluginID != n.Plugin.ID || edge.SourcePluginType != n.Plugin.Type || edge.TargetPluginType != target.Plugin.Type || target.Plugin.Status != 1 || target.Plugin.DeletedAt != nil || !accessible(n.Plugin, target.Plugin) {
				issues = append(issues, Issue{n.SourceID, "relation_target_invalid"})
			}
		}
		if n.Plugin.Type != model.PluginTypeExpert {
			continue
		}
		invalid := false
		reserved := func(s string) bool {
			return strings.EqualFold(path.Clean(strings.ReplaceAll(strings.TrimSpace(s), "\\", "/")), "expert/context.json")
		}
		for _, f := range n.Files {
			invalid = invalid || reserved(f.Path)
		}
		for _, raw := range []json.RawMessage{n.Plugin.Package, n.Artifacts} {
			if len(raw) == 0 {
				continue
			}
			var doc any
			if json.Unmarshal(raw, &doc) != nil {
				invalid = true
				continue
			}
			var visit func(any)
			visit = func(v any) {
				switch obj := v.(type) {
				case map[string]any:
					for k, v := range obj {
						if k == "path" {
							if s, ok := v.(string); ok && reserved(s) {
								invalid = true
							}
						}
						visit(v)
					}
				case []any:
					for _, v := range obj {
						visit(v)
					}
				}
			}
			visit(doc)
		}
		var sidecar map[string]string
		if len(n.Plugin.AttachmentKeys) > 0 && json.Unmarshal(n.Plugin.AttachmentKeys, &sidecar) != nil {
			invalid = true
		}
		for name := range sidecar {
			invalid = invalid || reserved(name)
		}
		if invalid {
			issues = append(issues, Issue{n.SourceID, "reserved_expert_context_path"})
		}
	}
	return append(append(issues, connectorNameIssues(p)...), iconIssues(p)...)
}

// Fleet's connector map uses the trimmed display name, scoped to one expanded
// installation. Different roots may legitimately have same-named connectors;
// two distinct identities within the same expert/team closure are ambiguous.
// Refuse the plan rather than rename any original MCP configuration implicitly.
func connectorNameIssues(p Plan) []Issue {
	nodes := make(map[string]Node, len(p.Nodes))
	for _, n := range p.Nodes {
		nodes[n.Plugin.ID] = n
	}
	var issues []Issue
	for _, root := range p.Nodes {
		if root.Plugin.Type != model.PluginTypeExpert && root.Plugin.Type != model.PluginTypeExpertTeam {
			continue
		}
		seen := map[string]bool{}
		owners := map[string]string{}
		ambiguous := false
		var visit func(string)
		visit = func(id string) {
			if seen[id] {
				return
			}
			seen[id] = true
			n, ok := nodes[id]
			if !ok {
				return
			}
			if n.Plugin.Type == model.PluginTypeConnector {
				name := strings.TrimSpace(n.Plugin.Name)
				if owner, ok := owners[name]; ok && owner != id {
					ambiguous = true
				}
				owners[name] = id
			}
			for _, edge := range n.Relations {
				visit(edge.TargetPluginID)
			}
		}
		visit(root.Plugin.ID)
		if ambiguous {
			issues = append(issues, Issue{root.SourceID, "connector_name_ambiguous"})
		}
	}
	return issues
}

// Managed icon upload IDs are global and unchanged when a plugin is cloned.
// Retain only the exact captured source key; bind its bytes to source evidence.
func iconIssues(p Plan) []Issue {
	var issues []Issue
	expected := map[string]SourceObject{}
	for _, n := range p.Nodes {
		key := n.Plugin.Icon
		managed := artifactscope.IsManagedIconKey(key)
		referenced := strings.Contains(key, "/") && !strings.HasPrefix(key, "http://") && !strings.HasPrefix(key, "https://")
		if !referenced && n.IconFile == nil {
			continue
		}
		f := n.IconFile
		valid := managed && f != nil && key == n.Source.Icon
		if valid {
			valid = f.ObjectKey == key && f.Path == key && path.Clean(key) == key && len(key) <= 512 && int64(len(f.Data)) <= 20<<20
			bound := false
			for _, o := range n.Evidence[n.SourceID].Objects {
				if o.Key == key && o.Size == int64(len(f.Data)) && o.Hash == digest(f.Data) {
					bound = true
				}
			}
			valid = valid && bound
		}
		if valid {
			current := SourceObject{Key: key, Hash: digest(f.Data), Size: int64(len(f.Data))}
			if previous, ok := expected[key]; ok && previous != current {
				valid = false
			}
			expected[key] = current
		}
		if !valid {
			issues = append(issues, Issue{n.SourceID, "icon_evidence_invalid"})
		}
	}
	return issues
}
