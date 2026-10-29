package migration

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

var mcpAliasName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Rename only target MCP server keys that collide within an Expert. The source
// document remains frozen separately; endpoints, auth, extensions and scripts
// are never changed. Every reference to a shared Connector sees the same alias.
func aliasConnectorServerKeys(nodes map[string]Node, snapshot string) []Issue {
	aliases := map[string]map[string]string{}
	used := map[string]bool{}
	for _, n := range nodes {
		if n.Plugin.Type == model.PluginTypeConnector {
			for _, f := range n.Files {
				if f.Path == "mcp.json" {
					_, servers, _ := decodeMCPDocument(f.Data)
					for key := range servers {
						used[key] = true
					}
				}
			}
		}
	}
	for _, expert := range nodes {
		if expert.Plugin.Type != model.PluginTypeExpert {
			continue
		}
		owners := map[string]map[string]bool{}
		for _, edge := range expert.Relations {
			if edge.Type != "expert_connector" {
				continue
			}
			for _, f := range nodes[edge.TargetPluginID].Files {
				if f.Path != "mcp.json" {
					continue
				}
				_, servers, _ := decodeMCPDocument(f.Data)
				for key := range servers {
					if owners[key] == nil {
						owners[key] = map[string]bool{}
					}
					owners[key][edge.TargetPluginID] = true
				}
			}
		}
		for key, ids := range owners {
			if len(ids) > 1 {
				for id := range ids {
					if aliases[id] == nil {
						aliases[id] = map[string]string{}
					}
					aliases[id][key] = ""
				}
			}
		}
	}
	ids := make([]string, 0, len(aliases))
	for id := range aliases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var issues []Issue
	for _, id := range ids {
		n := nodes[id]
		keys := make([]string, 0, len(aliases[id]))
		for key := range aliases[id] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			for attempt := 0; ; attempt++ {
				alias := mcpLabel(key, "mcp") + "-" + mcpLabel(n.Source.Name, "source") + "-" + digest([]byte(id + ":" + key + ":" + strconv.Itoa(attempt)))[:12]
				if !used[alias] {
					used[alias] = true
					aliases[id][key] = alias
					break
				}
			}
		}
		for i, f := range n.Files {
			if f.Path != "mcp.json" {
				continue
			}
			n.MCPOriginal = append([]byte(nil), f.Data...)
			n.MCPAliases = aliases[id]
			projected, ok := projectMCPAliases(n.MCPOriginal, n.MCPAliases)
			if !ok {
				issues = append(issues, Issue{n.SourceID, "mcp_alias_invalid"})
				break
			}
			n.Files[i].Data = projected
			if code := prepareFiles(&n, snapshot); code != "" {
				issues = append(issues, Issue{n.SourceID, code})
			}
			break
		}
		nodes[id] = n
	}
	return issues
}

func mcpLabel(value, fallback string) string {
	var out []byte
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			out = append(out, byte(r))
		} else if len(out) > 0 && out[len(out)-1] != '-' {
			out = append(out, '-')
		}
		if len(out) >= 20 {
			break
		}
	}
	if label := strings.Trim(string(out), "-"); label != "" {
		return label
	}
	return fallback
}

func decodeMCPDocument(raw []byte) (map[string]json.RawMessage, map[string]json.RawMessage, bool) {
	var root, servers map[string]json.RawMessage
	if model.ValidateUniqueJSONFields(raw) != nil || json.Unmarshal(raw, &root) != nil || root == nil || json.Unmarshal(root["mcpServers"], &servers) != nil || servers == nil {
		return nil, nil, false
	}
	return root, servers, true
}

func projectMCPAliases(original []byte, aliases map[string]string) ([]byte, bool) {
	root, servers, ok := decodeMCPDocument(original)
	if !ok || len(aliases) == 0 {
		return nil, false
	}
	for key, alias := range aliases {
		server, found := servers[key]
		if !found || !mcpAliasName.MatchString(alias) {
			return nil, false
		}
		if _, occupied := servers[alias]; occupied {
			return nil, false
		}
		servers[alias] = server
		delete(servers, key)
	}
	root["mcpServers"], _ = json.Marshal(servers)
	out, err := json.Marshal(root)
	return out, err == nil
}

func validateConnectorAliasProjection(n Node) string {
	if len(n.MCPAliases) == 0 && len(n.MCPOriginal) == 0 {
		return ""
	}
	if n.Plugin.Type != model.PluginTypeConnector {
		return "mcp_alias_invalid"
	}
	expected, ok := projectMCPAliases(n.MCPOriginal, n.MCPAliases)
	if !ok {
		return "mcp_alias_invalid"
	}
	want, _ := libplugin.CanonicalJSON(expected)
	matchedFile, matchedPackage := false, false
	for _, f := range n.Files {
		if f.Path == "mcp.json" {
			got, err := libplugin.CanonicalJSON(f.Data)
			matchedFile = err == nil && bytes.Equal(got, want)
		}
	}
	var pkg struct {
		Attachments []struct {
			Path string `json:"path"`
			Raw  string `json:"raw_content"`
		} `json:"attachments"`
	}
	if json.Unmarshal(n.Plugin.Package, &pkg) == nil {
		for _, f := range pkg.Attachments {
			if f.Path == "mcp.json" {
				got, err := libplugin.CanonicalJSON([]byte(f.Raw))
				matchedPackage = err == nil && bytes.Equal(got, want)
			}
		}
	}
	if !matchedFile || !matchedPackage {
		return "mcp_alias_projection_mismatch"
	}
	return ""
}
