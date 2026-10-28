// Package stockmigration converts existing legacy graphs, never uploaded bundles.
package stockmigration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

type Resource struct {
	PendingReview      bool                   `json:"pending_review"`
	PublishedVersionID *string                `json:"published_version_id,omitempty"`
	PublishedName      string                 `json:"published_name,omitempty"`
	Plugin             model.Plugin           `json:"plugin"`
	Relations          []model.PluginRelation `json:"relations"`
}
type Dataset struct {
	ResolvedSkillFiles map[string]map[string]string `json:"resolved_skill_files,omitempty"`
	FileExceptions     map[string]string            `json:"file_exceptions,omitempty"`
	Resources          []Resource                   `json:"resources"`
	Versions           []model.PluginVersion        `json:"versions"`
	ExistingMappings   []Mapping                    `json:"existing_mappings,omitempty"`
	MigratedSourceIDs  []string                     `json:"migrated_source_ids,omitempty"`
	Releases           []model.PluginVersionRelease `json:"releases"`
}
type Exception struct {
	ResourceID string `json:"resource_id"`
	Code       string `json:"code"`
}
type Mapping struct {
	SourceID  string           `json:"source_id"`
	TargetID  string           `json:"target_id"`
	VersionID string           `json:"version_id,omitempty"`
	Type      model.PluginType `json:"type"`
	Label     string           `json:"label,omitempty"`
}
type Node struct {
	Mapping
	Plugin            model.Plugin           `json:"-"`
	Relations         []model.PluginRelation `json:"-"`
	Artifacts         json.RawMessage        `json:"-"`
	Clone             bool                   `json:"-"`
	PreviousPublished *string                `json:"-"`
	Release           bool                   `json:"-"`
}
type Unit struct {
	Dependencies []string    `json:"dependencies"`
	RootID       string      `json:"root_id"`
	Nodes        []Node      `json:"mappings"`
	Exceptions   []Exception `json:"exceptions"`
}
type Plan struct {
	Digest         string      `json:"digest"`
	InputResources int         `json:"input_resources"`
	Units          []Unit      `json:"units"`
	Exceptions     []Exception `json:"exceptions"`
}

func hash(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func identity(kind, root, source string) string {
	return "sm-" + hash([]string{kind, root, source})[:40]
}

// Build only accepts facts from the supplied consistent inventory. No mutable
// current-version fallback supplies missing dependency pins.
func Build(d Dataset) Plan {
	d.Resources = append([]Resource(nil), d.Resources...)
	d.Versions = append([]model.PluginVersion(nil), d.Versions...)
	d.Releases = append([]model.PluginVersionRelease(nil), d.Releases...)
	sort.Slice(d.Resources, func(i, j int) bool { return d.Resources[i].Plugin.ID < d.Resources[j].Plugin.ID })
	sort.Slice(d.Versions, func(i, j int) bool { return d.Versions[i].ID < d.Versions[j].ID })
	sort.Slice(d.Releases, func(i, j int) bool { return d.Releases[i].VersionID < d.Releases[j].VersionID })
	p := Plan{Digest: hash(d), InputResources: len(d.Resources), Units: []Unit{}, Exceptions: []Exception{}}
	all := map[string]Resource{}
	duplicates := map[string]bool{}
	for _, r := range d.Resources {
		if _, ok := all[r.Plugin.ID]; ok {
			duplicates[r.Plugin.ID] = true
		}
		all[r.Plugin.ID] = r
	}
	owned := map[string]string{}
	knownMappings := map[string]Mapping{}
	for _, m := range d.ExistingMappings {
		if m.VersionID != "" && released(d, m.TargetID, m.VersionID) {
			if previous, exists := knownMappings[m.SourceID]; exists && (previous.TargetID != m.TargetID || previous.VersionID != m.VersionID) {
				p.Exceptions = append(p.Exceptions, Exception{m.SourceID, "ambiguous_audited_mapping"})
				continue
			}
			knownMappings[m.SourceID] = m
		}
	}
	forceTarget := map[string]bool{}
	for _, r := range d.Resources {
		if r.Plugin.Type == model.PluginTypeExpertTeam || r.Plugin.Type == model.PluginTypeExpert {
			for _, e := range r.Relations {
				if e.DeletedAt == nil && e.Status == 1 && e.Type != "expert_connector" && e.TargetVersionID == nil {
					forceTarget[e.TargetPluginID] = true
				}
			}
		}
	}
	roots := append([]Resource(nil), d.Resources...)
	sort.SliceStable(roots, func(i, j int) bool {
		rank := func(kind model.PluginType) int {
			if kind == model.PluginTypeSkill {
				return 0
			}
			if kind == model.PluginTypeExpert {
				return 1
			}
			if kind == model.PluginTypeExpertTeam {
				return 2
			}
			return 3
		}
		if rank(roots[i].Plugin.Type) != rank(roots[j].Plugin.Type) {
			return rank(roots[i].Plugin.Type) < rank(roots[j].Plugin.Type)
		}
		return roots[i].Plugin.ID < roots[j].Plugin.ID
	})
	plannedByRoot := map[string]string{}
	for _, root := range roots {
		if root.Plugin.IsEmbedded || (root.Plugin.Type != model.PluginTypeExpert && root.Plugin.Type != model.PluginTypeExpertTeam && (root.Plugin.Type != model.PluginTypeSkill || !forceTarget[root.Plugin.ID])) || root.Plugin.DeletedAt != nil || root.Plugin.Status != 1 {
			continue
		}
		// Existing pure reference graphs do not need content conversion.
		legacy := forceTarget[root.Plugin.ID] && knownMappings[root.Plugin.ID].VersionID == ""
		var detect func(string, map[string]bool)
		detect = func(id string, seen map[string]bool) {
			if seen[id] {
				return
			}
			seen[id] = true
			r := all[id]
			if r.Plugin.Type == model.PluginTypeExpert {
				if hasAttachment(r.Plugin.Package, "mcp.json") {
					legacy = true
				}
			}
			for _, e := range r.Relations {
				if e.Status == 1 && e.DeletedAt == nil && e.Type != "expert_connector" && e.TargetVersionID == nil {
					legacy = true
				}
				if target, ok := all[e.TargetPluginID]; ok && target.Plugin.IsEmbedded {
					legacy = true
					detect(target.Plugin.ID, seen)
				}
			}
		}
		detect(root.Plugin.ID, map[string]bool{})
		if !legacy {
			continue
		}
		unit := Unit{Dependencies: []string{}, RootID: root.Plugin.ID, Nodes: []Node{}, Exceptions: []Exception{}}
		fail := func(id, code string) { unit.Exceptions = append(unit.Exceptions, Exception{id, code}) }
		vis := model.NormalizeLegacyVisibility(root.Plugin.Visibility)
		if root.Plugin.OwnerUID == "" || (vis != model.PluginVisibilitySystem && (root.Plugin.SpaceID == nil || *root.Plugin.SpaceID == "")) || (vis != model.PluginVisibilityPrivate && vis != model.PluginVisibilitySpace && vis != model.PluginVisibilitySystem) {
			fail(root.Plugin.ID, "ambiguous_owner_or_scope")
		}
		if root.Plugin.ListingState != "draft" && root.Plugin.ListingState != "published" && root.Plugin.ListingState != "delisted" {
			fail(root.Plugin.ID, "unknown_listing_state")
		}
		// Never publish a mutable draft that has diverged from the active release.
		if root.PublishedVersionID != nil && (root.Plugin.CurrentVersionID == nil || *root.PublishedVersionID != *root.Plugin.CurrentVersionID) {
			fail(root.Plugin.ID, "unpublished_current_changes")
		}
		hasCurrent := false
		for _, v := range d.Versions {
			if root.Plugin.CurrentVersionID != nil && v.ID == *root.Plugin.CurrentVersionID && v.PluginID == root.Plugin.ID {
				hasCurrent = true
				if v.ManifestHash != root.Plugin.ManifestHash || v.PluginHash != root.Plugin.PluginHash {
					fail(root.Plugin.ID, "snapshot_content_mismatch")
				}
			}
		}
		if root.PendingReview {
			fail(root.Plugin.ID, "pending_review")
		}
		if !hasCurrent {
			fail(root.Plugin.ID, "missing_root_snapshot")
		}
		nodes := map[string]Node{}
		visiting := map[string]bool{}
		var convert func(string, bool) (Mapping, bool)
		convert = func(id string, isRoot bool) (Mapping, bool) {
			if node, ok := nodes[id]; ok {
				return node.Mapping, true
			}
			r, ok := all[id]
			if !ok || duplicates[id] || visiting[id] {
				fail(id, "missing_or_ambiguous_resource")
				return Mapping{}, false
			}
			visiting[id] = true
			defer delete(visiting, id)
			old := r.Plugin
			if old.DeletedAt != nil || old.Status != 1 {
				fail(id, "inactive_resource")
				return Mapping{}, false
			}
			if !isRoot && (!old.IsEmbedded || old.OwnerUID != root.Plugin.OwnerUID || !sameSpace(old.SpaceID, root.Plugin.SpaceID)) {
				fail(id, "ambiguous_embedded_ownership")
				return Mapping{}, false
			}
			if previous, ok := owned[id]; ok && previous != root.Plugin.ID {
				fail(id, "embedded_shared_across_roots")
				return Mapping{}, false
			}
			if !isRoot {
				owned[id] = root.Plugin.ID
			}
			targetID := id
			if !isRoot {
				targetID = identity("resource", root.Plugin.ID, id)
			}
			if !isRoot {
				if _, exists := all[targetID]; exists {
					fail(id, "target_identity_collision")
					return Mapping{}, false
				}
			}
			cp := old
			cp.ID = targetID
			cp.IsEmbedded = false
			cp.OwnerUID = root.Plugin.OwnerUID
			cp.SpaceID = root.Plugin.SpaceID
			cp.Visibility = vis
			cp.ListingState = root.Plugin.ListingState
			cp.CurrentVersionID = nil

			relations := []model.PluginRelation{}
			skillCount := 0
			for _, e := range r.Relations {
				if e.Type == "expert_skill" && e.Status == 1 && e.DeletedAt == nil {
					skillCount++
				}
			}
			if skillCount > 20 && old.Type == model.PluginTypeExpert {
				fail(id, "too_many_expert_dependencies")
			}
			if len(r.Relations) > 100 {
				fail(id, "too_many_relations")
			}
			for _, edge := range r.Relations {
				if edge.DeletedAt != nil || edge.Status == 0 {
					continue
				}
				if edge.Status != 1 {
					fail(id, "invalid_relation_status")
					continue
				}
				child, ok := all[edge.TargetPluginID]
				if !ok {
					fail(id, "missing_target")
					continue
				}
				if edge.Type == "expert_connector" && old.Type == model.PluginTypeExpert && child.Plugin.Type == model.PluginTypeConnector && !child.Plugin.IsEmbedded {
					if edge.TargetVersionID != nil || !audience(cp, child.Plugin) {
						fail(id, "invalid_existing_connector")
						continue
					}
					out := edge
					out.ID = identity("relation", targetID, edge.ID)
					out.SourcePluginID = targetID
					out.SourcePluginType = cp.Type
					out.TargetPluginType = model.PluginTypeConnector
					out.Data = nil
					relations = append(relations, out)
					continue
				}
				expected := model.PluginTypeSkill
				kind := "expert_skill"
				if old.Type == model.PluginTypeExpertTeam {
					expected = model.PluginTypeExpert
					kind = "expert_team_expert"
				}
				if edge.Type != kind || child.Plugin.Type != expected {
					fail(id, "invalid_legacy_relation")
					continue
				}
				out := edge
				normalized, err := normalizeRelationData(old.Type, edge.Data)
				if err != nil {
					fail(id, "ambiguous_legacy_relation_metadata")
					continue
				}
				out.Data = normalized
				out.ID = identity("relation", targetID, edge.ID)
				out.SourcePluginID = targetID
				out.SourcePluginType = cp.Type
				out.TargetPluginType = expected
				if child.Plugin.IsEmbedded {
					mapped, good := convert(child.Plugin.ID, false)
					if !good {
						continue
					}
					out.TargetPluginID = mapped.TargetID
					pin := mapped.VersionID
					out.TargetVersionID = &pin
				} else {
					if !audience(cp, child.Plugin) {
						fail(id, "incompatible_existing_audience")
						continue
					}
					if edge.TargetVersionID == nil {
						mapped, ok := knownMappings[child.Plugin.ID]
						if !ok || mapped.VersionID == "" {
							fail(id, "unproven_existing_pin")
							continue
						}
						out.TargetPluginID = mapped.TargetID
						pin := mapped.VersionID
						out.TargetVersionID = &pin
						if dependency := plannedByRoot[child.Plugin.ID]; dependency != "" {
							unit.Dependencies = append(unit.Dependencies, dependency)
						}
					} else if !released(d, child.Plugin.ID, *edge.TargetVersionID) {
						fail(id, "unproven_existing_pin")
						continue
					}
				}
				relations = append(relations, out)
			}
			if old.Type == model.PluginTypeSkill && len(r.Relations) > 0 {
				fail(id, "skill_has_relations")
			}
			if old.Type == model.PluginTypeExpertTeam && !validTeam(relations) {
				fail(id, "invalid_team_wiring")
			}
			if reason := verifySource(old); reason != "" {
				fail(id, reason)
				return Mapping{}, false
			}
			packageInput := old.Package
			if reason := d.FileExceptions[id]; reason != "" {
				fail(id, "artifact_verification_failed")
				return Mapping{}, false
			}
			if old.Type == model.PluginTypeSkill {
				if files, ok := d.ResolvedSkillFiles[id]; ok {
					packageInput = resolvedPackage(files)
				}
			}
			if len(packageInput) > 1<<20 {
				fail(id, "materialized_package_too_large")
				return Mapping{}, false
			}
			pkg, mcp, arts, err := preparePackage(old.Type, packageInput)
			if err != nil {
				fail(id, err.Error())
				return Mapping{}, false
			}
			cp.Package = pkg
			// Group-specific role and leadership live only in the team edge after extraction.
			if old.Type == model.PluginTypeExpert && len(mcp) > 0 {
				connectorID := identity("connector", root.Plugin.ID, id)
				if _, exists := all[connectorID]; exists {
					fail(id, "target_identity_collision")
					return Mapping{}, false
				}
				connector := cp
				connector.ID = connectorID
				connector.Type = model.PluginTypeConnector
				connector.Name = connectorName(old.Name)
				connector.Package = connectorPackage(mcp)
				connector.Tags = json.RawMessage(`[]`)
				connector.Manifest = connectorManifest(connector.Name)
				if err = canonical(&connector); err != nil {
					fail(id, "invalid_connector_document")
				} else {
					cm := Mapping{SourceID: id + ":mcp", TargetID: connectorID, Type: model.PluginTypeConnector}
					nodes[id+":mcp"] = Node{Mapping: cm, Plugin: connector, Clone: true}
					relations = append(relations, model.PluginRelation{ID: identity("mcp-relation", targetID, id), SourcePluginID: targetID, SourcePluginType: cp.Type, TargetPluginID: connectorID, TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1})
				}
			}
			if err = canonical(&cp); err != nil {
				fail(id, "invalid_resource_document")
				return Mapping{}, false
			}
			label := "1.0.0"
			if isRoot {
				label = forwardLabel(old, d)
				if label == "" {
					fail(id, "unresolved_version_label")
					return Mapping{}, false
				}
			}
			version := identity("version", targetID, hash([]any{cp.Manifest, cp.Package, relations, label}))
			if old.Type == model.PluginTypeSkill {
				files := rawFiles(cp.Package)
				description := manifestDescription(cp.Manifest)
				if _, err := pluginsvc.PreparePinnedSkills([]pluginsvc.PinnedSkillFiles{{PluginID: targetID, VersionID: version, Name: cp.Name, Description: description, Files: files}}); err != nil {
					fail(id, "invalid_install_skill")
					return Mapping{}, false
				}
			}
			mapping := Mapping{SourceID: id, TargetID: targetID, VersionID: version, Type: old.Type, Label: label}
			node := Node{Mapping: mapping, Plugin: cp, Relations: relations, Artifacts: arts, Clone: !isRoot, PreviousPublished: r.PublishedVersionID, Release: !isRoot || old.ListingState != "draft" || forceTarget[id]}
			nodes[id] = node
			return mapping, true
		}
		convert(root.Plugin.ID, true)
		// Topological order: children and connector first, member experts, then root.
		for _, node := range nodes {
			unit.Nodes = append(unit.Nodes, node)
		}
		rank := func(n Node) int {
			if n.SourceID == root.Plugin.ID {
				return 4
			}
			switch n.Type {
			case model.PluginTypeSkill:
				return 0
			case model.PluginTypeConnector:
				return 1
			case model.PluginTypeExpert:
				return 2
			}
			return 3
		}
		sort.Slice(unit.Nodes, func(i, j int) bool {
			a, b := rank(unit.Nodes[i]), rank(unit.Nodes[j])
			if a != b {
				return a < b
			}
			return unit.Nodes[i].SourceID < unit.Nodes[j].SourceID
		})
		if len(unit.Nodes) > 100 {
			fail(root.Plugin.ID, "graph_too_large")
		}
		sort.Slice(unit.Exceptions, func(i, j int) bool {
			a, b := unit.Exceptions[i], unit.Exceptions[j]
			return a.ResourceID+"/"+a.Code < b.ResourceID+"/"+b.Code
		})
		if len(unit.Exceptions) == 0 {
			for _, n := range unit.Nodes {
				if n.VersionID != "" && n.Release {
					knownMappings[n.SourceID] = n.Mapping
					plannedByRoot[n.SourceID] = root.Plugin.ID
				}
			}
		}
		expertNames := map[string]bool{}
		for _, edge := range root.Relations {
			if root.Plugin.Type == model.PluginTypeExpertTeam && edge.Type == "expert_team_expert" {
				if member, ok := all[edge.TargetPluginID]; ok {
					if expertNames[member.Plugin.Name] {
						fail(root.Plugin.ID, "duplicate_expert_install_name")
					}
					expertNames[member.Plugin.Name] = true
				}
			}
		}
		allSkills := []pluginsvc.PinnedSkillFiles{}
		totalBytes := 0
		for _, n := range unit.Nodes {
			if (n.Type == model.PluginTypeExpert || n.Type == model.PluginTypeExpertTeam) && utf8.RuneCountInString(manifestDescription(n.Plugin.Manifest)) > 255 {
				fail(n.SourceID, "invalid_install_description")
			}
			files := rawFiles(n.Plugin.Package)
			for _, content := range files {
				escaped, _ := json.Marshal(content)
				totalBytes += len(escaped) + len(n.TargetID) + len(n.VersionID) + 256
			}
			if n.Type == model.PluginTypeSkill {
				allSkills = append(allSkills, pluginsvc.PinnedSkillFiles{PluginID: n.TargetID, VersionID: n.VersionID, Name: n.Plugin.Name, Description: manifestDescription(n.Plugin.Manifest), Files: files})
			}
		}
		if _, err := pluginsvc.PreparePinnedSkills(allSkills); err != nil {
			fail(root.Plugin.ID, "invalid_install_skill_graph")
		}
		if totalBytes > (16<<20)-(128<<10) {
			fail(root.Plugin.ID, "install_graph_too_large")
		}
		sort.Strings(unit.Dependencies)
		p.Exceptions = append(p.Exceptions, unit.Exceptions...)
		p.Units = append(p.Units, unit)
	}
	// A shared embedded target must invalidate both roots, including the earlier one.
	for i := range p.Units {
		for _, n := range p.Units[i].Nodes {
			if n.Clone && n.Type != model.PluginTypeConnector && owned[n.SourceID] != p.Units[i].RootID {
				e := Exception{n.SourceID, "embedded_shared_across_roots"}
				p.Units[i].Exceptions = append(p.Units[i].Exceptions, e)
				p.Exceptions = append(p.Exceptions, e)
			}
		}
	}
	retired := map[string]bool{}
	for _, id := range d.MigratedSourceIDs {
		retired[id] = true
	}
	for _, r := range d.Resources {
		if r.Plugin.IsEmbedded && r.Plugin.Status == 1 && r.Plugin.DeletedAt == nil && owned[r.Plugin.ID] == "" && !retired[r.Plugin.ID] {
			p.Exceptions = append(p.Exceptions, Exception{r.Plugin.ID, "orphan_embedded_resource"})
		}
	}
	return p
}
func sameSpace(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func released(d Dataset, id, version string) bool {
	snapshot := false
	for _, v := range d.Versions {
		if v.ID == version && v.PluginID == id {
			snapshot = true
		}
	}
	for _, r := range d.Releases {
		if r.PluginID == id && r.VersionID == version {
			return snapshot
		}
	}
	return false
}
func audience(source, target model.Plugin) bool {
	a, b := model.NormalizeLegacyVisibility(source.Visibility), model.NormalizeLegacyVisibility(target.Visibility)
	return b == "system" || (a != "system" && sameSpace(source.SpaceID, target.SpaceID) && (b == "space" || (a == "private" && b == "private" && source.OwnerUID == target.OwnerUID)))
}
func forwardLabel(p model.Plugin, d Dataset) string {
	// Stay inside the ordinary write contract and above every historical label.
	// A numeric legacy label outside those bounds remains orderable by the model;
	// quarantine it rather than producing a label that later writes cannot use.
	const maxPart = 999999999
	var highest [3]uint64
	found := false
	include := func(label string) bool {
		parts, ok := model.ParseVersion(label)
		if !ok {
			return true // Unorderable legacy labels impose no numeric lower bound.
		}
		for _, part := range strings.Split(strings.TrimSpace(label), ".") {
			if len(part) > 9 || strings.Trim(part, "0123456789") != "" {
				return false
			}
		}
		greater := !found
		for i := range parts {
			if parts[i] != highest[i] {
				greater = parts[i] > highest[i]
				break
			}
		}
		if greater {
			highest = parts
		}
		found = true
		return true
	}
	if p.CurrentVersion != nil && !include(*p.CurrentVersion) {
		return ""
	}
	for _, r := range d.Releases {
		if r.PluginID == p.ID && !include(r.VersionLabel) {
			return ""
		}
	}
	for _, v := range d.Versions {
		if v.PluginID == p.ID && v.DeclaredVersion != nil && !include(*v.DeclaredVersion) {
			return ""
		}
	}
	if !found {
		return "1.0.0"
	}
	for i := len(highest) - 1; i >= 0; i-- {
		if highest[i] == maxPart {
			highest[i] = 0
			continue
		}
		highest[i]++
		// Leave at least one valid successor for the next content publication.
		if highest == [3]uint64{maxPart, maxPart, maxPart} {
			return ""
		}
		return fmt.Sprintf("%d.%d.%d", highest[0], highest[1], highest[2])
	}
	return ""
}
func canonical(p *model.Plugin) error {
	space := ""
	if p.SpaceID != nil {
		space = *p.SpaceID
	}
	docs, err := pluginsvc.CanonicalizeMigratedDocuments(p.Name, p.Type, p.Tags, p.Manifest, p.Package, space)
	if err != nil {
		return err
	}
	p.Manifest = docs.Manifest
	p.Package = docs.Package
	p.Tags = docs.Tags
	p.ManifestHash = docs.ManifestHash
	p.PluginHash = docs.PluginHash
	p.AttachmentKeys = nil
	return nil
}
func hasAttachment(raw json.RawMessage, wanted string) bool {
	var doc struct {
		Attachments []struct {
			Path string `json:"path"`
		} `json:"attachments"`
	}
	_ = json.Unmarshal(raw, &doc)
	for _, a := range doc.Attachments {
		if a.Path == wanted {
			return true
		}
	}
	return false
}
func connectorManifest(name string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-manifest-2.0.json", "plugin_name": name, "plugin_type": "connector", "name": name, "description": "Extracted MCP configuration", "labels": []string{}, "examples": []any{}})
	return raw
}
func connectorPackage(mcp json.RawMessage) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-package-2.0.json", "connector": map[string]any{"type": "mcp", "source": "stock-migration"}, "attachments": []any{map[string]any{"path": "mcp.json", "content_type": "raw", "mime_type": "application/json", "raw_content": string(mcp)}}})
	return raw
}
func preparePackage(kind model.PluginType, raw json.RawMessage) (json.RawMessage, json.RawMessage, json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil, nil, nil, fmt.Errorf("invalid_package")
	}
	var attachments []map[string]json.RawMessage
	if json.Unmarshal(doc["attachments"], &attachments) != nil {
		return nil, nil, nil, fmt.Errorf("missing_attachments")
	}
	kept := []map[string]json.RawMessage{}
	artifacts := []model.PluginVersionArtifact{}
	mcp := json.RawMessage(nil)
	seen := map[string]bool{}
	required := false
	total := 0
	for _, a := range attachments {
		var path, typ, content string
		_ = json.Unmarshal(a["path"], &path)
		_ = json.Unmarshal(a["content_type"], &typ)
		_ = json.Unmarshal(a["raw_content"], &content)
		if path == "" || seen[path] {
			return nil, nil, nil, fmt.Errorf("invalid_attachment")
		}
		seen[path] = true
		if typ != "raw" || path == "skill/ref.json" {
			return nil, nil, nil, fmt.Errorf("external_file_requires_verification")
		}
		total += len(content)
		if !utf8.ValidString(content) || strings.ContainsRune(content, 0) || len(content) > 1<<20 || total > 16<<20 {
			return nil, nil, nil, fmt.Errorf("oversized_or_binary_content")
		}
		if kind == model.PluginTypeExpert && path == "mcp.json" {
			if pluginsvc.ValidateMigrationMCP(content) != nil {
				return nil, nil, nil, fmt.Errorf("invalid_mcp_config")
			}
			var config map[string]json.RawMessage
			if json.Unmarshal([]byte(content), &config) != nil || config == nil {
				return nil, nil, nil, fmt.Errorf("invalid_mcp_config")
			}
			if len(config) != 0 {
				if len(config) != 1 || config["mcpServers"] == nil {
					return nil, nil, nil, fmt.Errorf("invalid_mcp_config")
				}
				var servers map[string]json.RawMessage
				if json.Unmarshal(config["mcpServers"], &servers) != nil || servers == nil {
					return nil, nil, nil, fmt.Errorf("invalid_mcp_config")
				}
				if len(servers) > 0 {
					if reason := checkMCP(servers); reason != "" {
						return nil, nil, nil, fmt.Errorf("%s", reason)
					}
					mcp = []byte(content)
				}
			}
			continue
		}
		if kind == model.PluginTypeExpert && path == "expert/context.json" {
			continue
		}
		if (kind == model.PluginTypeSkill && path == "SKILL.md") || ((kind == model.PluginTypeExpert || kind == model.PluginTypeExpertTeam) && path == "AGENTS.md") {
			required = strings.TrimSpace(content) != ""
		}
		kept = append(kept, a)
		h := sha256.Sum256([]byte(content))
		artifacts = append(artifacts, model.PluginVersionArtifact{Path: path, ContentType: "raw", Size: int64(len(content)), Digest: "sha256:" + hex.EncodeToString(h[:])})
	}
	if !required {
		return nil, nil, nil, fmt.Errorf("missing_required_file")
	}
	doc["attachments"], _ = json.Marshal(kept)
	pkg, _ := json.Marshal(doc)
	arts, _ := json.Marshal(artifacts)
	return pkg, mcp, arts, nil
}

var placeholder = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

func checkMCP(servers map[string]json.RawMessage) string {
	if len(servers) > 100 {
		return "oversized_mcp_config"
	}
	for name, raw := range servers {
		if name == "" || len(name) > 128 {
			return "invalid_mcp_config"
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return "invalid_mcp_config"
		}
		allowed := map[string]bool{"command": true, "args": true, "env": true, "url": true, "type": true, "headers": true}
		for field := range fields {
			if !allowed[field] {
				return "unsupported_mcp_field"
			}
		}
		var remote string
		if raw, ok := fields["url"]; ok && json.Unmarshal(raw, &remote) == nil && remote != "" {
			u, err := url.Parse(remote)
			if err != nil {
				return "invalid_mcp_config"
			}
			if u.User != nil {
				return "secret_value_requires_policy"
			}
			for key, values := range u.Query() {
				if secretCarrier(key) {
					for _, value := range values {
						if value != "" && !placeholder.MatchString(value) {
							return "secret_value_requires_policy"
						}
					}
				}
			}
		}
		var args []string
		if raw, ok := fields["args"]; ok && json.Unmarshal(raw, &args) == nil {
			for i, arg := range args {
				flag, value, hasValue := strings.Cut(arg, "=")
				if strings.HasPrefix(flag, "--") && secretCarrier(strings.TrimPrefix(flag, "--")) {
					if !hasValue && i+1 < len(args) {
						value = args[i+1]
					}
					if value != "" && !placeholder.MatchString(value) {
						return "secret_value_requires_policy"
					}
				}
			}
		}
		for _, key := range []string{"env", "headers"} {
			if value, ok := fields[key]; ok {
				var entries map[string]string
				if json.Unmarshal(value, &entries) != nil || entries == nil {
					return "invalid_mcp_config"
				}
				for _, v := range entries {
					if v != "" && !placeholder.MatchString(v) {
						return "secret_value_requires_policy"
					}
				}
			}
		}
	}
	return ""
}

func normalizeRelationData(kind model.PluginType, raw json.RawMessage) (json.RawMessage, error) {
	data := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &data) != nil || data == nil {
			return nil, ErrExceptions
		}
	}
	for key := range data {
		if key != "source_index" && (kind != model.PluginTypeExpertTeam || (key != "member_key" && key != "role" && key != "is_leader")) {
			return nil, ErrExceptions
		}
	}
	delete(data, "source_index")
	if kind != model.PluginTypeExpertTeam {
		return nil, nil
	}
	b, _ := json.Marshal(data)
	return b, nil
}
func validTeam(rels []model.PluginRelation) bool {
	if len(rels) == 0 || len(rels) > 30 {
		return false
	}
	leaders := 0
	keys := map[string]bool{}
	for _, r := range rels {
		var v struct {
			Key    string `json:"member_key"`
			Role   string `json:"role"`
			Leader bool   `json:"is_leader"`
		}
		if json.Unmarshal(r.Data, &v) != nil || strings.TrimSpace(v.Key) == "" || keys[v.Key] || utf8.RuneCountInString(v.Role) > 500 || len(v.Key) > 128 || (!v.Leader && strings.TrimSpace(v.Role) == "leader") {
			return false
		}
		keys[v.Key] = true
		if v.Leader {
			leaders++
		}
	}
	return leaders == 1
}

func connectorName(name string) string {
	runes := []rune(name)
	if len(runes) > 124 {
		runes = runes[:124]
	}
	return string(runes) + " MCP"
}

func rawFiles(raw json.RawMessage) map[string]string {
	var doc struct {
		Attachments []struct {
			Path    string `json:"path"`
			Content string `json:"raw_content"`
		} `json:"attachments"`
	}
	_ = json.Unmarshal(raw, &doc)
	result := map[string]string{}
	for _, a := range doc.Attachments {
		result[a.Path] = a.Content
	}
	return result
}
func manifestDescription(raw json.RawMessage) string {
	var m struct {
		Description string `json:"description"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Description
}

func resolvedPackage(files map[string]string) json.RawMessage {
	paths := []string{}
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	atts := []any{}
	for _, path := range paths {
		atts = append(atts, map[string]any{"path": path, "content_type": "raw", "mime_type": "text/plain", "raw_content": files[path]})
	}
	raw, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-package-2.0.json", "attachments": atts})
	return raw
}

func secretCarrier(key string) bool {
	key = strings.ReplaceAll(strings.ToLower(key), "-", "_")
	switch key {
	case "token", "access_token", "api_key", "apikey", "authorization", "auth", "password", "secret", "client_secret":
		return true
	}
	return false
}

// Persisted packages already split storage_uri into a host-private sidecar;
// use the shared read-contract validator rather than the incoming-write adapter.
// Hydration verifies the actual files and original sidecar independently.
func verifySource(p model.Plugin) string {
	m, _, err := pluginsvc.CanonicalizeManifest(p.Name, p.Type, p.Tags, p.Manifest)
	if err != nil || model.ValidateUniqueJSONFields(p.Package) != nil {
		return "invalid_source_document"
	}
	pkg, err := libplugin.CanonicalJSON(p.Package)
	if err != nil {
		return "invalid_source_document"
	}
	if _, err = libplugin.DecodePackage(libplugin.Type(p.Type), pkg); err != nil {
		return "invalid_source_document"
	}
	ph, err := libplugin.ComputePluginHash(m, pkg)
	if err != nil {
		return "invalid_source_document"
	}
	sum := sha256.Sum256(m)
	mh := "sha256:" + hex.EncodeToString(sum[:])
	if (p.PluginHash != "" && p.PluginHash != ph) || (p.ManifestHash != "" && p.ManifestHash != mh) {
		return "source_integrity_mismatch"
	}
	return ""
}
