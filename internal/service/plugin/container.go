// Container import ingests an uploaded expert or expert_team container archive
// and, server-side, stores it as the unified plugin graph in one transaction:
// the expert/team plugin, its bundled skills as separate skill plugins, its
// squad members as separate expert plugins, and the relations wiring them
// (expert_skill, expert_team_expert). The archive is treated as hostile input —
// the outer zip and every bundled skill package pass the shared
// zip-slip/symlink/decompression-bomb/size-cap guards in internal/service/parse.
//
// The expert/team/member package documents are rendered through the shared
// internal/plugindoc renderers (AGENTS.md, sanitized mcp.json) so an imported
// expert is byte-identical to a backfilled one; bundled skills are parsed into
// the canonical flat attachment tree exactly like a normal skill import.

package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/plugindoc"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
)

// ErrInvalidContainer collapses every container parse/validation failure into
// one opaque error so a malformed upload cannot probe internal structure. It
// wraps ErrInvalidRequest so the handler renders it as a 400 VALIDATION_ERROR.
var ErrInvalidContainer = fmt.Errorf("invalid expert/expert_team container: %w", ErrInvalidRequest)

// Container field limits mirror octo-admin parseContainer.ts so the server
// rejects the same oversized/malformed input the browser does.
const (
	containerMaxNameLen       = 128
	containerMaxSummaryLen    = 512
	containerMaxSkills        = 20
	containerMaxMembers       = 30
	containerMaxStrategies    = 30
	containerMaxTags          = 20
	containerMaxMCPConfigLen  = 64 * 1024
	containerMaxManifestBytes = 1 << 20
	containerMaxArchiveFiles  = 64
)

// ContainerImportParams carries an admin container upload. Archive is the raw
// zip bytes; CategoryID optionally places the top-level expert/team under a
// plugin category (the free-text manifest category is display-only and not
// mapped to a category row).
type ContainerImportParams struct {
	Archive    []byte
	CategoryID *string
}

// ---- parsed container model (mirrors octo-admin parseContainer.ts) ----------

type parsedSkillRef struct {
	name string
	file string
}

type parsedExpert struct {
	name        string
	summary     string
	tags        []string
	instruction string
	mcpConfig   string
	skills      []parsedSkillRef
}

type parsedMember struct {
	memberKey   string
	name        string
	role        string
	isLeader    bool
	instruction string
	mcpConfig   string
	skills      []parsedSkillRef
}

type parsedSquad struct {
	name            string
	summary         string
	tags            []string
	leader          string
	strategies      []string
	depsBlocking    []string
	depsRecommended []string
	permission      string
	members         []parsedMember
}

type parsedContainer struct {
	kind    model.PluginType // expert or expert_team
	expert  *parsedExpert
	squad   *parsedSquad
	skillZs map[string][]byte // container path -> bundled skill package bytes
}

// ImportContainer parses an uploaded expert/expert_team container and stores it
// as the unified plugin graph in one transaction. It returns the top-level
// expert/team Detail (its plugin plus the relations to its skills/members).
func (s *Service) ImportContainer(ctx context.Context, caller Caller, p ContainerImportParams) (*Detail, error) {
	if strings.TrimSpace(caller.UID) == "" {
		return nil, ErrInvalidRequest
	}
	if s.storage == nil {
		return nil, errors.New("plugin container import is not configured")
	}
	if int64(len(p.Archive)) > s.maxArchiveBytes {
		return nil, ErrTooLarge
	}
	parsed, err := s.parseContainer(p.Archive)
	if err != nil {
		return nil, err
	}
	// Admin identity: system visibility in the empty global Space, matching the
	// AdminCreate convention for expert/skill/expert_team.
	eff := caller
	eff.IsSystemAdmin = true
	eff.SpaceID = adminGlobalSpace

	switch parsed.kind {
	case model.PluginTypeExpert:
		return s.importExpertContainer(ctx, eff, parsed, p.CategoryID)
	default:
		return s.importSquadContainer(ctx, eff, parsed, p.CategoryID)
	}
}

// ReuploadContainer re-uploads an expert/expert_team container archive to rebuild
// an EXISTING top plugin in place, preserving its plugin_id, visibility, Space,
// owner, creator provenance, and market placement while replacing its
// package/manifest/tags and swapping every embedded child (the bundled skills of
// an expert; the member experts and their skills of a squad). The rebuild is one
// transaction (RebuildGraph): the old children and their relations are
// soft-deleted so they never surface again, and the new children are fresh,
// IsEmbedded rows. It returns the rebuilt top-level Detail, the same shape as
// ImportContainer. The container kind must match the existing plugin's type.
func (s *Service) ReuploadContainer(ctx context.Context, caller Caller, pluginID string, p ContainerImportParams) (*Detail, error) {
	if strings.TrimSpace(caller.UID) == "" {
		return nil, ErrInvalidRequest
	}
	if s.storage == nil {
		return nil, errors.New("plugin container import is not configured")
	}
	if int64(len(p.Archive)) > s.maxArchiveBytes {
		return nil, ErrTooLarge
	}
	topID, err := parseStorageID(pluginID)
	if err != nil {
		return nil, err
	}
	// Resolve the reupload target cross-Space (adminScope), matching SkillReupload.
	old, _, err := s.repo.GetWithRelations(ctx, adminScope(caller), topID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	// Only a container top (expert or expert_team) can be rebuilt through this
	// path; any other row — including an embedded squad member, itself an expert
	// row — is reported as not found so it cannot be content-swapped out of band
	// and so the endpoint cannot probe types.
	if old.Type != model.PluginTypeExpert && old.Type != model.PluginTypeExpertTeam {
		return nil, ErrNotFound
	}
	if old.IsEmbedded {
		return nil, ErrNotFound
	}
	parsed, err := s.parseContainer(p.Archive)
	if err != nil {
		return nil, err
	}
	// The uploaded archive kind must match the existing plugin's type — an expert
	// zip cannot rebuild a squad row, or vice versa.
	if parsed.kind != old.Type {
		return nil, ErrInvalidContainer
	}
	// The old embedded children (bundled skills for an expert; members + their
	// skills for a squad) are soft-deleted by RebuildGraph so they stop surfacing
	// anywhere. Their IDs are derived INSIDE the rebuild transaction, after the top
	// is FOR UPDATE-locked, not from this unlocked pre-parse read — a multi-second
	// parse window otherwise lets a concurrent reupload/delete commit a swap that a
	// stale snapshot would orphan.

	// Admin identity for building the replacement graph, matching ImportContainer.
	// The object namespace / canonicalization Space must be the ROW's real Space
	// (P1-1), not the empty global Space, so a spilled storage attachment resolves.
	eff := caller
	eff.IsSystemAdmin = true
	eff.SpaceID = adminGlobalSpace
	if old.SpaceID != nil {
		eff.SpaceID = *old.SpaceID
	}

	var top *model.Plugin
	var topRels []model.PluginRelation
	var childNodes []pluginrepo.Mutation
	var uploaded []string
	if parsed.kind == model.PluginTypeExpert {
		top, topRels, childNodes, uploaded, err = s.buildExpertGraph(ctx, eff, parsed, p.CategoryID, topID)
	} else {
		top, topRels, childNodes, uploaded, err = s.buildSquadGraph(ctx, eff, parsed, p.CategoryID, topID)
	}
	if err != nil {
		s.deleteObjects(ctx, uploaded...)
		return nil, err
	}
	// Preserve the row's identity: a rebuild replaces content, never the plugin_id,
	// visibility, listing state, Space, owner, publisher, creator provenance, or
	// created_at. The container carries no icon/publisher, so keep the existing ones
	// rather than clearing them (RebuildGraph re-derives visibility/listing_state/
	// Space/owner from the locked row — normalizing a preserved legacy `public` to
	// `system` there — while publisher/icon/category keep this unlocked read —
	// content, not identity).
	top.Visibility = old.Visibility
	top.ListingState = old.ListingState
	top.SpaceID = old.SpaceID
	top.OwnerUID = old.OwnerUID
	top.Publisher = old.Publisher
	top.CreatedAt = old.CreatedAt
	top.CurrentVersionID = old.CurrentVersionID
	top.CurrentVersion = old.CurrentVersion
	top.CreatorName, top.CreatedByType = old.CreatorName, old.CreatedByType
	top.CreatedByBotUID, top.CreatedByBotName = old.CreatedByBotUID, old.CreatedByBotName
	top.Icon = old.Icon
	// Preserve the existing category when the reupload does not supply one, so a
	// package-only rebuild does not wipe the row's category (and desync it from
	// the surviving market placement, which keeps the old category).
	if top.CategoryID == nil {
		top.CategoryID = old.CategoryID
	}
	// Every rebuilt embedded child (an expert's bundled skills; a squad's member
	// experts and their skills) inherits the container top's visibility/Space/owner
	// rather than the system/global-Space defaults buildGraphPlugin mints — else
	// reuploading a space/private container would silently promote its parts to
	// platform-global. The IMPORT path keeps minting `system` because its top is
	// `system`; only the reupload path re-stamps, mirroring how the top is preserved.
	//
	// listing_state travels with visibility for the same reason and one more: a
	// child left `published` under a draft top would be independently readable, and
	// a child left `draft` under a published top makes every non-owner install fail
	// with ErrDependencyHidden (resolveInstallDetail sees fewer visible targets than
	// CountDeclaredRelations).
	//
	// All four fields are stamped again inside RebuildGraph from the FOR
	// UPDATE-locked row; the values read here are the pre-parse ones and lose to a
	// concurrent edit/delist/approve, which is the intended precedence.
	for i := range childNodes {
		childNodes[i].Plugin.Visibility = old.Visibility
		childNodes[i].Plugin.ListingState = old.ListingState
		childNodes[i].Plugin.SpaceID = old.SpaceID
		childNodes[i].Plugin.OwnerUID = old.OwnerUID
	}

	audit := s.audit(eff, top.ID, "update", old, top, s.now())
	topMut := mutation(*top, topRels, audit)
	topMut.SnapshotVersion = true
	sync, err := s.repo.RebuildGraph(ctx, adminScope(eff), topMut, childNodes)
	if err != nil {
		s.deleteObjects(ctx, uploaded...)
		return nil, mapStoreError(err)
	}
	return s.topLevelDetail(ctx, top, sync), nil
}

// collectContainerChildren was removed: the embedded child set is now derived
// inside the RebuildGraph / DeleteGraph transaction, under the top's FOR UPDATE
// lock, by the repository (collectEmbeddedChildren). Deriving it here from an
// unlocked pre-parse read let a concurrent reupload/delete orphan a swapped-in
// child; see the round-5 concurrency fix and the repo helper's doc comment.

// importExpertContainer builds the skill plugins + the expert plugin and stores
// the graph. Object uploads (binary skill files) are rolled back if the DB
// transaction fails, so no orphan objects survive a partial import.
func (s *Service) importExpertContainer(ctx context.Context, eff Caller, parsed *parsedContainer, categoryID *string) (*Detail, error) {
	top, topRels, childNodes, uploaded, err := s.buildExpertGraph(ctx, eff, parsed, categoryID, "")
	if err != nil {
		s.deleteObjects(ctx, uploaded...)
		return nil, err
	}
	// Only the top expert is a standalone market entry — auto-attach a default
	// visible placement so it surfaces in the tenant market. Bundled skills are
	// wired via relations, not listed on their own, so they carry no placement.
	expertNode := s.graphMutation(eff, top, topRels)
	expertNode.Placements = []model.PluginPlacement{defaultMarketPlacement(top.CategoryID)}
	expertNode.SnapshotVersion = true
	nodes := append(childNodes, expertNode)

	syncs, err := s.repo.CreateGraph(ctx, adminScope(eff), nodes)
	if err != nil {
		s.deleteObjects(ctx, uploaded...)
		return nil, mapStoreError(err)
	}
	return s.topLevelDetail(ctx, top, syncs[len(syncs)-1]), nil
}

// buildExpertGraph builds the bundled skill child nodes, the top expert plugin,
// and the expert's expert_skill relations from a parsed expert container. It
// persists nothing — the import and reupload paths attach the placement (or
// preserve identity) and drive the write. topID, when non-empty, pins the top
// expert's ID (reupload preserves it); empty mints a fresh one. The returned
// uploaded object keys are always returned (even on error) so the caller can roll
// them back. The bundled skills are marked IsEmbedded=true by buildSkillNode.
func (s *Service) buildExpertGraph(ctx context.Context, eff Caller, parsed *parsedContainer, categoryID *string, topID string) (*model.Plugin, []model.PluginRelation, []pluginrepo.Mutation, []string, error) {
	e := parsed.expert
	var childNodes []pluginrepo.Mutation
	var uploaded []string
	var expertRels []model.PluginRelation

	state := s.newSkillBuildState()
	seenTargets := map[string]struct{}{}
	for i, ref := range e.skills {
		skillNode, keys, isNew, err := s.buildSkillNode(ctx, eff, parsed, ref, state)
		uploaded = append(uploaded, keys...)
		if err != nil {
			return nil, nil, nil, uploaded, err
		}
		if isNew {
			childNodes = append(childNodes, skillNode.mutation)
		}
		// One expert wires at most one expert_skill edge to a given skill target;
		// two refs collapsing onto the same nested archive do not create a duplicate
		// edge (which validateGraphRelations / lockRelationTargets would reject).
		if _, dup := seenTargets[skillNode.plugin.ID]; dup {
			continue
		}
		seenTargets[skillNode.plugin.ID] = struct{}{}
		expertRels = append(expertRels, s.skillRelation(eff, skillNode.plugin, i))
	}

	manifest, pkg, err := expertDocuments(e)
	if err != nil {
		return nil, nil, nil, uploaded, err
	}
	expertPlugin, err := s.buildGraphPlugin(ctx, eff, model.PluginTypeExpert, e.name, e.tags, categoryID, manifest, pkg, topID)
	if err != nil {
		return nil, nil, nil, uploaded, err
	}
	for i := range expertRels {
		expertRels[i].SourcePluginID = expertPlugin.ID
	}
	if err := validateGraphRelations(expertPlugin.Type, expertRels); err != nil {
		return nil, nil, nil, uploaded, err
	}
	return expertPlugin, expertRels, childNodes, uploaded, nil
}

// importSquadContainer builds every member's skills, the member expert plugins,
// the team plugin, and the relations wiring them, then stores the graph.
func (s *Service) importSquadContainer(ctx context.Context, eff Caller, parsed *parsedContainer, categoryID *string) (*Detail, error) {
	top, topRels, childNodes, uploaded, err := s.buildSquadGraph(ctx, eff, parsed, categoryID, "")
	if err != nil {
		s.deleteObjects(ctx, uploaded...)
		return nil, err
	}
	// Only the top team is a standalone market entry — auto-attach a default
	// visible placement. Member experts and their skills are wired via relations,
	// not listed on their own, so they carry no placement.
	teamNode := s.graphMutation(eff, top, topRels)
	teamNode.Placements = []model.PluginPlacement{defaultMarketPlacement(top.CategoryID)}
	teamNode.SnapshotVersion = true
	nodes := append(childNodes, teamNode)

	syncs, err := s.repo.CreateGraph(ctx, adminScope(eff), nodes)
	if err != nil {
		s.deleteObjects(ctx, uploaded...)
		return nil, mapStoreError(err)
	}
	return s.topLevelDetail(ctx, top, syncs[len(syncs)-1]), nil
}

// buildSquadGraph builds each member's skill child nodes, the member expert
// child nodes, the top team plugin, and the team's expert_team_expert relations
// from a parsed squad container. It persists nothing — the import and reupload
// paths drive the write. topID, when non-empty, pins the top team's ID (reupload
// preserves it); empty mints a fresh one. Members and their bundled skills are
// marked IsEmbedded=true so they never surface in the catalog on their own. The
// child node order (each member's skills, then the member) matches the import so
// the reupload rebuilds an identical graph shape.
func (s *Service) buildSquadGraph(ctx context.Context, eff Caller, parsed *parsedContainer, categoryID *string, topID string) (*model.Plugin, []model.PluginRelation, []pluginrepo.Mutation, []string, error) {
	sq := parsed.squad
	var childNodes []pluginrepo.Mutation
	var uploaded []string
	var teamRels []model.PluginRelation

	teamManifest, teamPkg, err := teamDocuments(sq)
	if err != nil {
		return nil, nil, nil, uploaded, err
	}
	teamPlugin, err := s.buildGraphPlugin(ctx, eff, model.PluginTypeExpertTeam, sq.name, sq.tags, categoryID, teamManifest, teamPkg, topID)
	if err != nil {
		return nil, nil, nil, uploaded, err
	}

	// One shared build state spans the whole squad so a nested archive bundled by
	// several members is parsed/expanded exactly once and charged against a single
	// container-wide budget.
	state := s.newSkillBuildState()
	for i := range sq.members {
		member := sq.members[i]
		var memberRels []model.PluginRelation
		seenTargets := map[string]struct{}{}
		for j, ref := range member.skills {
			skillNode, keys, isNew, err := s.buildSkillNode(ctx, eff, parsed, ref, state)
			uploaded = append(uploaded, keys...)
			if err != nil {
				return nil, nil, nil, uploaded, err
			}
			if isNew {
				childNodes = append(childNodes, skillNode.mutation)
			}
			if _, dup := seenTargets[skillNode.plugin.ID]; dup {
				continue
			}
			seenTargets[skillNode.plugin.ID] = struct{}{}
			memberRels = append(memberRels, s.skillRelation(eff, skillNode.plugin, j))
		}
		manifest, pkg, err := memberDocuments(member)
		if err != nil {
			return nil, nil, nil, uploaded, err
		}
		memberPlugin, err := s.buildGraphPlugin(ctx, eff, model.PluginTypeExpert, member.name, nil, nil, manifest, pkg, "")
		if err != nil {
			return nil, nil, nil, uploaded, err
		}
		// A team member is a part of its parent team, not a standalone catalog
		// entry — mark it embedded so it never appears in the expert list, only
		// via the team's detail and expert_team_expert relations.
		memberPlugin.IsEmbedded = true
		for k := range memberRels {
			memberRels[k].SourcePluginID = memberPlugin.ID
		}
		if err := validateGraphRelations(memberPlugin.Type, memberRels); err != nil {
			return nil, nil, nil, uploaded, err
		}
		childNodes = append(childNodes, s.graphMutation(eff, memberPlugin, memberRels))
		teamRels = append(teamRels, s.memberRelation(eff, memberPlugin, member, i))
	}

	for i := range teamRels {
		teamRels[i].SourcePluginID = teamPlugin.ID
	}
	if err := validateGraphRelations(teamPlugin.Type, teamRels); err != nil {
		return nil, nil, nil, uploaded, err
	}
	return teamPlugin, teamRels, childNodes, uploaded, nil
}

// ---- node builders ----------------------------------------------------------

type graphNode struct {
	plugin   *model.Plugin
	mutation pluginrepo.Mutation
}

// skillBuildState is the per-container shared state threaded through every
// bundled-skill build so a nested archive referenced by many skill refs is
// expanded within one shared budget. cache maps a container-relative skill file
// AND its display name to the skill node already built for that pair, so two
// refs to the same file under DIFFERENT names each mint their own node (a member
// may bundle a shared archive under its own skill name) while an exact
// (file,name) repeat collapses onto one node. budget is the shared
// remaining-decompressed-bytes allowance passed to buildSkillAttachmentTree so
// the AGGREGATE expansion is bounded: EVERY expansion (each distinct (file,name)
// that misses the cache) is charged, so a shared archive re-materialized under N
// names costs N expansions against the container budget — the same bytes an
// attacker forces the server to decompress N times. An exact (file,name) repeat
// resolves from the cache without re-expanding, so it is never charged.
type skillBuildState struct {
	cache  map[string]*graphNode
	budget int64
}

func (s *Service) newSkillBuildState() *skillBuildState {
	return &skillBuildState{cache: map[string]*graphNode{}, budget: s.maxArchiveBytes}
}

// buildSkillNode parses one bundled skill package into a skill plugin exactly
// like a normal skill import (canonical flat attachment tree). It returns the
// node, the object keys uploaded for binary files (for rollback), and whether
// the node is newly built (isNew) — an exact (file,name) repeat resolves from the
// shared cache instead of re-expanding, so the caller adds the node to the graph
// only on the first occurrence. Two refs sharing a file under different display
// names each mint a distinct skill node (their own id/name/object namespace) and
// each re-materializes the archive, so each is charged against the container
// budget: the aggregate cap bounds the total bytes the server decompresses, not
// just the count of distinct files.
func (s *Service) buildSkillNode(ctx context.Context, eff Caller, parsed *parsedContainer, ref parsedSkillRef, state *skillBuildState) (node *graphNode, uploaded []string, isNew bool, err error) {
	if ref.file == "" {
		return nil, nil, false, ErrInvalidContainer
	}
	cacheKey := ref.file + "\x00" + ref.name
	if cached, ok := state.cache[cacheKey]; ok {
		return cached, nil, false, nil
	}
	zipData, ok := parsed.skillZs[ref.file]
	if !ok {
		return nil, nil, false, ErrInvalidContainer
	}
	skillID := s.id()
	// Charge every expansion against the shared container budget: a re-materialized
	// (file,name) miss decompresses the same bytes again, so it must draw down the
	// aggregate allowance too. Only an exact (file,name) cache hit (handled above)
	// skips the charge, because it does not re-expand.
	attachments, uploaded, _, err := s.buildSkillAttachmentTree(ctx, eff.SpaceID, skillID, zipData, nil, &state.budget)
	if err != nil {
		return nil, uploaded, false, err
	}
	manifest, err := skillManifest(ref.name)
	if err != nil {
		return nil, uploaded, false, err
	}
	pkg, err := json.Marshal(map[string]any{"$schema": packageSchema, "attachments": attachments})
	if err != nil {
		return nil, uploaded, false, ErrInvalidRequest
	}
	// Persist under the reserved ID the spilled object keys namespace under, so
	// the row, the object namespace, and any storage_uri all agree.
	plugin, err := s.buildGraphPlugin(ctx, eff, model.PluginTypeSkill, ref.name, nil, nil, manifest, pkg, skillID)
	if err != nil {
		return nil, uploaded, false, err
	}
	// A bundled skill is a part of its parent expert/team, not a standalone
	// catalog entry — mark it embedded so it is reachable via detail/relations
	// but excluded from the skill list (matching the backfill's embedded skills).
	plugin.IsEmbedded = true
	built := &graphNode{plugin: plugin, mutation: s.graphMutation(eff, plugin, nil)}
	state.cache[cacheKey] = built
	return built, uploaded, true, nil
}

// buildGraphPlugin canonicalizes a plugin's documents and constructs its
// current-state row under the admin identity (system-visible, empty global
// Space). It builds no relations — those are attached separately so intra-graph
// targets resolve inside CreateGraph. reservedID pins the plugin ID when
// non-empty (skills reserve it up front to namespace object keys); otherwise a
// fresh ID is minted.
func (s *Service) buildGraphPlugin(ctx context.Context, eff Caller, typ model.PluginType, name string, tags []string, categoryID *string, manifest, pkg json.RawMessage, reservedID string) (*model.Plugin, error) {
	tagsJSON, err := canonicalJSONValue(nonNilTags(tags))
	if err != nil {
		return nil, ErrInvalidRequest
	}
	req := WriteRequest{
		Name:       name,
		Type:       typ,
		CategoryID: categoryID,
		Tags:       tagsJSON,
		Visibility: model.PluginVisibilitySystem,
		Manifest:   manifest,
		Package:    pkg,
	}
	p, _, err := s.buildWrite(ctx, eff, "", req, s.now(), true)
	if err != nil {
		return nil, err
	}
	p.ID = reservedID
	if p.ID == "" {
		p.ID = s.id()
	}
	// A container graph minted here is `system`-visible and immediately live: the
	// admin import IS the publish. buildWrite mints the tenant default (draft), so
	// re-stamp it, exactly as the ID is re-stamped above. The reupload path
	// overrides both visibility and listing state again from the locked row.
	p.ListingState = model.PluginListingStatePublished
	return p, nil
}

func (s *Service) graphMutation(eff Caller, p *model.Plugin, rels []model.PluginRelation) pluginrepo.Mutation {
	audit := s.audit(eff, p.ID, "create", nil, p, s.now())
	return mutation(*p, rels, audit)
}

// ---- relation builders ------------------------------------------------------

func (s *Service) skillRelation(eff Caller, skill *model.Plugin, index int) model.PluginRelation {
	data, _ := json.Marshal(map[string]any{"source_index": index})
	return model.PluginRelation{
		TargetPluginID: skill.ID, TargetPluginType: model.PluginTypeSkill,
		Type: "expert_skill", SortOrder: index, Data: data, Status: 1,
		CreatedBy: eff.UID, CreatedAt: s.now(), UpdatedAt: s.now(),
	}
}

func (s *Service) memberRelation(eff Caller, member *model.Plugin, m parsedMember, index int) model.PluginRelation {
	data, _ := json.Marshal(map[string]any{
		"source_index": index, "member_key": m.memberKey, "role": m.role, "is_leader": m.isLeader,
	})
	return model.PluginRelation{
		TargetPluginID: member.ID, TargetPluginType: model.PluginTypeExpert,
		Type: "expert_team_expert", SortOrder: index, Data: data, Status: 1,
		CreatedBy: eff.UID, CreatedAt: s.now(), UpdatedAt: s.now(),
	}
}

// validateGraphRelations enforces the endpoint matrix on the in-graph edges
// before they reach the repo (which re-checks under lock), so a bad edge fails
// before any object upload or write commits.
func validateGraphRelations(sourceType model.PluginType, rels []model.PluginRelation) error {
	if len(rels) > maxRelations {
		return ErrInvalidRequest
	}
	seen := make(map[string]struct{}, len(rels))
	for _, r := range rels {
		if r.TargetPluginID == "" || !validRelationType(r.Type, sourceType, r.TargetPluginType) {
			return ErrInvalidRequest
		}
		key := r.Type + "\x00" + r.TargetPluginID
		if _, dup := seen[key]; dup {
			return ErrInvalidRequest
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (s *Service) topLevelDetail(ctx context.Context, p *model.Plugin, sync *pluginrepo.RelationSync) *Detail {
	p.IconURL = s.resolveIcon(ctx, p.Icon)
	// The container write snapshotted the top node and advanced its
	// current_version_id; stamp the new row id onto the response so it agrees
	// with the DB (a follow-up GET must not contradict it), mirroring the tenant
	// and admin single-plugin paths.
	if sync != nil && sync.NewVersionID != "" {
		p.CurrentVersionID = &sync.NewVersionID
	}
	rels := []model.PluginRelation{}
	if sync != nil && sync.Relations != nil {
		rels = sync.Relations
	}
	for i := range rels {
		rels[i].SourcePluginType = p.Type
	}
	return &Detail{Plugin: p, Relations: rels, RelationResult: relationResult(sync)}
}

// ---- document builders (byte-identical to the backfill via plugindoc) -------

func expertDocuments(e *parsedExpert) (json.RawMessage, json.RawMessage, error) {
	mcp, err := mcpAttachmentContent(e.mcpConfig)
	if err != nil {
		return nil, nil, err
	}
	attachments := []map[string]any{
		rawAttachmentMap("mcp.json", "application/json", mcp),
		rawAttachmentMap("AGENTS.md", "text/markdown", plugindoc.ExpertAgentsMarkdown(e.name, e.summary, e.instruction)),
	}
	return pluginDocuments(model.PluginTypeExpert, e.name, e.summary, e.tags, attachments)
}

func memberDocuments(m parsedMember) (json.RawMessage, json.RawMessage, error) {
	mcp, err := mcpAttachmentContent(m.mcpConfig)
	if err != nil {
		return nil, nil, err
	}
	context, err := jsonAttachmentContent(map[string]any{
		"is_leader": m.isLeader, "member_key": m.memberKey, "role": m.role, "template_id": "",
	})
	if err != nil {
		return nil, nil, err
	}
	attachments := []map[string]any{
		rawAttachmentMap("expert/context.json", "application/json", context),
		rawAttachmentMap("mcp.json", "application/json", mcp),
		rawAttachmentMap("AGENTS.md", "text/markdown", plugindoc.ExpertAgentsMarkdown(m.name, m.role, m.instruction)),
	}
	return pluginDocuments(model.PluginTypeExpert, m.name, m.role, nil, attachments)
}

func teamDocuments(sq *parsedSquad) (json.RawMessage, json.RawMessage, error) {
	strategies := make([]any, len(sq.strategies))
	for i, v := range sq.strategies {
		strategies[i] = v
	}
	deps := map[string]any{"blocking": anySlice(sq.depsBlocking), "recommended": anySlice(sq.depsRecommended)}
	agents := plugindoc.TeamAgentsMarkdown(sq.name, sq.summary, sq.leader, strategies, deps, sq.permission)
	attachments := []map[string]any{rawAttachmentMap("AGENTS.md", "text/markdown", agents)}
	return pluginDocuments(model.PluginTypeExpertTeam, sq.name, sq.summary, sq.tags, attachments)
}

func skillManifest(name string) (json.RawMessage, error) {
	return manifestDraft(model.PluginTypeSkill, name, "", nil)
}

// pluginDocuments assembles a manifest + package pair for one plugin, sorting
// attachments by path so the canonical package bytes match the backfill (which
// sorts identically in connectorPackageJSON).
func pluginDocuments(typ model.PluginType, name, description string, tags []string, attachments []map[string]any) (json.RawMessage, json.RawMessage, error) {
	manifest, err := manifestDraft(typ, name, description, tags)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(attachments, func(i, j int) bool {
		return attachments[i]["path"].(string) < attachments[j]["path"].(string)
	})
	pkg, err := json.Marshal(map[string]any{"$schema": packageSchema, "attachments": attachments})
	if err != nil {
		return nil, nil, ErrInvalidRequest
	}
	return manifest, pkg, nil
}

func manifestDraft(typ model.PluginType, name, description string, tags []string) (json.RawMessage, error) {
	draft := map[string]any{
		"$schema":     manifestSchema,
		"plugin_name": name,
		"plugin_type": string(typ),
		"name":        name,
		"description": description,
		"labels":      nonNilTags(tags),
		"examples":    []any{},
	}
	raw, err := json.Marshal(draft)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return raw, nil
}

// rawAttachmentMap builds one inline raw attachment map. The 2.0 contract
// forbids a derived content_size/content_hash on raw content (it carries its
// bytes inline), so only the path/mime/raw_content triple is set.
func rawAttachmentMap(path, mime, content string) map[string]any {
	return map[string]any{
		"path":         path,
		"content_type": "raw",
		"mime_type":    mime,
		"raw_content":  content,
	}
}

// mcpAttachmentContent canonicalizes a container mcp_config string into the
// rendered mcp.json attachment. It deliberately does NOT run a backend secret
// scanner: secret handling is a client-side control (the frontend sends
// `${PLACEHOLDER}` references, not values), and CLAUDE.md records that the
// heuristic backend value scanner was removed on purpose — the live import must
// not reject or blank config it is handed. The config was already validated as
// valid, size-bounded JSON by normalizeMCPConfig, so we only re-marshal it
// deterministically. (The offline backfill still uses SanitizeConnectorJSON.)
func mcpAttachmentContent(mcpConfig string) (string, error) {
	var cfg any
	if err := json.Unmarshal([]byte(mcpConfig), &cfg); err != nil {
		return "", ErrInvalidContainer
	}
	return jsonAttachmentContent(cfg)
}

func jsonAttachmentContent(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", ErrInvalidRequest
	}
	return string(raw), nil
}

func anySlice(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

// ---- container parsing (hostile input) --------------------------------------

func (s *Service) parseContainer(archive []byte) (*parsedContainer, error) {
	files, code, _ := parse.ExtractArchive(bytes.NewReader(archive), int64(len(archive)), s.maxArchiveBytes, s.maxArchiveBytes, containerMaxArchiveFiles)
	if code != "" {
		return nil, ErrInvalidContainer
	}
	expertJSON, hasExpert := files["expert.json"]
	squadJSON, hasSquad := files["squad.json"]
	if hasExpert == hasSquad { // both or neither
		return nil, ErrInvalidContainer
	}
	if hasExpert {
		if int64(len(expertJSON)) > containerMaxManifestBytes {
			return nil, ErrInvalidContainer
		}
		expert, err := parseExpertManifest(expertJSON)
		if err != nil {
			return nil, err
		}
		zs, err := collectSkillPackages(files, expert.skills)
		if err != nil {
			return nil, err
		}
		return &parsedContainer{kind: model.PluginTypeExpert, expert: expert, skillZs: zs}, nil
	}
	if int64(len(squadJSON)) > containerMaxManifestBytes {
		return nil, ErrInvalidContainer
	}
	squad, err := parseSquadManifest(squadJSON)
	if err != nil {
		return nil, err
	}
	var allRefs []parsedSkillRef
	for _, m := range squad.members {
		allRefs = append(allRefs, m.skills...)
	}
	zs, err := collectSkillPackages(files, allRefs)
	if err != nil {
		return nil, err
	}
	return &parsedContainer{kind: model.PluginTypeExpertTeam, squad: squad, skillZs: zs}, nil
}

func collectSkillPackages(files map[string][]byte, refs []parsedSkillRef) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, ref := range refs {
		if ref.file == "" {
			continue
		}
		data, ok := files[ref.file]
		if !ok {
			return nil, ErrInvalidContainer
		}
		out[ref.file] = data
	}
	return out, nil
}

type wireSkillRef struct {
	Name string `json:"name"`
	File string `json:"file"`
}

type wireExpert struct {
	Name        string          `json:"name"`
	Summary     string          `json:"summary"`
	Description string          `json:"description"`
	Tags        []string        `json:"tags"`
	Instruction string          `json:"instruction"`
	MCPConfig   json.RawMessage `json:"mcp_config"`
	Skills      []wireSkillRef  `json:"skills"`
}

type wireMember struct {
	MemberKey   string          `json:"member_key"`
	Name        string          `json:"name"`
	Role        string          `json:"role"`
	IsLeader    bool            `json:"is_leader"`
	Instruction string          `json:"instruction"`
	MCPConfig   json.RawMessage `json:"mcp_config"`
	Skills      []wireSkillRef  `json:"skills"`
}

type wireSquad struct {
	Name         string       `json:"name"`
	Summary      string       `json:"summary"`
	Description  string       `json:"description"`
	Tags         []string     `json:"tags"`
	Leader       string       `json:"leader"`
	Strategies   []string     `json:"strategies"`
	Dependencies wireDeps     `json:"dependencies"`
	Permission   string       `json:"permission"`
	Members      []wireMember `json:"members"`
}

type wireDeps struct {
	Blocking    []string `json:"blocking"`
	Recommended []string `json:"recommended"`
}

func parseExpertManifest(raw []byte) (*parsedExpert, error) {
	var w wireExpert
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, ErrInvalidContainer
	}
	name, summary, tags, err := commonMeta(w.Name, firstNonEmpty(w.Summary, w.Description), w.Tags)
	if err != nil {
		return nil, err
	}
	instruction := strings.TrimSpace(w.Instruction)
	if instruction == "" {
		return nil, ErrInvalidContainer
	}
	mcp, err := normalizeMCPConfig(w.MCPConfig)
	if err != nil {
		return nil, err
	}
	skills, err := normalizeSkillRefs(w.Skills)
	if err != nil {
		return nil, err
	}
	return &parsedExpert{name: name, summary: summary, tags: tags, instruction: instruction, mcpConfig: mcp, skills: skills}, nil
}

func parseSquadManifest(raw []byte) (*parsedSquad, error) {
	var w wireSquad
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, ErrInvalidContainer
	}
	name, summary, tags, err := commonMeta(w.Name, firstNonEmpty(w.Summary, w.Description), w.Tags)
	if err != nil {
		return nil, err
	}
	if len(w.Members) == 0 || len(w.Members) > containerMaxMembers {
		return nil, ErrInvalidContainer
	}
	strategies := normalizeStringList(w.Strategies)
	if len(strategies) > containerMaxStrategies {
		return nil, ErrInvalidContainer
	}
	seen := map[string]struct{}{}
	members := make([]parsedMember, 0, len(w.Members))
	leaderCount := 0
	for i, wm := range w.Members {
		mn := strings.TrimSpace(wm.Name)
		role := strings.TrimSpace(wm.Role)
		instruction := strings.TrimSpace(wm.Instruction)
		if mn == "" || role == "" || instruction == "" {
			return nil, ErrInvalidContainer
		}
		key := strings.TrimSpace(wm.MemberKey)
		if key == "" {
			key = "member-" + itoa(i+1)
		}
		if _, dup := seen[key]; dup {
			return nil, ErrInvalidContainer
		}
		seen[key] = struct{}{}
		mcp, err := normalizeMCPConfig(wm.MCPConfig)
		if err != nil {
			return nil, err
		}
		refs, err := normalizeSkillRefs(wm.Skills)
		if err != nil {
			return nil, err
		}
		if wm.IsLeader {
			leaderCount++
		}
		members = append(members, parsedMember{memberKey: key, name: mn, role: role, isLeader: wm.IsLeader, instruction: instruction, mcpConfig: mcp, skills: refs})
	}
	if leaderCount != 1 {
		return nil, ErrInvalidContainer
	}
	leader := strings.TrimSpace(w.Leader)
	if leader == "" {
		for _, m := range members {
			if m.isLeader {
				leader = m.name
				break
			}
		}
	}
	return &parsedSquad{
		name: name, summary: summary, tags: tags, leader: leader,
		strategies:      strategies,
		depsBlocking:    normalizeStringList(w.Dependencies.Blocking),
		depsRecommended: normalizeStringList(w.Dependencies.Recommended),
		permission:      strings.TrimSpace(w.Permission),
		members:         members,
	}, nil
}

func commonMeta(rawName, rawSummary string, rawTags []string) (string, string, []string, error) {
	name := strings.TrimSpace(rawName)
	if name == "" || len(name) > containerMaxNameLen {
		return "", "", nil, ErrInvalidContainer
	}
	summary := strings.TrimSpace(rawSummary)
	if summary == "" || len(summary) > containerMaxSummaryLen {
		return "", "", nil, ErrInvalidContainer
	}
	tags := normalizeStringList(rawTags)
	if len(tags) > containerMaxTags {
		return "", "", nil, ErrInvalidContainer
	}
	return name, summary, tags, nil
}

func normalizeSkillRefs(in []wireSkillRef) ([]parsedSkillRef, error) {
	if len(in) > containerMaxSkills {
		return nil, ErrInvalidContainer
	}
	out := make([]parsedSkillRef, 0, len(in))
	for _, ref := range in {
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			return nil, ErrInvalidContainer
		}
		file := strings.TrimSpace(ref.File)
		if file != "" {
			if unsafeContainerPath(file) || !hasSkillExtension(file) {
				return nil, ErrInvalidContainer
			}
		}
		out = append(out, parsedSkillRef{name: name, file: file})
	}
	return out, nil
}

// normalizeMCPConfig mirrors parseContainer.ts validateMcpConfig: absent/empty
// defaults to an empty server map; otherwise the value must be a JSON string
// carrying valid JSON within the size cap.
func normalizeMCPConfig(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == `""` {
		return `{"mcpServers":{}}`, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", ErrInvalidContainer
	}
	if strings.TrimSpace(s) == "" {
		return `{"mcpServers":{}}`, nil
	}
	if len(s) > containerMaxMCPConfigLen || !json.Valid([]byte(s)) {
		return "", ErrInvalidContainer
	}
	return s, nil
}

func normalizeStringList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func unsafeContainerPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return true
	}
	if len(p) >= 2 && p[1] == ':' {
		return true
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return strings.Contains(p, "..")
}

func hasSkillExtension(p string) bool {
	lower := strings.ToLower(p)
	return strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".skill")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
