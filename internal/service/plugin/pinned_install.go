package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// InstallationReadStore batches caller-scoped rows and released evidence without
// granting installation eligibility. The resolver rejects delisted roots AND
// dependencies; retained pins are history, not a distribution exemption. Private
// reads require owner and Space. No permission result survives a request.
type InstallationReadStore interface {
	GetInstallPlugins(context.Context, pluginrepo.Scope, []string) (map[string]*model.Plugin, error)
	GetInstallVersions(context.Context, pluginrepo.Scope, []pluginrepo.InstallVersionKey) (map[pluginrepo.InstallVersionKey]*model.PluginVersion, error)
}

func (s *Service) WithPinnedInstallation(enabled bool) *Service {
	s.pinnedInstallation = enabled
	return s
}

type pinnedInstallResolver struct {
	s        *Service
	caller   Caller
	store    InstallationReadStore
	plugins  map[string]*model.Plugin
	versions map[pluginrepo.InstallVersionKey]*model.PluginVersion
	loaded   map[pluginrepo.InstallVersionKey]*Detail
	assets   capabilityBuilder
	skills   map[pluginrepo.InstallVersionKey]PinnedSkillFiles
}

type pinnedInstallationPlan struct {
	RootVersionID string
	Definition    fleet.CapabilityDefinition
	NodeIDs       []string
	Edges         []planEligibilityEdge
}

type planEligibilityEdge struct{ Source, Target string }

func (s *Service) resolvePinnedInstallation(ctx context.Context, caller Caller, id, rootVersion string) (*pinnedInstallationPlan, error) {
	if !s.versionManagement {
		return nil, ErrVersionBaselineUnavailable
	}
	store, ok := s.repo.(InstallationReadStore)
	if !ok {
		return nil, ErrInvalidRequest
	}
	r := pinnedInstallResolver{s: s, caller: caller, store: store, plugins: map[string]*model.Plugin{}, versions: map[pluginrepo.InstallVersionKey]*model.PluginVersion{}, loaded: map[pluginrepo.InstallVersionKey]*Detail{}, skills: map[pluginrepo.InstallVersionKey]PinnedSkillFiles{}, assets: capabilityBuilder{service: s, caller: caller, remaining: min(s.maxArchiveBytes, maxCapabilityBytes)}}
	if err := r.load(ctx, []string{id}, nil); err != nil {
		if errors.Is(err, ErrDependencyHidden) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	root := r.plugins[id]
	if root == nil || root.ListingState != model.PluginListingStatePublished {
		return nil, ErrNotFound
	}
	if root.Type != model.PluginTypeExpert && root.Type != model.PluginTypeExpertTeam {
		return nil, ErrInvalidRequest
	}
	if rootVersion == "" {
		vs, err := s.versionStore()
		if err != nil {
			return nil, err
		}
		state, err := vs.GetVersionState(ctx, scope(caller), id)
		if err != nil {
			return nil, mapStoreError(err)
		}
		if state.PublishedVersionID == nil {
			return nil, ErrVersionBaselineUnavailable
		}
		rootVersion = *state.PublishedVersionID
	}
	key := pluginrepo.InstallVersionKey{PluginID: id, VersionID: rootVersion}
	if err := r.load(ctx, nil, []pluginrepo.InstallVersionKey{key}); err != nil {
		return nil, err
	}
	d, err := r.detail(ctx, key)
	if err != nil {
		return nil, err
	}
	roots := []*Detail{d}
	memberRels := []model.PluginRelation{}
	if root.Type == model.PluginTypeExpertTeam {
		memberRels = relationsOfType(d.Relations, "expert_team_expert")
		if len(memberRels) == 0 || len(memberRels) > 30 || len(memberRels) != len(d.Relations) {
			return nil, installationInvalid("definition.expert_team.members", "invalid_count")
		}
		sort.Slice(memberRels, func(i, j int) bool {
			if memberRels[i].SortOrder != memberRels[j].SortOrder {
				return memberRels[i].SortOrder < memberRels[j].SortOrder
			}
			return memberRels[i].ID < memberRels[j].ID
		})
		if err = r.loadRelations(ctx, memberRels); err != nil {
			return nil, err
		}
		roots = nil
		for _, rel := range memberRels {
			member, err := r.referenced(ctx, d.Plugin, rel, model.PluginTypeExpert)
			if err != nil {
				return nil, err
			}
			roots = append(roots, member)
		}
	}
	var dependencies []model.PluginRelation
	for _, expert := range roots {
		dependencies = append(dependencies, expert.Relations...)
	}
	if err = r.loadRelations(ctx, dependencies); err != nil {
		return nil, err
	}
	def := fleet.CapabilityDefinition{SchemaVersion: "1.0", Name: "marketplace:" + id}
	names := map[string]bool{}
	for _, expert := range roots {
		name := expert.Plugin.Name
		if !installationName(name, 128) || names[name] {
			return nil, installationInvalid("definition.experts", "invalid_or_duplicate_name")
		}
		names[name] = true
		instructions, ok := rawAttachmentContent(expert.Plugin.Package, "AGENTS.md")
		if !ok || strings.TrimSpace(instructions) == "" {
			return nil, installationInvalid("definition.experts.instructions", "required")
		}
		if err = r.assets.text(instructions); err != nil {
			return nil, err
		}
		out := fleet.CapabilityExpert{Name: name, Description: manifestDescription(expert.Plugin.Manifest), Instructions: instructions}
		if err = r.assets.boundedText("definition.experts.description", out.Description, 255); err != nil {
			return nil, err
		}
		var connectors []CurrentConnectorMCP
		var skillKeys []pluginrepo.InstallVersionKey
		count := 0
		for _, rel := range expert.Relations {
			switch rel.Type {
			case "expert_skill":
				count++
				if count > 20 {
					return nil, ErrTooLarge
				}
				skill, err := r.referenced(ctx, expert.Plugin, rel, model.PluginTypeSkill)
				if err != nil {
					return nil, err
				}
				key := pluginrepo.InstallVersionKey{PluginID: rel.TargetPluginID, VersionID: *rel.TargetVersionID}
				skillKeys = append(skillKeys, key)
				if _, ok := r.skills[key]; !ok {
					files, err := r.assets.skillFiles(ctx, skill.Plugin)
					if err != nil {
						return nil, err
					}
					r.skills[key] = PinnedSkillFiles{PluginID: key.PluginID, VersionID: key.VersionID, Name: skill.Plugin.Name, Description: manifestDescription(skill.Plugin.Manifest), Files: files}
				}
			case "expert_connector":
				if rel.TargetVersionID != nil {
					return nil, ErrIntegrity
				}
				connector := r.plugins[rel.TargetPluginID]
				if connector == nil || connector.Type != model.PluginTypeConnector || rel.SourcePluginID != expert.Plugin.ID || !installAudience(expert.Plugin, connector) {
					return nil, ErrDependencyHidden
				}
				config, ok := rawAttachmentContent(connector.Package, "mcp.json")
				if !ok {
					return nil, installationInvalid("definition.experts.mcp_config", "required")
				}
				connectors = append(connectors, CurrentConnectorMCP{ConnectorID: connector.ID, Config: config})
			default:
				return nil, ErrIntegrity
			}
		}
		config, err := MergeCurrentConnectorMCP(connectors)
		if err != nil {
			return nil, err
		}
		out.MCPConfig = config
		if err = r.assets.text(string(config)); err != nil {
			return nil, err
		}
		for _, key := range skillKeys {
			name, err := pinnedSkillName(r.skills[key])
			if err != nil {
				return nil, err
			}
			out.SkillNames = append(out.SkillNames, name)
		}
		sort.Strings(out.SkillNames)
		out.SkillNames = uniqueStrings(out.SkillNames)
		def.Experts = append(def.Experts, out)
	}
	var inputs []PinnedSkillFiles
	for _, skill := range r.skills {
		inputs = append(inputs, skill)
	}
	prepared, err := PreparePinnedSkills(inputs)
	if err != nil {
		return nil, err
	}
	for _, skill := range prepared {
		def.Skills = append(def.Skills, skill.Skill)
	}
	if root.Type == model.PluginTypeExpertTeam {
		instructions, ok := rawAttachmentContent(d.Plugin.Package, "AGENTS.md")
		if !ok {
			return nil, installationInvalid("definition.expert_team.instructions", "required")
		}
		if err = r.assets.text(instructions); err != nil {
			return nil, err
		}
		team := &fleet.CapabilityExpertTeam{Name: d.Plugin.Name, Description: manifestDescription(d.Plugin.Manifest), Instructions: instructions}
		leaders := 0
		keys := map[string]bool{}
		if !installationName(team.Name, 128) {
			return nil, ErrIntegrity
		}
		if err = r.assets.boundedText("definition.expert_team.description", team.Description, 255); err != nil {
			return nil, err
		}
		for i, rel := range memberRels {
			var wiring struct {
				Role      string `json:"role"`
				IsLeader  bool   `json:"is_leader"`
				MemberKey string `json:"member_key"`
			}
			if json.Unmarshal(rel.Data, &wiring) != nil || wiring.MemberKey == "" || keys[wiring.MemberKey] {
				return nil, installationInvalid("definition.expert_team.members", "invalid_wiring")
			}
			keys[wiring.MemberKey] = true
			if wiring.IsLeader {
				leaders++
			}
			if err = r.assets.boundedText("definition.expert_team.members.role", wiring.Role, 500); err != nil {
				return nil, err
			}
			if !wiring.IsLeader && wiring.Role == "leader" {
				return nil, ErrIntegrity
			}
			if wiring.IsLeader {
				wiring.Role = "leader"
			} else if wiring.Role == "" {
				wiring.Role = "member"
			}
			team.Members = append(team.Members, fleet.CapabilityTeamMember{ExpertName: def.Experts[i].Name, Role: wiring.Role, IsLeader: wiring.IsLeader})
		}
		if leaders != 1 {
			return nil, installationInvalid("definition.expert_team.members", "one_leader_required")
		}
		def.ExpertTeam = team
	}
	plan := &pinnedInstallationPlan{RootVersionID: rootVersion, Definition: def}
	for id := range r.plugins {
		plan.NodeIDs = append(plan.NodeIDs, id)
	}
	sort.Strings(plan.NodeIDs)
	for _, d := range r.loaded {
		for _, rel := range d.Relations {
			plan.Edges = append(plan.Edges, planEligibilityEdge{d.Plugin.ID, rel.TargetPluginID})
		}
	}
	sort.Slice(plan.Edges, func(i, j int) bool {
		a, b := plan.Edges[i], plan.Edges[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Target < b.Target
	})
	return plan, nil
}

func (r *pinnedInstallResolver) load(ctx context.Context, ids []string, keys []pluginrepo.InstallVersionKey) error {
	var missing []string
	seen := map[string]bool{}
	for _, id := range ids {
		if r.plugins[id] == nil && !seen[id] {
			missing = append(missing, id)
			seen[id] = true
		}
	}
	if len(missing) > 0 {
		got, err := r.store.GetInstallPlugins(ctx, scope(r.caller), missing)
		if err != nil {
			return mapStoreError(err)
		}
		for _, id := range missing {
			if got[id] == nil || got[id].ID != id || got[id].IsEmbedded || got[id].Status != 1 || got[id].DeletedAt != nil || got[id].ListingState != model.PluginListingStatePublished {
				return ErrDependencyHidden
			}
			r.plugins[id] = got[id]
		}
	}
	var versions []pluginrepo.InstallVersionKey
	seenKeys := map[pluginrepo.InstallVersionKey]bool{}
	for _, key := range keys {
		if r.versions[key] == nil && !seenKeys[key] {
			versions = append(versions, key)
			seenKeys[key] = true
		}
	}
	if len(versions) > 0 {
		got, err := r.store.GetInstallVersions(ctx, scope(r.caller), versions)
		if err != nil {
			return mapStoreError(err)
		}
		for _, key := range versions {
			if got[key] == nil {
				return ErrDependencyHidden
			}
			r.versions[key] = got[key]
		}
	}
	return nil
}
func (r *pinnedInstallResolver) loadRelations(ctx context.Context, rels []model.PluginRelation) error {
	var ids []string
	var keys []pluginrepo.InstallVersionKey
	for _, rel := range rels {
		if rel.Status != 1 || rel.DeletedAt != nil {
			return ErrIntegrity
		}
		ids = append(ids, rel.TargetPluginID)
		if rel.Type == "expert_connector" {
			if rel.TargetVersionID != nil {
				return ErrIntegrity
			}
			continue
		}
		if rel.TargetVersionID == nil || *rel.TargetVersionID == "" {
			return installationInvalid("relations.target_version_id", "required")
		}
		keys = append(keys, pluginrepo.InstallVersionKey{PluginID: rel.TargetPluginID, VersionID: *rel.TargetVersionID})
	}
	return r.load(ctx, ids, keys)
}
func (r *pinnedInstallResolver) detail(ctx context.Context, key pluginrepo.InstallVersionKey) (*Detail, error) {
	if d := r.loaded[key]; d != nil {
		return d, nil
	}
	p, v := r.plugins[key.PluginID], r.versions[key]
	if p == nil || v == nil || v.ID != key.VersionID || v.PluginID != key.PluginID || v.Release == nil || v.Release.PluginID != key.PluginID || v.Release.VersionID != key.VersionID || v.SnapshotSchemaVersion == nil || *v.SnapshotSchemaVersion != model.PluginSnapshotSchemaVersion || len(v.Artifacts) == 0 {
		return nil, ErrIntegrity
	}
	if err := r.s.verifyVersionFiles(ctx, p, v); err != nil {
		return nil, err
	}
	if rejectInlineCapabilities(p.Type, v.Package) != nil || rejectInlineCapabilities(p.Type, v.Manifest) != nil {
		return nil, installationInvalid("definition", "unsupported_inline_capabilities")
	}
	rels, err := model.DecodeSnapshotRelations(v.Relations)
	if err != nil {
		return nil, ErrIntegrity
	}
	cp := *p
	cp.Manifest = v.Manifest
	cp.Package = v.Package
	cp.AttachmentKeys = v.AttachmentKeys
	cp.CurrentVersionID = &v.ID
	var manifest struct {
		PluginName string `json:"plugin_name"`
	}
	if json.Unmarshal(v.Manifest, &manifest) != nil || manifest.PluginName == "" {
		return nil, ErrIntegrity
	}
	cp.Name = manifest.PluginName
	d := &Detail{Plugin: &cp, Relations: rels}
	r.loaded[key] = d
	return d, nil
}
func (r *pinnedInstallResolver) referenced(ctx context.Context, source *model.Plugin, rel model.PluginRelation, kind model.PluginType) (*Detail, error) {
	p := r.plugins[rel.TargetPluginID]
	if p == nil || p.Type != kind || !installAudience(source, p) || rel.SourcePluginID != source.ID || rel.TargetVersionID == nil {
		return nil, ErrDependencyHidden
	}
	return r.detail(ctx, pluginrepo.InstallVersionKey{PluginID: rel.TargetPluginID, VersionID: *rel.TargetVersionID})
}
func installAudience(source, target *model.Plugin) bool {
	a, b := model.NormalizeLegacyVisibility(source.Visibility), model.NormalizeLegacyVisibility(target.Visibility)
	if b == model.PluginVisibilitySystem {
		return true
	}
	sameSpace := source.SpaceID != nil && target.SpaceID != nil && *source.SpaceID == *target.SpaceID
	if a == model.PluginVisibilitySystem {
		return false
	}
	if b == model.PluginVisibilitySpace {
		return sameSpace
	}
	return a == model.PluginVisibilityPrivate && b == model.PluginVisibilityPrivate && sameSpace && source.OwnerUID == target.OwnerUID
}
func uniqueStrings(in []string) []string {
	out := in[:0]
	for _, v := range in {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}
