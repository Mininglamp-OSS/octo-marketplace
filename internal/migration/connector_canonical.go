package migration

import (
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"sort"
	"strings"
)

// Merge only equivalent complete resources in the same ownership/lifecycle
// boundary. Original source rows remain immutable evidence, not discarded data.
func canonicalizeConnectors(nodes map[string]Node) map[string]string {
	ids := []string{}
	for id, n := range nodes {
		if n.Plugin.Type == model.PluginTypeConnector {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := nodes[ids[i]], nodes[ids[j]]
		if a.Source.UpdatedAt.Equal(b.Source.UpdatedAt) {
			return ids[i] < ids[j]
		}
		return a.Source.UpdatedAt.Before(b.Source.UpdatedAt)
	})
	canonical := map[string]string{}
	remap := map[string]string{}
	for _, id := range ids {
		n := nodes[id]
		content, ok := connectorFingerprint(n)
		if !ok {
			continue
		}
		key := scope(n.Plugin) + "\x00" + n.Plugin.OwnerUID + "\x00" + lifecycle(n.Plugin) + "\x00" + strings.TrimSpace(n.Plugin.Name) + "\x00" + content
		if target, ok := canonical[key]; ok {
			keep := nodes[target]
			if keep.Evidence == nil {
				keep.Evidence = map[string]SourceEvidence{}
			}
			keep.Evidence[n.SourceID] = SourceEvidence{}
			nodes[target] = keep
			remap[id] = target
			delete(nodes, id)
		} else {
			canonical[key] = id
		}
	}
	for id, n := range nodes {
		rels := make([]model.PluginRelation, 0, len(n.Relations))
		seen := map[string]bool{}
		for _, e := range n.Relations {
			if target, ok := remap[e.TargetPluginID]; ok {
				e.TargetPluginID = target
			}
			key := e.Type + "\x00" + e.TargetPluginID
			if e.Type == "expert_connector" && seen[key] {
				continue
			}
			seen[key] = true
			rels = append(rels, e)
		}
		n.Relations = rels
		nodes[id] = n
	}
	return remap
}

// File paths, normalized MIME and bytes participate through the same complete
// resource fingerprint as other planning decisions. Only JSON spelling of the
// MCP document is ignored; supporting files remain byte-exact. Package metadata
// also participates, excluding generated attachment storage representations.
func connectorFingerprint(n Node) (string, bool) {
	r := Resource{Plugin: n.Plugin, Files: append([]File(nil), n.Files...)}
	found := false
	for i, f := range r.Files {
		if f.Path == "mcp.json" {
			canonical, err := libplugin.CanonicalJSON(f.Data)
			if err != nil {
				return "", false
			}
			r.Files[i].Data = canonical
			found = true
		}
	}
	var metadata map[string]json.RawMessage
	if !found || json.Unmarshal(n.Plugin.Package, &metadata) != nil || metadata == nil {
		return "", false
	}
	delete(metadata, "attachments")
	raw, _ := json.Marshal(metadata)
	canonical, err := libplugin.CanonicalJSON(raw)
	if err != nil {
		return "", false
	}
	manifest, err := libplugin.CanonicalJSON(n.Plugin.Manifest)
	if err != nil {
		return "", false
	}
	return fingerprint(r) + ":" + digest(canonical) + ":" + digest(manifest), true
}
