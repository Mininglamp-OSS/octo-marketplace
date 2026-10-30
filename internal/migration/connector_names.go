package migration

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"golang.org/x/text/unicode/norm"
)

// Distinct connectors retain their own IDs, files and references. Disambiguate
// display/installation names only; never rewrite mcp.json server keys or values.
func nameDistinctConnectors(nodes map[string]Node, snapshot string) []Issue {
	peers := map[string]map[string]bool{}
	rename := map[string]bool{}
	for _, ids := range connectorClosures(nodes) {
		groups := map[string][]string{}
		for _, id := range ids {
			groups[normalized(nodes[id].Plugin.Name)] = append(groups[normalized(nodes[id].Plugin.Name)], id)
			if peers[id] == nil {
				peers[id] = map[string]bool{}
			}
			for _, peer := range ids {
				if peer != id {
					peers[id][peer] = true
				}
			}
		}
		for _, group := range groups {
			if len(group) > 1 {
				for _, id := range group {
					rename[id] = true
				}
			}
		}
	}
	ids := make([]string, 0, len(rename))
	for id := range rename {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var issues []Issue
	for _, id := range ids {
		n := nodes[id]
		source := strings.TrimSpace(n.Source.Name)
		if source == "" {
			source = n.SourceID
		}
		used := func(name string) bool {
			for peer := range peers[id] {
				if normalized(nodes[peer].Plugin.Name) == normalized(name) {
					return true
				}
			}
			return false
		}
		name := connectorSourceName(n.Plugin.Name, source, "")
		for attempt := 0; used(name); attempt++ {
			suffix := digest([]byte(id))[:12]
			if attempt > 0 {
				suffix += fmt.Sprintf("-%d", attempt)
			}
			name = connectorSourceName(n.Plugin.Name, source, suffix)
		}
		var manifest map[string]json.RawMessage
		if json.Unmarshal(n.Plugin.Manifest, &manifest) != nil || manifest == nil {
			issues = append(issues, Issue{n.SourceID, "document_invalid"})
			continue
		}
		n.Plugin.Name = name
		manifest["name"], _ = json.Marshal(name)
		manifest["plugin_name"], _ = json.Marshal(name)
		n.Plugin.Manifest, _ = json.Marshal(manifest)
		if code := prepareFiles(&n, snapshot); code != "" {
			issues = append(issues, Issue{n.SourceID, code})
		}
		nodes[id] = n
	}
	return issues
}

// A connector can occur in several closures. Only resources that actually
// co-occur constrain one another's installation names.
func connectorClosures(nodes map[string]Node) map[string][]string {
	out := map[string][]string{}
	for id, root := range nodes {
		if root.Plugin.Type != model.PluginTypeExpert && root.Plugin.Type != model.PluginTypeExpertTeam {
			continue
		}
		seen := map[string]bool{}
		var visit func(string)
		visit = func(current string) {
			if seen[current] {
				return
			}
			seen[current] = true
			n, ok := nodes[current]
			if !ok {
				return
			}
			if n.Plugin.Type == model.PluginTypeConnector {
				out[id] = append(out[id], current)
			}
			for _, edge := range n.Relations {
				visit(edge.TargetPluginID)
			}
		}
		visit(id)
	}
	return out
}

func connectorSourceName(base, source, discriminator string) string {
	clean := func(s string) []rune {
		s = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return '-'
			}
			return r
		}, norm.NFKC.String(strings.TrimSpace(s)))
		r := []rune(s)
		if len(r) > 50 {
			r = r[:50]
		}
		return r
	}
	b, s := clean(base), clean(source)
	for {
		name := string(b) + "-" + string(s)
		if discriminator != "" {
			name += "-" + discriminator
		}
		// Fleet names allow 128 runes; normalized database names allow 160.
		if len([]rune(name)) <= 128 && len([]rune(normalized(name))) <= model.PluginNameMaxLength {
			return name
		}
		if len(b) > 1 {
			b = b[:len(b)-1]
		} else {
			s = s[:len(s)-1]
		}
	}
}
