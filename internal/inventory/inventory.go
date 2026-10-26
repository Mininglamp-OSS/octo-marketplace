// Package inventory performs operator-only, read-only inventory. It does not
// infer publication, ownership, migration policy or object existence.
package inventory

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Plugin struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Owner       string          `json:"owner"`
	Space       string          `json:"space"`
	Visibility  string          `json:"visibility"`
	Listing     string          `json:"listing"`
	Embedded    bool            `json:"embedded"`
	Deleted     bool            `json:"deleted"`
	Current     string          `json:"current"`
	Published   string          `json:"published"`
	Attachments json.RawMessage `json:"attachments,omitempty"`
	Keys        json.RawMessage `json:"keys,omitempty"`
}
type Version struct {
	ID          string          `json:"id"`
	PluginID    string          `json:"plugin_id"`
	Schema      int             `json:"schema"`
	Attachments json.RawMessage `json:"attachments,omitempty"`
	Keys        json.RawMessage `json:"keys,omitempty"`
	Artifacts   json.RawMessage `json:"artifacts,omitempty"`
	Relations   json.RawMessage `json:"relations,omitempty"`
}
type Relation struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
}
type Release struct {
	PluginID  string `json:"plugin_id"`
	VersionID string `json:"version_id"`
}
type Input struct {
	Plugins   []Plugin   `json:"plugins"`
	Versions  []Version  `json:"versions"`
	Relations []Relation `json:"relations"`
	Releases  []Release  `json:"releases"`
}
type Resource struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Owner        string   `json:"owner"`
	Space        string   `json:"space"`
	Visibility   string   `json:"visibility"`
	Listing      string   `json:"listing"`
	Embedded     bool     `json:"embedded"`
	Deleted      bool     `json:"deleted"`
	Current      string   `json:"current"`
	Published    string   `json:"published"`
	Parents      []string `json:"parents"`
	VersionCount int      `json:"version_count"`
	ReleaseCount int      `json:"release_count"`
}
type Finding struct {
	Code      string `json:"code"`
	PluginID  string `json:"plugin_id"`
	VersionID string `json:"version_id,omitempty"`
	RelatedID string `json:"related_id,omitempty"`
	// Index locates attachment metadata without exposing names, paths or content.
	Index *int `json:"attachment_index,omitempty"`
}

// Distribution counts metadata as observed, without inferring publication from a
// listing state. Deleted rows remain separate so totals cover the entire input.
type Distribution struct {
	Visibility string `json:"visibility"`
	Listing    string `json:"listing"`
	Identity   string `json:"identity"`
	Deleted    bool   `json:"deleted"`
	Count      int    `json:"count"`
}

type Report struct {
	Distribution       []Distribution `json:"distribution"`
	Format             int            `json:"format"`
	Scope              string         `json:"scope"`
	ObjectVerification string         `json:"object_verification"`
	Resources          []Resource     `json:"resources"`
	Relations          []Relation     `json:"relations"`
	SameNameGroups     [][]string     `json:"same_name_groups"`
	Findings           []Finding      `json:"findings"`
	Counts             map[string]int `json:"finding_counts"`
}

// Build emits only identifiers, lifecycle metadata and fixed diagnostic codes.
// In particular names, package content, relation data, keys and URLs never leave
// the analyzer. Same-name groups are candidates, not automatic reuse decisions.
func Build(in Input) Report {
	r := Report{Distribution: buildDistribution(in.Plugins), Format: 1, Scope: "all_marketplace_resources_including_deleted", ObjectVerification: "not_checked", Resources: []Resource{}, Relations: append([]Relation{}, in.Relations...), SameNameGroups: [][]string{}, Findings: []Finding{}, Counts: map[string]int{}}
	add := func(code, p, v, related string) {
		r.Findings = append(r.Findings, Finding{Code: code, PluginID: p, VersionID: v, RelatedID: related})
	}
	plugins := map[string]Plugin{}
	versions := map[string]Version{}
	releases := map[string]map[string]bool{}
	parents := map[string][]string{}
	names := map[string][]string{}
	counts := map[string]int{}
	for _, p := range in.Plugins {
		plugins[p.ID] = p
		if !p.Deleted {
			key := p.Type + "\x00" + p.Name
			names[key] = append(names[key], p.ID)
		}
	}
	for _, v := range in.Versions {
		versions[v.ID] = v
		counts[v.PluginID]++
		if _, ok := plugins[v.PluginID]; !ok {
			add("orphan_version", v.PluginID, v.ID, "")
		}
		if v.Schema != 1 {
			add("historical_schema_unknown", v.PluginID, v.ID, "")
		}
		if len(v.Artifacts) == 0 || string(v.Artifacts) == "null" {
			add("artifact_inventory_unknown", v.PluginID, v.ID, "")
		} else {
			var a []json.RawMessage
			if json.Unmarshal(v.Artifacts, &a) != nil {
				add("artifact_inventory_invalid", v.PluginID, v.ID, "")
			}
		}
		inspectAttachments(&r, v.PluginID, v.ID, v.Attachments, v.Keys)
		// No current-version fallback or fabricated pin is possible here.
		var rels []map[string]json.RawMessage
		if len(v.Relations) == 0 || string(v.Relations) == "null" || json.Unmarshal(v.Relations, &rels) != nil {
			add("historical_relations_unknown", v.PluginID, v.ID, "")
		} else {
			for _, rel := range rels {
				var target, pin string
				_ = json.Unmarshal(rel["target_plugin_id"], &target)
				if target == "" {
					_ = json.Unmarshal(rel["TargetPluginID"], &target)
				}
				_ = json.Unmarshal(rel["target_version_id"], &pin)
				if p, ok := plugins[target]; !ok {
					add("historical_target_unknown", v.PluginID, v.ID, target)
				} else if p.Type != "connector" && pin == "" {
					add("historical_dependency_unpinned", v.PluginID, v.ID, target)
				}
			}
		}
	}
	for _, rel := range in.Relations {
		source, sok := plugins[rel.Source]
		target, tok := plugins[rel.Target]
		if !sok || !tok {
			add("dangling_relation", rel.Source, "", rel.Target)
			continue
		}
		parents[rel.Target] = append(parents[rel.Target], rel.Source)
		if source.Deleted || target.Deleted {
			add("relation_to_deleted_resource", rel.Source, "", rel.Target)
		}
		if target.Embedded && (source.Space != target.Space || source.Owner != target.Owner) {
			add("embedded_ownership_mismatch", rel.Target, "", rel.Source)
		}
	}
	for _, release := range in.Releases {
		v, ok := versions[release.VersionID]
		if !ok || v.PluginID != release.PluginID {
			add("release_snapshot_mismatch", release.PluginID, release.VersionID, "")
			continue
		}
		if releases[release.PluginID] == nil {
			releases[release.PluginID] = map[string]bool{}
		}
		releases[release.PluginID][release.VersionID] = true
	}
	for _, p := range in.Plugins {
		ps := parents[p.ID]
		sort.Strings(ps)
		if ps == nil {
			ps = []string{}
		}
		r.Resources = append(r.Resources, Resource{p.ID, p.Type, p.Owner, p.Space, p.Visibility, p.Listing, p.Embedded, p.Deleted, p.Current, p.Published, ps, counts[p.ID], len(releases[p.ID])})
		if p.Embedded && len(ps) == 0 {
			add("embedded_without_parent", p.ID, "", "")
		}
		if p.Embedded && len(ps) > 1 {
			add("embedded_multiple_parents", p.ID, "", "")
		}
		if p.Owner == "" {
			add("owner_unknown", p.ID, "", "")
		}
		if p.Type != "connector" {
			if p.Current == "" {
				add("current_snapshot_missing", p.ID, "", "")
			} else if v, ok := versions[p.Current]; !ok || v.PluginID != p.ID {
				add("current_snapshot_mismatch", p.ID, p.Current, "")
			}
			if p.Listing == "published" && p.Published == "" {
				add("listed_publication_unknown", p.ID, "", "")
			}
			if p.Published != "" && !releases[p.ID][p.Published] {
				add("published_release_missing", p.ID, p.Published, "")
			}
		}
		inspectAttachments(&r, p.ID, "", p.Attachments, p.Keys)
	}
	for _, ids := range names {
		if len(ids) > 1 {
			sort.Strings(ids)
			r.SameNameGroups = append(r.SameNameGroups, ids)
		}
	}
	sort.Slice(r.Resources, func(i, j int) bool { return r.Resources[i].ID < r.Resources[j].ID })
	sort.Slice(r.Relations, func(i, j int) bool { return r.Relations[i].ID < r.Relations[j].ID })
	sort.Slice(r.SameNameGroups, func(i, j int) bool {
		return strings.Join(r.SameNameGroups[i], "\x00") < strings.Join(r.SameNameGroups[j], "\x00")
	})
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		return fmt.Sprintf("%s/%s/%s/%s", a.PluginID, a.VersionID, a.Code, a.RelatedID) < fmt.Sprintf("%s/%s/%s/%s", b.PluginID, b.VersionID, b.Code, b.RelatedID)
	})
	for _, f := range r.Findings {
		r.Counts[f.Code]++
	}
	return r
}

func inspectAttachments(r *Report, p, v string, raw, keys json.RawMessage) {
	add := func(code string, index *int) {
		r.Findings = append(r.Findings, Finding{Code: code, PluginID: p, VersionID: v, Index: index})
	}
	if len(raw) == 0 || string(raw) == "null" {
		add("attachment_metadata_unknown", nil)
		return
	}
	var attachments []struct {
		Path   string `json:"path"`
		Type   string `json:"content_type"`
		Legacy string `json:"storage_uri"`
		Hash   string `json:"content_hash"`
		Size   *int64 `json:"content_size"`
	}
	if json.Unmarshal(raw, &attachments) != nil {
		add("attachment_metadata_invalid", nil)
		return
	}
	sidecar := map[string]string{}
	if len(keys) > 0 && string(keys) != "null" {
		if json.Unmarshal(keys, &sidecar) != nil {
			add("attachment_sidecar_invalid", nil)
		}
	}
	seen := map[string]bool{}
	for i, a := range attachments {
		idx := i
		if a.Path == "" || seen[a.Path] {
			add("attachment_path_invalid_or_duplicate", &idx)
		}
		seen[a.Path] = true
		if a.Type == "storage" {
			if sidecar[a.Path] == "" && a.Legacy == "" {
				add("storage_key_missing", &idx)
			} else {
				add("storage_object_not_checked", &idx)
			}
			if a.Size == nil || *a.Size < 0 || a.Hash == "" {
				add("storage_integrity_metadata_missing", &idx)
			}
		}
	}
	for path := range sidecar {
		if !seen[path] {
			add("orphan_attachment_key", nil)
		}
	}
}

// Unknown labels are bucketed rather than copied into aggregated report fields.
// Legacy public is explicit; converting it to system is a migration decision.
func buildDistribution(plugins []Plugin) []Distribution {
	type key struct {
		visibility, listing, identity string
		deleted                       bool
	}
	counts := map[key]int{}
	for _, p := range plugins {
		visibility := p.Visibility
		switch visibility {
		case "private", "space", "system":
		case "public":
			visibility = "legacy_public"
		default:
			visibility = "unknown"
		}
		listing := p.Listing
		switch listing {
		case "draft", "published", "delisted":
		default:
			listing = "unknown"
		}
		identity := "top_level"
		if p.Embedded {
			identity = "embedded"
		}
		counts[key{visibility, listing, identity, p.Deleted}]++
	}
	result := []Distribution{}
	for k, n := range counts {
		result = append(result, Distribution{k.visibility, k.listing, k.identity, k.deleted, n})
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Visibility != b.Visibility {
			return a.Visibility < b.Visibility
		}
		if a.Listing != b.Listing {
			return a.Listing < b.Listing
		}
		if a.Identity != b.Identity {
			return a.Identity < b.Identity
		}
		return !a.Deleted && b.Deleted
	})
	return result
}
