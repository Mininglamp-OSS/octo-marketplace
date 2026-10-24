// Package referenceindex rebuilds facts, never resources or publication evidence.
package referenceindex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type Resource struct {
	ID       string           `json:"id"`
	Type     model.PluginType `json:"type"`
	Embedded bool             `json:"embedded"`
}

type Snapshot struct {
	PluginID  string          `json:"plugin_id"`
	VersionID string          `json:"version_id"`
	Relations json.RawMessage `json:"relations"`
}

type Release struct {
	PluginID  string `json:"plugin_id"`
	VersionID string `json:"version_id"`
}

type Dataset struct {
	Resources []Resource `json:"resources"`
	Snapshots []Snapshot `json:"snapshots"`
	Releases  []Release  `json:"releases"`
}

type Edge struct {
	RelationID      string  `json:"relation_id"`
	Type            string  `json:"relation_type"`
	TargetPluginID  string  `json:"target_plugin_id"`
	TargetVersionID *string `json:"target_version_id,omitempty"`
}

type Plan struct {
	PluginID       string   `json:"plugin_id"`
	VersionID      string   `json:"version_id"`
	SnapshotDigest string   `json:"snapshot_digest"`
	Complete       bool     `json:"complete"`
	Reasons        []string `json:"reasons"`
	Edges          []Edge   `json:"edges"`
}

// Build does not infer historical scope from mutable current visibility. Its
// output is an index of snapshot facts, not an authorization or enablement grant.
func Build(data Dataset) []Plan {
	resources := map[string]Resource{}
	duplicates := map[string]bool{}
	for _, r := range data.Resources {
		if _, ok := resources[r.ID]; ok {
			duplicates[r.ID] = true
		}
		resources[r.ID] = r
	}
	versions := map[string]string{}
	for _, s := range data.Snapshots {
		if _, ok := versions[s.VersionID]; ok {
			duplicates["version:"+s.VersionID] = true
		}
		versions[s.VersionID] = s.PluginID
	}
	releases := map[string]string{}
	for _, r := range data.Releases {
		if owner, ok := releases[r.VersionID]; ok && owner != r.PluginID {
			duplicates["version:"+r.VersionID] = true
		}
		releases[r.VersionID] = r.PluginID
	}
	result := make([]Plan, 0, len(data.Snapshots))
	for _, s := range data.Snapshots {
		digest := sha256.Sum256(s.Relations)
		p := Plan{PluginID: s.PluginID, VersionID: s.VersionID, SnapshotDigest: hex.EncodeToString(digest[:]), Complete: true, Reasons: []string{}, Edges: []Edge{}}
		fail := func(reason string) { p.Complete = false; p.Reasons = append(p.Reasons, reason) }
		source, found := resources[s.PluginID]
		if !found || s.PluginID == "" || s.VersionID == "" || duplicates[s.PluginID] || duplicates["version:"+s.VersionID] {
			fail("ambiguous_source")
		}
		if source.Embedded {
			fail("embedded_source")
		}
		if source.Type != model.PluginTypeExpert && source.Type != model.PluginTypeExpertTeam && source.Type != model.PluginTypeSkill && source.Type != model.PluginTypeConnector {
			fail("invalid_source_type")
		}
		if !bytes.HasPrefix(bytes.TrimSpace(s.Relations), []byte("[")) || !unambiguousJSON(s.Relations) {
			fail("invalid_relations")
		}
		rows, err := model.DecodeSnapshotRelations(s.Relations)
		if err != nil {
			fail("invalid_relations")
		}
		seen := map[string]bool{}
		var fields []map[string]json.RawMessage
		if json.Unmarshal(s.Relations, &fields) != nil {
			fail("invalid_relations")
		}
		for i, r := range rows {
			statusPresent := false
			if i < len(fields) {
				for _, key := range []string{"status", "Status"} {
					if raw, ok := fields[i][key]; ok && string(bytes.TrimSpace(raw)) != "null" {
						statusPresent = true
					}
				}
			}
			if !statusPresent || (r.Status != 1 && r.Status != 0) || r.ID == "" || seen[r.ID] || r.SourcePluginID != s.PluginID || r.SourcePluginType != source.Type || r.TargetPluginID == "" || r.TargetPluginID == s.PluginID {
				fail("invalid_relation_identity")
				continue
			}
			seen[r.ID] = true
			if r.DeletedAt != nil || r.Status == 0 {
				continue
			}
			target, ok := resources[r.TargetPluginID]
			if !ok || duplicates[r.TargetPluginID] || target.Type != r.TargetPluginType {
				fail("invalid_target")
				continue
			}
			if target.Embedded {
				fail("embedded_target")
				continue
			}
			validKind := (source.Type == model.PluginTypeExpert && target.Type == model.PluginTypeSkill && r.Type == "expert_skill") || (source.Type == model.PluginTypeExpert && target.Type == model.PluginTypeConnector && r.Type == "expert_connector") || (source.Type == model.PluginTypeExpertTeam && target.Type == model.PluginTypeExpert && r.Type == "expert_team_expert")
			if !validKind {
				fail("invalid_relation_type")
				continue
			}
			if target.Type == model.PluginTypeConnector {
				if r.TargetVersionID != nil {
					fail("connector_version_forbidden")
					continue
				}
			} else {
				if r.TargetVersionID == nil || strings.TrimSpace(*r.TargetVersionID) == "" {
					fail("missing_pin")
					continue
				}
				v := *r.TargetVersionID
				if duplicates["version:"+v] || versions[v] != target.ID {
					fail("wrong_target_version")
					continue
				}
				if releases[v] != target.ID {
					fail("missing_release_evidence")
					continue
				}
			}
			p.Edges = append(p.Edges, Edge{r.ID, r.Type, r.TargetPluginID, r.TargetVersionID})
		}
		if !p.Complete {
			p.Edges = []Edge{}
		}
		sort.Strings(p.Reasons)
		p.Reasons = unique(p.Reasons)
		sort.Slice(p.Edges, func(i, j int) bool { return p.Edges[i].RelationID < p.Edges[j].RelationID })
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return fmt.Sprint(result[i].PluginID, "/", result[i].VersionID) < fmt.Sprint(result[j].PluginID, "/", result[j].VersionID)
	})
	return result
}

func unique(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// Historical JSON must not gain an identity through duplicate-key last-wins
// parsing. Bound nesting and bytes before constructing any relation evidence.
func unambiguousJSON(raw []byte) bool {
	if len(raw) > 16<<20 {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("nested evidence")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		mark, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch mark {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return errors.New("ambiguous evidence")
				}
				seen[s] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid evidence")
		}
		_, err = d.Token()
		return err
	}
	if value(0) != nil {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
