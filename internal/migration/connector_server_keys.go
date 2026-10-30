package migration

import (
	"encoding/json"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Default pinned installation merges MCP documents per Expert by server key,
// unlike Workspace Connector installation which uses distinct display names.
// Never admit a newly retained graph that the former cannot merge. Team members
// have separate default MCP documents, so keys across members do not conflict.
func connectorServerKeyIssues(p Plan) []Issue {
	nodes := map[string]Node{}
	for _, n := range p.Nodes {
		nodes[n.Plugin.ID] = n
	}
	var issues []Issue
	for _, expert := range p.Nodes {
		if expert.Plugin.Type != model.PluginTypeExpert {
			continue
		}
		owners := map[string]string{}
		conflict := false
		for _, edge := range expert.Relations {
			if edge.Type != "expert_connector" {
				continue
			}
			// Inspect the package consumed by the default installer, rather than
			// assuming the separate planned file inventory has identical JSON.
			var pkg struct {
				Attachments []struct {
					Path string `json:"path"`
					Raw  string `json:"raw_content"`
				} `json:"attachments"`
			}
			if json.Unmarshal(nodes[edge.TargetPluginID].Plugin.Package, &pkg) != nil {
				issues = append(issues, Issue{expert.SourceID, "mcp_invalid"})
				continue
			}
			for _, f := range pkg.Attachments {
				if f.Path != "mcp.json" {
					continue
				}
				var doc struct {
					Servers map[string]json.RawMessage `json:"mcpServers"`
				}
				if json.Unmarshal([]byte(f.Raw), &doc) != nil {
					issues = append(issues, Issue{expert.SourceID, "mcp_invalid"})
					continue
				}
				for name := range doc.Servers {
					if owner, ok := owners[name]; ok && owner != edge.TargetPluginID {
						conflict = true
					}
					owners[name] = edge.TargetPluginID
				}
			}
		}
		if conflict {
			issues = append(issues, Issue{expert.SourceID, "connector_server_key_conflict"})
		}
	}
	return issues
}
