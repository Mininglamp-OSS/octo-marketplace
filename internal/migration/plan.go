package migration

import (
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/artifactscope"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/artifacts"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

func Build(in Input, policy Policy) (Plan, error) {
	// JSON cloning deliberately includes private file bytes separately: File.Data is report-private.
	rs := make([]Resource, len(in.Resources))
	for i, r := range in.Resources {
		b, _ := json.Marshal(r)
		_ = json.Unmarshal(b, &rs[i])
		if r.IconFile != nil {
			icon := *r.IconFile
			icon.Data = append([]byte(nil), icon.Data...)
			rs[i].IconFile = &icon
		}
		rs[i].Files = make([]File, len(r.Files))
		for j, f := range r.Files {
			rs[i].Files[j] = f
			rs[i].Files[j].Data = append([]byte(nil), f.Data...)
		}
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Plugin.UpdatedAt.Equal(rs[j].Plugin.UpdatedAt) {
			return rs[i].Plugin.ID < rs[j].Plugin.ID
		}
		return rs[i].Plugin.UpdatedAt.Before(rs[j].Plugin.UpdatedAt)
	})
	p := Plan{SnapshotID: in.SnapshotID, CategorySnapshotCaptured: in.CategorySnapshotCaptured}
	if in.CategorySnapshotCaptured {
		p.CategorySnapshot = map[string]string{}
		for id, h := range in.CategorySnapshot {
			p.CategorySnapshot[id] = h
		}
	}
	issue := func(id, code string) { p.Issues = append(p.Issues, Issue{id, code}) }
	if in.SnapshotID == "" {
		issue("", "snapshot_required")
	}
	byID := map[string]Resource{}
	active := map[string]bool{}
	for _, r := range rs {
		if _, ok := byID[r.Plugin.ID]; ok {
			issue(r.Plugin.ID, "duplicate_source")
		}
		byID[r.Plugin.ID] = r
		if r.Plugin.DeletedAt == nil && !r.PendingReview {
			active[r.Plugin.ID] = true
		}
	}
	versions := map[string]model.PluginVersion{}
	for _, v := range in.Versions {
		versions[v.ID] = v
	}
	for _, r := range rs {
		if !active[r.Plugin.ID] {
			continue
		}
		v := model.NormalizeLegacyVisibility(r.Plugin.Visibility)
		if v != model.PluginVisibilitySystem && v != model.PluginVisibilitySpace && v != model.PluginVisibilityPrivate {
			issue(r.Plugin.ID, "visibility_invalid")
		}
		if v != model.PluginVisibilitySystem && (r.Plugin.SpaceID == nil || *r.Plugin.SpaceID == "") {
			issue(r.Plugin.ID, "space_required")
		}
		if v == model.PluginVisibilityPrivate && r.Plugin.OwnerUID == "" {
			issue(r.Plugin.ID, "owner_required")
		}
		switch r.Plugin.Type {
		case model.PluginTypeSkill, model.PluginTypeConnector, model.PluginTypeExpert, model.PluginTypeExpertTeam:
		default:
			issue(r.Plugin.ID, "plugin_type_invalid")
		}
		if r.Plugin.PluginHash != "" {
			h, err := libplugin.ComputePluginHash(r.Plugin.Manifest, r.Plugin.Package)
			if err != nil || h != r.Plugin.PluginHash {
				issue(r.Plugin.ID, "source_integrity_mismatch")
			}
		}
		if r.Plugin.ManifestHash != "" {
			m, err := libplugin.CanonicalJSON(r.Plugin.Manifest)
			if err != nil || "sha256:"+digest(m) != r.Plugin.ManifestHash {
				issue(r.Plugin.ID, "source_integrity_mismatch")
			}
		}
		if _, err := libplugin.DecodePackage(libplugin.Type(r.Plugin.Type), r.Plugin.Package); err != nil {
			issue(r.Plugin.ID, "source_package_contract_invalid")
		}
		if r.Plugin.UpdatedAt.IsZero() {
			issue(r.Plugin.ID, "updated_at_required")
		}
		if r.PublishedVersionID != nil {
			released := false
			for _, release := range in.Releases {
				if release.PluginID == r.Plugin.ID && release.VersionID == *r.PublishedVersionID {
					released = true
				}
			}
			if !released {
				issue(r.Plugin.ID, "published_release_missing")
			}
			v, ok := versions[*r.PublishedVersionID]
			if !ok || v.PluginID != r.Plugin.ID {
				issue(r.Plugin.ID, "published_version_missing")
			} else if !snapshotMatches(r, v) {
				issue(r.Plugin.ID, "draft_published_divergence")
			}
		}
		if r.Plugin.CurrentVersionID != nil {
			v, ok := versions[*r.Plugin.CurrentVersionID]
			if !ok || v.PluginID != r.Plugin.ID {
				issue(r.Plugin.ID, "current_version_missing")
			}
		}
	}
	// Resolve frontend only from authorized IDs, or all unambiguous Chinese children of the named expert.
	chosen := map[string]bool{}
	for _, id := range policy.FrontendChineseSourceIDs {
		r, ok := byID[id]
		if !ok || !active[id] || normalized(r.Plugin.Name) != "frontend-dev" || !chineseSkill(r) {
			issue(id, "frontend_source_invalid")
		} else {
			chosen[id] = true
		}
	}
	if len(policy.FrontendChineseSourceIDs) == 0 {
		for _, r := range rs {
			if normalized(r.Plugin.Name) != "高级开发专家" || r.Plugin.Type != model.PluginTypeExpert {
				continue
			}
			for _, e := range r.Relations {
				c, ok := byID[e.TargetPluginID]
				if ok && active[c.Plugin.ID] && normalized(c.Plugin.Name) == "frontend-dev" && chineseSkill(c) {
					chosen[c.Plugin.ID] = true
				}
			}
		}
	}
	groups := map[string][]Resource{}
	for _, r := range rs {
		if !active[r.Plugin.ID] {
			continue
		}
		if r.Plugin.Type != model.PluginTypeSkill && r.Plugin.Type != model.PluginTypeConnector {
			continue
		}
		key := scope(r.Plugin) + ":" + string(r.Plugin.Type) + ":" + normalized(r.Plugin.Name)
		if variant(normalized(r.Plugin.Name)) || r.Plugin.Type == model.PluginTypeConnector {
			key += ":" + fingerprint(r)
		}
		groups[key] = append(groups[key], r)
	}
	canonical := map[string]string{}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := groups[k]
		c := g[0]
		if normalized(c.Plugin.Name) == "frontend-dev" {
			candidates := []Resource{}
			for _, r := range g {
				if chosen[r.Plugin.ID] {
					candidates = append(candidates, r)
				}
			}
			if len(candidates) == 0 {
				issue(c.Plugin.ID, "frontend_chinese_source_missing")
			} else {
				c = candidates[0]
				for _, r := range candidates[1:] {
					if fingerprint(r) != fingerprint(c) {
						issue(r.Plugin.ID, "frontend_chinese_ambiguous")
					}
				}
			}
		}
		for _, r := range g {
			canonical[r.Plugin.ID] = c.Plugin.ID
		}
	}
	// Reuse system content only when full trees and manifest semantics are identical.
	for _, r := range rs {
		c, ok := canonical[r.Plugin.ID]
		if !ok || scope(r.Plugin) == "system" {
			continue
		}
		for _, s := range rs {
			if scope(s.Plugin) == "system" && sharedActive(r.Plugin) && lifecycle(r.Plugin) == lifecycle(s.Plugin) && active[s.Plugin.ID] && canonical[s.Plugin.ID] == s.Plugin.ID && normalized(s.Plugin.Name) == normalized(r.Plugin.Name) && fingerprint(s) == fingerprint(byID[c]) {
				canonical[r.Plugin.ID] = s.Plugin.ID
				break
			}
		}
	}
	target := map[string]string{}
	vid := map[string]string{}
	for _, r := range rs {
		if !active[r.Plugin.ID] {
			continue
		}
		c := r.Plugin.ID
		if x, ok := canonical[c]; ok {
			c = x
		}
		cp := byID[c].Plugin
		id := c
		if cp.IsEmbedded || cp.Type == model.PluginTypeSkill || cp.Type == model.PluginTypeConnector {
			id = stable(in.SnapshotID, "resource", c, scope(cp))
		}
		target[r.Plugin.ID] = id
		if cp.Type != model.PluginTypeConnector {
			vid[r.Plugin.ID] = stable(in.SnapshotID, "version", id)
		}
	}
	nodes := map[string]Node{}
	for _, r := range rs {
		id := r.Plugin.ID
		if !active[id] {
			d := "deleted_history"
			if r.PendingReview && r.Plugin.DeletedAt == nil {
				d = "pending_review"
				issue(id, "pending_review")
			}
			p.Mappings = append(p.Mappings, Mapping{SourceID: id, Disposition: d})
			continue
		}
		d := "converted"
		if canonical[id] != "" && canonical[id] != id {
			d = "canonical_reuse"
		}
		p.Mappings = append(p.Mappings, Mapping{id, target[id], vid[id], d})
		if _, ok := nodes[target[id]]; ok {
			continue
		}
		if canonical[id] != "" && canonical[id] != id {
			continue
		}
		cp := r.Plugin
		cp.Visibility = model.NormalizeLegacyVisibility(cp.Visibility)
		cp.ID = target[id]
		cp.IsEmbedded = false
		cp.CurrentVersionID = nil
		cp.CurrentVersion = nil
		// Delisting removes discoverability, not publication. Convert the validated
		// released content into a target-owned release while keeping listing unchanged.
		n := Node{IconFile: r.IconFile, SourceRowHash: r.SourceRowHash, SourceID: id, Source: r.Plugin, Plugin: cp, VersionID: vid[id], VersionLabel: nextLabel(r.Plugin, in), Clone: cp.ID != id, Release: cp.ListingState == model.PluginListingStatePublished || r.PublishedVersionID != nil, Files: r.Files}
		n.SourcePublishedVersionID = r.PublishedVersionID
		if r.PublishedVersionID != nil {
			for _, release := range in.Releases {
				if release.PluginID == id && release.VersionID == *r.PublishedVersionID {
					captured := release
					n.SourceRelease = &captured
					break
				}
			}
		}

		if n.VersionID != "" {
			if n.VersionLabel == "" {
				issue(id, "version_label_exhausted")
			}
			n.Plugin.CurrentVersionID = &n.VersionID
			n.Plugin.CurrentVersion = &n.VersionLabel
		}
		if n.Plugin.Type == model.PluginTypeConnector {
			for _, f := range n.Files {
				if f.Path == "mcp.json" {
					if pluginsvc.ValidateMigrationMCP(string(f.Data)) != nil {
						issue(id, "mcp_invalid")
					}
				}
			}
		}
		if code := completeSkillDescription(&n); code != "" {
			issue(id, code)
		}
		// Keep the old member context bytes as an ordinary supporting file.
		// Its reserved path means an inline capability to the reference reader.
		if n.Plugin.Type == model.PluginTypeExpert {
			for i := range n.Files {
				if strings.EqualFold(strings.TrimSpace(n.Files[i].Path), "expert/context.json") {
					n.Files[i].Path = "migration/legacy/expert/context.json"
				}
			}
		}
		if err := prepareFiles(&n, in.SnapshotID); err != "" {
			issue(id, err)
		}
		nodes[cp.ID] = n
	}
	// Validate and rewrite every edge, keeping all member metadata and exact pins.
	for key, n := range nodes {
		r := byID[n.SourceID]
		memberKeys := map[string]bool{}
		relationIDs := map[string]bool{}
		leaders := 0
		for _, e := range r.Relations {
			if e.DeletedAt != nil || e.Status != 1 {
				continue
			}
			tr, ok := byID[e.TargetPluginID]
			if !ok || !active[e.TargetPluginID] {
				issue(n.SourceID, "missing_target")
				continue
			}
			valid := n.Plugin.Type == model.PluginTypeExpert && ((e.Type == "expert_skill" && tr.Plugin.Type == model.PluginTypeSkill) || (e.Type == "expert_connector" && tr.Plugin.Type == model.PluginTypeConnector)) || n.Plugin.Type == model.PluginTypeExpertTeam && e.Type == "expert_team_expert" && tr.Plugin.Type == model.PluginTypeExpert
			if !valid || e.TargetPluginType != tr.Plugin.Type {
				issue(n.SourceID, "relation_type_invalid")
			}
			if !accessible(n.Plugin, tr.Plugin) {
				issue(n.SourceID, "scope_conflict")
			}
			if e.TargetVersionID != nil {
				v, ok := versions[*e.TargetVersionID]
				if !ok || v.PluginID != tr.Plugin.ID {
					issue(n.SourceID, "version_pin_invalid")
				} else if !snapshotMatches(tr, v) {
					issue(n.SourceID, "historical_pin_divergence")
				}
			}
			if e.Type == "expert_team_expert" {
				var data struct {
					Key    string `json:"member_key"`
					Role   string `json:"role"`
					Leader bool   `json:"is_leader"`
				}
				if json.Unmarshal(e.Data, &data) != nil || strings.TrimSpace(data.Key) == "" || memberKeys[data.Key] || utf8.RuneCountInString(data.Role) > 500 || (!data.Leader && data.Role == "leader") {
					issue(n.SourceID, "member_role_invalid")
				}
				memberKeys[data.Key] = true
				if data.Leader {
					leaders++
				}
			}
			// Legacy container imports duplicated ordering in source_index. The
			// new reference contract uses SortOrder and rejects this old field.
			var metadata map[string]json.RawMessage
			if len(e.Data) > 0 && string(e.Data) != "null" {
				if model.ValidateUniqueJSONFields(e.Data) != nil || json.Unmarshal(e.Data, &metadata) != nil || metadata == nil {
					issue(n.SourceID, "relation_metadata_invalid")
				} else {
					delete(metadata, "source_index")
					e.Data, _ = json.Marshal(metadata)
				}
			}
			old := e.TargetPluginID
			e.SourcePluginID = n.Plugin.ID
			e.SourcePluginType = n.Plugin.Type
			e.TargetPluginID = target[old]
			e.TargetVersionID = nil
			if v := vid[old]; v != "" {
				vv := v
				e.TargetVersionID = &vv
			}
			e.ID = stable(in.SnapshotID, "relation", n.Plugin.ID, e.ID, e.Type, e.TargetPluginID)
			if relationIDs[e.ID] {
				issue(n.SourceID, "duplicate_relation_identity")
			}
			relationIDs[e.ID] = true
			n.Relations = append(n.Relations, e)
		}
		if n.Plugin.Type == model.PluginTypeExpertTeam && (leaders != 1 || len(n.Relations) == 0) {
			issue(n.SourceID, "team_leader_invalid")
		}
		if pluginrepo.ValidateReferenceMetadata(n.Plugin.Type, n.Relations) != nil {
			issue(n.SourceID, "relation_metadata_invalid")
		}
		sort.Slice(n.Relations, func(i, j int) bool {
			if n.Relations[i].SortOrder == n.Relations[j].SortOrder {
				return n.Relations[i].ID < n.Relations[j].ID
			}
			return n.Relations[i].SortOrder < n.Relations[j].SortOrder
		})
		nodes[key] = n
	}
	for id, n := range nodes {
		if n.Plugin.Type != model.PluginTypeExpert {
			continue
		}
		previousFileCount := len(n.Files)
		extracted, code := extractMCP(&n, in.SnapshotID)
		if code != "" {
			issue(n.SourceID, code)
		}
		// Even an empty MCP document is a reserved inline capability. Persist
		// its removal instead of conditioning cleanup on generated children.
		if len(extracted) > 0 || len(n.Files) != previousFileCount {
			if code := prepareFiles(&n, in.SnapshotID); code != "" {
				issue(n.SourceID, code)
			}
			nodes[id] = n
			for _, cn := range extracted {
				nodes[cn.Plugin.ID] = cn
			}
		}
	}
	state := map[string]int{}
	var visit func(string)
	visit = func(id string) {
		if state[id] == 1 {
			issue(nodes[id].SourceID, "graph_cycle")
			return
		}
		if state[id] == 2 {
			return
		}
		state[id] = 1
		for _, e := range nodes[id].Relations {
			visit(e.TargetPluginID)
		}
		state[id] = 2
		p.Nodes = append(p.Nodes, nodes[id])
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		visit(id)
	}
	// Each node also consumes mapped duplicate sources when merging placements.
	// Carry their original evidence without exposing inventories in public reports.
	for i := range p.Nodes {
		n := &p.Nodes[i]
		n.Evidence = map[string]SourceEvidence{n.SourceID: byID[n.SourceID].Evidence}
		for _, mapping := range p.Mappings {
			if mapping.TargetID == n.Plugin.ID {
				n.Evidence[mapping.SourceID] = byID[mapping.SourceID].Evidence
			}
		}
	}
	p.Issues = append(p.Issues, validateMigrationReleases(p)...)
	p.Issues = append(p.Issues, connectorNameIssues(p)...)
	p.Issues = append(p.Issues, iconIssues(p)...)
	sort.Slice(p.Mappings, func(i, j int) bool { return p.Mappings[i].SourceID < p.Mappings[j].SourceID })
	sort.Slice(p.Issues, func(i, j int) bool {
		if p.Issues[i].ResourceID == p.Issues[j].ResourceID {
			return p.Issues[i].Code < p.Issues[j].Code
		}
		return p.Issues[i].ResourceID < p.Issues[j].ResourceID
	})
	// Run identity canonicalizes unordered file/object locations; physical source keys
	// are deliberately excluded from content policy.
	for i := range rs {
		rs[i].Files = append([]File(nil), rs[i].Files...)
		sort.Slice(rs[i].Files, func(a, b int) bool { return rs[i].Files[a].Path < rs[i].Files[b].Path })
		for j := range rs[i].Files {
			rs[i].Files[j].ObjectKey = ""
		}
		sort.Slice(rs[i].Relations, func(a, b int) bool { return rs[i].Relations[a].ID < rs[i].Relations[b].ID })
	}
	vv := append([]model.PluginVersion(nil), in.Versions...)
	sort.Slice(vv, func(i, j int) bool { return vv[i].ID < vv[j].ID })
	rr := append([]model.PluginVersionRelease(nil), in.Releases...)
	sort.Slice(rr, func(i, j int) bool {
		if rr[i].PluginID == rr[j].PluginID {
			return rr[i].VersionID < rr[j].VersionID
		}
		return rr[i].PluginID < rr[j].PluginID
	})
	pp := Policy{FrontendChineseSourceIDs: append([]string(nil), policy.FrontendChineseSourceIDs...)}
	sort.Strings(pp.FrontendChineseSourceIDs)
	raw, _ := json.Marshal(struct {
		Input  Input
		Policy Policy
	}{Input{SnapshotID: in.SnapshotID, Resources: rs, Versions: vv, Releases: rr}, pp})
	content := ""
	for _, r := range rs {
		content += r.Plugin.ID + fingerprint(r)
	}
	p.ID = stable(in.SnapshotID, "plan", digest(raw), content)
	if len(p.Issues) > 0 {
		return p, ErrIssues
	}
	return p, nil
}
func chineseSkill(r Resource) bool {
	for _, f := range r.Files {
		if strings.EqualFold(path.Base(f.Path), "SKILL.md") {
			for _, c := range string(f.Data) {
				if unicode.Is(unicode.Han, c) {
					return true
				}
			}
		}
	}
	return false
}
func accessible(parent, child model.Plugin) bool {
	pv, cv := model.NormalizeLegacyVisibility(parent.Visibility), model.NormalizeLegacyVisibility(child.Visibility)
	if !sharedActive(child) {
		if lifecycle(parent) != lifecycle(child) || parent.OwnerUID != child.OwnerUID {
			return false
		}
		if cv == model.PluginVisibilitySystem {
			return pv == model.PluginVisibilitySystem
		}
		return pv != model.PluginVisibilitySystem && parent.SpaceID != nil && child.SpaceID != nil && *parent.SpaceID == *child.SpaceID
	}
	if cv == model.PluginVisibilitySystem {
		return true
	}
	if pv == model.PluginVisibilitySystem {
		return false
	}
	if child.SpaceID == nil || parent.SpaceID == nil || *child.SpaceID != *parent.SpaceID {
		return false
	}
	return cv != model.PluginVisibilityPrivate || pv == model.PluginVisibilityPrivate && child.OwnerUID == parent.OwnerUID
}
func prepareFiles(n *Node, snapshot string) string {
	if len(n.Files) == 0 {
		return "files_missing"
	}
	sort.Slice(n.Files, func(i, j int) bool { return n.Files[i].Path < n.Files[j].Path })
	limits := artifacts.DefaultLimits()
	if len(n.Files) > limits.MaxFiles {
		return "file_count_budget_exceeded"
	}
	totalBytes := int64(0)
	entry := "SKILL.md"
	if n.Plugin.Type == model.PluginTypeExpert || n.Plugin.Type == model.PluginTypeExpertTeam {
		entry = "AGENTS.md"
	}
	if n.Plugin.Type == model.PluginTypeConnector {
		entry = "mcp.json"
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(n.Plugin.Package, &metadata) != nil || metadata == nil {
		return "package_invalid"
	}
	delete(metadata, "attachments")
	metadataJSON, _ := json.Marshal(metadata)
	inlineUsed := len(metadataJSON) + 4096
	for _, f := range n.Files {
		totalBytes += int64(len(f.Data))
		if int64(len(f.Data)) > limits.MaxFileBytes {
			return "file_size_budget_exceeded"
		}
		if f.Path == entry {
			raw, _ := json.Marshal(string(f.Data))
			inlineUsed += len(raw)
			if inlineUsed > 1<<20 {
				return "entry_inline_budget_exceeded"
			}
		}
	}
	if totalBytes > limits.MaxTotalBytes {
		return "expanded_bytes_budget_exceeded"
	}
	seen := map[string]bool{}
	atts := []map[string]any{}
	side := map[string]string{}
	artifacts := []map[string]any{}
	for i, f := range n.Files {
		if f.Path == "" || strings.Contains(f.Path, "\\") || strings.HasPrefix(f.Path, "/") || path.Clean(f.Path) != f.Path || f.Path == ".." || strings.HasPrefix(f.Path, "../") || seen[f.Path] {
			return "file_path_invalid"
		}
		seen[f.Path] = true
		h := digest(f.Data)
		a := map[string]any{"path": f.Path, "mime_type": f.MIME}
		typ := "raw"
		rawSize, _ := json.Marshal(string(f.Data))
		spillAggregate := f.Path != entry && inlineUsed+len(rawSize)+512 > 768<<10
		if spillAggregate || !utf8.Valid(f.Data) || strings.IndexByte(string(f.Data), 0) >= 0 || len(f.Data) > 1<<20 || (!strings.HasPrefix(f.MIME, "text/") && f.MIME != "application/json" && f.MIME != "") {
			typ = "storage"
			if f.Path == entry {
				return "entry_inline_budget_exceeded"
			}
			prefix := artifactscope.Prefix(&n.Plugin)
			if prefix == "" {
				return "storage_scope_invalid"
			}
			key := prefix + "versions/" + h
			n.Files[i].ObjectKey = key
			side[f.Path] = key
			a["content_size"] = len(f.Data)
			a["content_hash"] = "sha256:" + h
		} else {
			a["raw_content"] = string(f.Data)
			if f.Path != entry {
				inlineUsed += len(rawSize) + 512
			}
			n.Files[i].ObjectKey = ""
		}
		a["content_type"] = typ
		atts = append(atts, a)
		artifacts = append(artifacts, map[string]any{"path": f.Path, "content_type": typ, "size": len(f.Data), "digest": "sha256:" + h, "object_key": n.Files[i].ObjectKey})
	}
	var pkg map[string]json.RawMessage
	if json.Unmarshal(n.Plugin.Package, &pkg) != nil || pkg == nil {
		return "package_invalid"
	}
	pkg["attachments"], _ = json.Marshal(atts)
	n.Plugin.Package, _ = json.Marshal(pkg)
	if len(n.Plugin.Package) > 1<<20 {
		return "package_inline_budget_exceeded"
	}
	n.Plugin.AttachmentKeys, _ = json.Marshal(side)
	n.Artifacts, _ = json.Marshal(artifacts)
	if model.ValidateUniqueJSONFields(n.Plugin.Manifest) != nil || model.ValidateUniqueJSONFields(n.Plugin.Package) != nil {
		return "document_invalid"
	}
	normalized, err := libplugin.NormalizeRevisionContent(libplugin.RevisionContent{PluginType: libplugin.Type(n.Plugin.Type), ManifestJSON: n.Plugin.Manifest, PluginJSON: n.Plugin.Package})
	if err != nil {
		return "package_contract_invalid"
	}
	n.Plugin.Manifest = normalized.ManifestJSON
	n.Plugin.Package = normalized.PluginJSON
	n.Plugin.ManifestHash = "sha256:" + digest(n.Plugin.Manifest)
	n.Plugin.PluginHash = normalized.PluginHash
	return ""
}
