package migration

import (
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"sort"
)

func extractMCP(n *Node, snapshot string) ([]Node, string) {
	out := []Node{}
	files := []File{}
	for _, f := range n.Files {
		if f.Path != "mcp.json" {
			files = append(files, f)
			continue
		}
		if pluginsvc.ValidateMigrationMCP(string(f.Data)) != nil {
			return nil, "mcp_invalid"
		}
		var cfg struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal(f.Data, &cfg) != nil {
			return nil, "mcp_invalid"
		}
		var document map[string]json.RawMessage
		if json.Unmarshal(f.Data, &document) != nil {
			return nil, "mcp_invalid"
		}
		names := []string{}
		for name := range cfg.Servers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			raw := cfg.Servers[name]
			id := stable(snapshot, "connector", n.SourceID, name)
			cp := n.Plugin
			cp.ID = id
			cp.Type = model.PluginTypeConnector
			cp.Name = name
			cp.CurrentVersionID = nil
			cp.CurrentVersion = nil
			cp.Manifest, _ = json.Marshal(map[string]any{"$schema": "cowork-plugin-manifest-2.0.json", "plugin_name": name, "plugin_type": "connector", "name": name, "description": "Extracted MCP configuration", "labels": []string{}, "examples": []any{}})
			cp.Package = json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","connector":{"type":"mcp","source":"migration"},"attachments":[]}`)
			// Root and server extension fields belong to the original effective
			// configuration. Each split Connector retains those same values.
			split := make(map[string]json.RawMessage, len(document))
			for key, value := range document {
				split[key] = value
			}
			split["mcpServers"], _ = json.Marshal(map[string]json.RawMessage{name: raw})
			data, _ := json.Marshal(split)
			cn := Node{SourceID: n.SourceID, Source: n.Source, SourcePublishedVersionID: n.SourcePublishedVersionID, SourceRelease: n.SourceRelease, Plugin: cp, Clone: true, Release: n.Release, Files: []File{{Path: "mcp.json", MIME: "application/json", Data: data}}}
			if code := prepareFiles(&cn, snapshot); code != "" {
				return nil, code
			}
			out = append(out, cn)
			n.Relations = append(n.Relations, model.PluginRelation{ID: stable(snapshot, "mcp-relation", n.Plugin.ID, name), SourcePluginID: n.Plugin.ID, SourcePluginType: model.PluginTypeExpert, TargetPluginID: id, TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1, CreatedAt: n.Plugin.CreatedAt, UpdatedAt: n.Plugin.UpdatedAt})
		}
	}
	n.Files = files
	return out, ""
}
