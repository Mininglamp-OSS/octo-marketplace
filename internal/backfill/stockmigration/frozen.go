package stockmigration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/jcs"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

const (
	PlanSchemaVersion = "1.0"
	PlanActionCreate  = "create"
	PlanActionReuse   = "reuse"
	PlanActionUpdate  = "update_source"
	PlanSourceDB      = "plugin_version"
	RootNodeKey       = "root"
)

var (
	ErrPlanInvalid     = errors.New("migration plan invalid")
	ErrPlanStale       = errors.New("migration plan stale")
	ErrUnsupportedPlan = errors.New("migration plan uses an unsupported source")
)

// MigrationPlan is the complete frozen plan. plan_digest is intentionally not a
// field of the signed/canonical object; it is SHA-256(JCS(MigrationPlan)).
type MigrationPlan struct {
	SchemaVersion        string                  `json:"schema_version"`
	InputDigest          string                  `json:"input_digest"`
	SourceKind           string                  `json:"source_kind"`
	SourceIdentity       string                  `json:"source_identity"`
	SourceScope          string                  `json:"source_scope"`
	SourcePluginID       string                  `json:"source_plugin_id,omitempty"`
	SourceVersionID      string                  `json:"source_version_id,omitempty"`
	ExpectedEditRevision *uint64                 `json:"expected_edit_revision,omitempty"`
	Resources            []MigrationPlanResource `json:"resources"`
	Relations            []MigrationPlanRelation `json:"relations"`
}

// MigrationPlanResource is one source inventory node. CategoryID preserves the
// operator's explicit null for create actions.
type MigrationPlanResource struct {
	SourceNodeKey           string                 `json:"source_node_key"`
	OriginNodeKey           string                 `json:"origin_node_key"`
	PluginType              model.PluginType       `json:"plugin_type"`
	SourceContentDigest     string                 `json:"source_content_digest"`
	Action                  string                 `json:"action"`
	OwnerID                 string                 `json:"owner_id"`
	SpaceID                 *string                `json:"space_id"`
	Visibility              model.PluginVisibility `json:"visibility"`
	CategoryID              json.RawMessage        `json:"category_id,omitempty"`
	VersionLabel            *string                `json:"version_label,omitempty"`
	TargetPluginID          *string                `json:"target_plugin_id,omitempty"`
	TargetVersionID         *string                `json:"target_version_id,omitempty"`
	MCPServerKeys           []string               `json:"mcp_server_keys,omitempty"`
	TargetEffectiveRevision *uint64                `json:"target_effective_revision,omitempty"`
	TargetEffectiveDigest   *string                `json:"target_effective_digest,omitempty"`
}

// MigrationPlanRelation is one edge in the frozen graph. target_version_id is
// deliberately absent: B-model references are by target ID.
type MigrationPlanRelation struct {
	SourceNodeKey string          `json:"source_node_key"`
	TargetNodeKey string          `json:"target_node_key"`
	RelationType  string          `json:"relation_type"`
	SortOrder     int             `json:"sort_order"`
	Data          json.RawMessage `json:"data"`
}

// PreviewException is a sanitized blocking reason for a source node.
type PreviewException struct {
	SourceNodeKey string `json:"source_node_key"`
	Reason        string `json:"reason"`
	CanRetry      bool   `json:"can_retry"`
}

// SuggestedTarget is a candidate for operator review, never an approval.
type SuggestedTarget struct {
	PluginID     string  `json:"plugin_id"`
	VersionID    *string `json:"version_id,omitempty"`
	VersionLabel *string `json:"version_label,omitempty"`
}

// DiscoveryResource is the read-only inventory projection. It contains no
// credentials and no raw MCP configuration.
type DiscoveryResource struct {
	SourceNodeKey       string                 `json:"source_node_key"`
	OriginNodeKey       string                 `json:"origin_node_key"`
	PluginType          model.PluginType       `json:"plugin_type"`
	SourceContentDigest string                 `json:"source_content_digest"`
	PluginName          string                 `json:"plugin_name"`
	ProposedOwnerID     string                 `json:"proposed_owner_id"`
	ProposedSpaceID     *string                `json:"proposed_space_id"`
	ProposedVisibility  model.PluginVisibility `json:"proposed_visibility"`
	SuggestedTargets    []SuggestedTarget      `json:"suggested_targets"`
	MCPServerKeys       []string               `json:"mcp_server_keys,omitempty"`
}

// MigrationPreview is the CLI preview file format, not an HTTP payload.
type MigrationPreview struct {
	Phase                string                  `json:"phase"`
	InputDigest          string                  `json:"input_digest"`
	SourceKind           string                  `json:"source_kind"`
	SourceIdentity       string                  `json:"source_identity"`
	SourceScope          string                  `json:"source_scope"`
	SourcePluginID       string                  `json:"source_plugin_id,omitempty"`
	SourceVersionID      string                  `json:"source_version_id,omitempty"`
	ExpectedEditRevision *uint64                 `json:"expected_edit_revision,omitempty"`
	Resources            []DiscoveryResource     `json:"resources,omitempty"`
	Relations            []MigrationPlanRelation `json:"relations,omitempty"`
	Exceptions           []PreviewException      `json:"exceptions"`
	CanApply             bool                    `json:"can_apply"`
	Plan                 *MigrationPlan          `json:"plan,omitempty"`
	PlanDigest           string                  `json:"plan_digest,omitempty"`
	ProposedPlan         *MigrationPlan          `json:"proposed_plan,omitempty"`
}

type expectedNode struct {
	key       string
	originKey string
	node      Node
	source    Resource
	action    string
}

type expectedGraph struct {
	root         string
	rootResource Resource
	nodes        []expectedNode
	reuses       []expectedNode
}

// PreviewDiscovery builds a read-only discovery report for one database root.
func PreviewDiscovery(d Dataset, rootID string) (MigrationPreview, error) {
	g, unit, err := expectedForRoot(d, rootID)
	if err != nil {
		return MigrationPreview{}, err
	}
	identity, versionID, revision, err := sourceIdentity(d, rootID)
	if err != nil {
		return MigrationPreview{}, err
	}
	inputDigest, err := InputDigest(d, rootID)
	if err != nil {
		return MigrationPreview{}, err
	}
	scope, err := SourceScope(d, rootID)
	if err != nil {
		return MigrationPreview{}, err
	}
	preview := MigrationPreview{
		Phase:                "discovery",
		InputDigest:          inputDigest,
		SourceKind:           PlanSourceDB,
		SourceIdentity:       identity,
		SourceScope:          scope,
		SourcePluginID:       rootID,
		SourceVersionID:      versionID,
		ExpectedEditRevision: &revision,
		Resources:            []DiscoveryResource{},
		Relations:            []MigrationPlanRelation{},
		Exceptions:           []PreviewException{},
		CanApply:             false,
	}
	proposal := MigrationPlan{
		SchemaVersion:        PlanSchemaVersion,
		SourceKind:           PlanSourceDB,
		SourceIdentity:       identity,
		SourcePluginID:       rootID,
		SourceVersionID:      versionID,
		ExpectedEditRevision: &revision,
		Resources:            []MigrationPlanResource{},
		Relations:            []MigrationPlanRelation{},
	}
	for _, en := range g.nodes {
		digest, mcpKeys, err := sourceContentDigest(d, en)
		if err != nil {
			preview.Exceptions = append(preview.Exceptions, PreviewException{SourceNodeKey: en.key, Reason: "source_content_unavailable", CanRetry: true})
			continue
		}
		suggested := suggestedLabel(d, en)
		preview.Resources = append(preview.Resources, DiscoveryResource{
			SourceNodeKey:       en.key,
			OriginNodeKey:       en.originKey,
			PluginType:          en.node.Type,
			SourceContentDigest: digest,
			PluginName:          en.node.Plugin.Name,
			ProposedOwnerID:     en.node.Plugin.OwnerUID,
			ProposedSpaceID:     en.node.Plugin.SpaceID,
			ProposedVisibility:  en.node.Plugin.Visibility,
			SuggestedTargets:    []SuggestedTarget{},
			MCPServerKeys:       mcpKeys,
		})
		resource := MigrationPlanResource{
			SourceNodeKey:       en.key,
			OriginNodeKey:       en.originKey,
			PluginType:          en.node.Type,
			SourceContentDigest: digest,
			Action:              en.action,
			OwnerID:             en.node.Plugin.OwnerUID,
			SpaceID:             en.node.Plugin.SpaceID,
			Visibility:          en.node.Plugin.Visibility,
			MCPServerKeys:       mcpKeys,
		}
		switch en.action {
		case PlanActionUpdate:
			resource.TargetPluginID = stringPtr(rootID)
			if en.node.Type != model.PluginTypeConnector {
				resource.CategoryID = nullableString(en.node.Plugin.CategoryID)
				resource.VersionLabel = stringPtr(suggested)
			}
		case PlanActionCreate:
			if en.node.Type != model.PluginTypeConnector {
				resource.CategoryID = nullableString(en.node.Plugin.CategoryID)
				resource.VersionLabel = stringPtr(suggested)
			}
		}
		proposal.Resources = append(proposal.Resources, resource)
	}
	for _, en := range g.reuses {
		digest, mcpKeys, err := sourceContentDigest(d, en)
		if err != nil {
			preview.Exceptions = append(preview.Exceptions, PreviewException{SourceNodeKey: en.key, Reason: "reuse_target_unavailable", CanRetry: true})
			continue
		}
		preview.Resources = append(preview.Resources, DiscoveryResource{
			SourceNodeKey:       en.key,
			OriginNodeKey:       en.originKey,
			PluginType:          en.node.Type,
			SourceContentDigest: digest,
			PluginName:          en.node.Plugin.Name,
			ProposedOwnerID:     en.node.Plugin.OwnerUID,
			ProposedSpaceID:     en.node.Plugin.SpaceID,
			ProposedVisibility:  en.node.Plugin.Visibility,
			SuggestedTargets:    []SuggestedTarget{{PluginID: en.node.TargetID}},
			MCPServerKeys:       mcpKeys,
		})
		resource := MigrationPlanResource{
			SourceNodeKey:       en.key,
			OriginNodeKey:       en.originKey,
			PluginType:          en.node.Type,
			SourceContentDigest: digest,
			Action:              PlanActionReuse,
			OwnerID:             en.node.Plugin.OwnerUID,
			SpaceID:             en.node.Plugin.SpaceID,
			Visibility:          en.node.Plugin.Visibility,
			TargetPluginID:      stringPtr(en.node.TargetID),
			MCPServerKeys:       mcpKeys,
		}
		if en.node.Type == model.PluginTypeConnector {
			if effective, ok := d.Effective[en.node.TargetID]; ok {
				resource.TargetEffectiveRevision = &effective.Revision
				resource.TargetEffectiveDigest = stringPtr(effective.Digest)
			} else {
				preview.Exceptions = append(preview.Exceptions, PreviewException{SourceNodeKey: en.key, Reason: "reuse_connector_effective_missing", CanRetry: true})
			}
		}
		proposal.Resources = append(proposal.Resources, resource)
	}
	if len(unit.Exceptions) > 0 || len(preview.Exceptions) > 0 {
		for _, e := range unit.Exceptions {
			preview.Exceptions = append(preview.Exceptions, PreviewException{SourceNodeKey: e.ResourceID, Reason: e.Code, CanRetry: true})
		}
		preview.ProposedPlan = nil
		return preview, nil
	}
	proposal.Relations = relationsForGraph(g)
	sortPlan(&proposal)
	preview.ProposedPlan = &proposal
	return preview, nil
}

// Freeze validates a proposed plan against the current source snapshot and
// returns the canonical frozen plan and digest. The source snapshot is re-read
// by the caller; this function never writes.
func Freeze(d Dataset, proposed MigrationPlan) (MigrationPlan, string, error) {
	if proposed.SchemaVersion != PlanSchemaVersion || proposed.SourceKind != PlanSourceDB {
		return MigrationPlan{}, "", fmt.Errorf("%w: schema/source kind", ErrPlanInvalid)
	}
	rootID := strings.TrimSpace(proposed.SourcePluginID)
	if rootID == "" || proposed.SourceVersionID == "" || proposed.ExpectedEditRevision == nil {
		return MigrationPlan{}, "", fmt.Errorf("%w: database source identity", ErrPlanInvalid)
	}
	root, ok := findResource(d, rootID)
	if !ok {
		return MigrationPlan{}, "", fmt.Errorf("%w: source root missing", ErrPlanStale)
	}
	if root.Plugin.CurrentVersionID == nil || *root.Plugin.CurrentVersionID != proposed.SourceVersionID {
		return MigrationPlan{}, "", fmt.Errorf("%w: source version changed", ErrPlanStale)
	}
	if root.Plugin.EditRevision != *proposed.ExpectedEditRevision {
		return MigrationPlan{}, "", fmt.Errorf("%w: source edit revision changed", ErrPlanStale)
	}
	expectedIdentity := rootID + "@" + proposed.SourceVersionID
	if proposed.SourceIdentity != "" && proposed.SourceIdentity != expectedIdentity {
		return MigrationPlan{}, "", fmt.Errorf("%w: source identity", ErrPlanStale)
	}
	g, unit, err := expectedForRoot(d, rootID)
	if err != nil {
		return MigrationPlan{}, "", err
	}
	if len(unit.Exceptions) > 0 {
		return MigrationPlan{}, "", fmt.Errorf("%w: %s", ErrPlanInvalid, unit.Exceptions[0].Code)
	}
	expected, err := expectedResources(d, g)
	if err != nil {
		return MigrationPlan{}, "", err
	}
	if len(proposed.Resources) != len(expected) {
		return MigrationPlan{}, "", fmt.Errorf("%w: resource coverage", ErrPlanInvalid)
	}
	byKey := map[string]MigrationPlanResource{}
	for _, r := range proposed.Resources {
		if _, exists := byKey[r.SourceNodeKey]; exists {
			return MigrationPlan{}, "", fmt.Errorf("%w: duplicate resource", ErrPlanInvalid)
		}
		byKey[r.SourceNodeKey] = r
	}
	frozenResources := make([]MigrationPlanResource, 0, len(expected))
	for _, want := range expected {
		got, ok := byKey[want.SourceNodeKey]
		if !ok {
			return MigrationPlan{}, "", fmt.Errorf("%w: missing resource %s", ErrPlanInvalid, want.SourceNodeKey)
		}
		validated, _, err := validateResource(d, want, got)
		if err != nil {
			return MigrationPlan{}, "", err
		}
		frozenResources = append(frozenResources, validated)
	}
	for key := range byKey {
		found := false
		for _, want := range expected {
			if want.SourceNodeKey == key {
				found = true
				break
			}
		}
		if !found {
			return MigrationPlan{}, "", fmt.Errorf("%w: extra resource %s", ErrPlanInvalid, key)
		}
	}
	expectedRelations := relationsForGraph(g)
	if len(proposed.Relations) != len(expectedRelations) {
		return MigrationPlan{}, "", fmt.Errorf("%w: relation coverage", ErrPlanInvalid)
	}
	relations, err := validateRelations(expectedRelations, proposed.Relations, byKey)
	if err != nil {
		return MigrationPlan{}, "", err
	}
	inputDigest, err := InputDigest(d, rootID)
	if err != nil {
		return MigrationPlan{}, "", err
	}
	scope, err := SourceScope(d, rootID)
	if err != nil {
		return MigrationPlan{}, "", err
	}
	revision := root.Plugin.EditRevision
	plan := MigrationPlan{
		SchemaVersion:        PlanSchemaVersion,
		InputDigest:          inputDigest,
		SourceKind:           PlanSourceDB,
		SourceIdentity:       expectedIdentity,
		SourceScope:          scope,
		SourcePluginID:       rootID,
		SourceVersionID:      proposed.SourceVersionID,
		ExpectedEditRevision: &revision,
		Resources:            frozenResources,
		Relations:            relations,
	}
	sortPlan(&plan)
	digest, err := PlanDigest(plan)
	if err != nil {
		return MigrationPlan{}, "", err
	}
	return plan, digest, nil
}

// PlanDigest is SHA-256(JCS(plan)) in lowercase hex.
func PlanDigest(plan MigrationPlan) (string, error) {
	return jcs.Digest(plan)
}

// InputDigest covers the source closure: current snapshot, relations, verified
// skill bytes, ownership and Connector effective observations. It is named
// input_digest everywhere; there is no second source_digest domain.
func InputDigest(d Dataset, rootID string) (string, error) {
	g, _, err := expectedForRoot(d, rootID)
	if err != nil {
		return "", err
	}
	snapshot := struct {
		SourceIdentity string                        `json:"source_identity"`
		Root           inputNode                     `json:"root"`
		Nodes          []inputNode                   `json:"nodes"`
		Effective      map[string]ConnectorEffective `json:"effective,omitempty"`
	}{}
	root, versionID, revision, err := sourceIdentity(d, rootID)
	if err != nil {
		return "", err
	}
	snapshot.SourceIdentity = root
	rootResource, _ := findResource(d, rootID)
	snapshot.Root = inputNodeFor(d, g.root, rootResource, versionID, revision)
	nodes := []inputNode{}
	for _, en := range append(append([]expectedNode{}, g.nodes...), g.reuses...) {
		if en.key == RootNodeKey {
			continue
		}
		nodes = append(nodes, inputNodeFor(d, en.key, en.source, "", 0))
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Key < nodes[j].Key })
	snapshot.Nodes = nodes
	effective := map[string]ConnectorEffective{}
	for _, en := range g.reuses {
		if en.node.Type != model.PluginTypeConnector {
			continue
		}
		if value, ok := d.Effective[en.node.TargetID]; ok {
			effective[en.node.TargetID] = value
		}
	}
	if len(effective) > 0 {
		snapshot.Effective = effective
	}
	return jcs.Digest(snapshot)
}

type inputNode struct {
	Key                string                 `json:"key"`
	Plugin             model.Plugin           `json:"plugin"`
	PublishedVersionID *string                `json:"published_version_id,omitempty"`
	Relations          []model.PluginRelation `json:"relations"`
	Files              map[string]string      `json:"files,omitempty"`
	VersionID          string                 `json:"version_id,omitempty"`
	EditRevision       uint64                 `json:"edit_revision,omitempty"`
}

func inputNodeFor(d Dataset, key string, r Resource, versionID string, revision uint64) inputNode {
	relations := append([]model.PluginRelation(nil), r.Relations...)
	sort.Slice(relations, func(i, j int) bool { return relations[i].ID < relations[j].ID })
	return inputNode{
		Key:                key,
		Plugin:             r.Plugin,
		PublishedVersionID: r.PublishedVersionID,
		Relations:          relations,
		Files:              d.ResolvedSkillFiles[r.Plugin.ID],
		VersionID:          versionID,
		EditRevision:       revision,
	}
}

// SourceScope is SHA-256(JCS({owner_id,space_id,visibility})) for the root.
func SourceScope(d Dataset, rootID string) (string, error) {
	r, ok := findResource(d, rootID)
	if !ok {
		return "", ErrPlanStale
	}
	scope := struct {
		OwnerID    string                 `json:"owner_id"`
		SpaceID    *string                `json:"space_id"`
		Visibility model.PluginVisibility `json:"visibility"`
	}{OwnerID: r.Plugin.OwnerUID, SpaceID: r.Plugin.SpaceID, Visibility: normalizeVisibility(r.Plugin.Visibility)}
	if scope.OwnerID == "" {
		return "", fmt.Errorf("%w: root owner missing", ErrPlanInvalid)
	}
	return jcs.Digest(scope)
}

func normalizeVisibility(v model.PluginVisibility) model.PluginVisibility {
	return model.NormalizeLegacyVisibility(v)
}

func expectedForRoot(d Dataset, rootID string) (expectedGraph, Unit, error) {
	base := Build(d)
	var unit *Unit
	for i := range base.Units {
		if base.Units[i].RootID == rootID {
			unit = &base.Units[i]
			break
		}
	}
	if unit == nil {
		return expectedGraph{}, Unit{}, fmt.Errorf("%w: root has no migration unit", ErrPlanInvalid)
	}
	if len(unit.Nodes) == 0 {
		return expectedGraph{}, Unit{}, fmt.Errorf("%w: empty graph", ErrPlanInvalid)
	}
	g := expectedGraph{root: rootID}
	nodes := map[string]Node{}
	targetKeys := map[string]string{}
	for _, node := range unit.Nodes {
		// B-model references are by target ID. The legacy builder still carries
		// audit pins for embedded children; execution and plan relations omit them.
		for i := range node.Relations {
			node.Relations[i].TargetVersionID = nil
		}
		key := node.SourceID
		action := PlanActionCreate
		if node.SourceID == rootID {
			key = RootNodeKey
			action = PlanActionUpdate
		}
		origin := key
		if node.Type == model.PluginTypeConnector && strings.HasSuffix(node.SourceID, ":mcp") {
			originSource := strings.TrimSuffix(node.SourceID, ":mcp")
			if originSource == rootID {
				origin = RootNodeKey
			} else {
				origin = originSource
			}
		}
		nodes[key] = node
		en := expectedNode{key: key, originKey: origin, node: node, action: action}
		if source, ok := findResource(d, node.SourceID); ok {
			en.source = source
		} else if origin != key {
			if source, ok := findResource(d, strings.TrimSuffix(node.SourceID, ":mcp")); ok {
				en.source = source
			}
		}
		if node.SourceID == rootID {
			en.source, _ = findResource(d, rootID)
			g.rootResource = en.source
		}
		g.nodes = append(g.nodes, en)
		targetKeys[node.TargetID] = key
	}
	// Every relation target that is not created by this unit is an explicit
	// reuse target. Endpoints are therefore complete by construction.
	seen := map[string]bool{}
	for _, node := range unit.Nodes {
		for _, edge := range node.Relations {
			if targetKeys[edge.TargetPluginID] != "" {
				continue
			}
			if seen[edge.TargetPluginID] {
				continue
			}
			source, ok := findResource(d, edge.TargetPluginID)
			if !ok {
				return expectedGraph{}, Unit{}, fmt.Errorf("%w: relation target missing", ErrPlanInvalid)
			}
			key := edge.TargetPluginID
			if key == RootNodeKey {
				return expectedGraph{}, Unit{}, fmt.Errorf("%w: reuse key collides with root", ErrPlanInvalid)
			}
			if _, exists := nodes[key]; exists {
				return expectedGraph{}, Unit{}, fmt.Errorf("%w: reuse key collides with source node", ErrPlanInvalid)
			}
			seen[key] = true
			g.reuses = append(g.reuses, expectedNode{
				key:       key,
				originKey: key,
				node:      Node{Mapping: Mapping{SourceID: key, TargetID: edge.TargetPluginID, Type: source.Plugin.Type}, Plugin: source.Plugin},
				source:    source,
				action:    PlanActionReuse,
			})
		}
	}
	return g, *unit, nil
}

type expectedResource struct {
	SourceNodeKey       string
	OriginNodeKey       string
	PluginType          model.PluginType
	SourceContentDigest string
	Action              string
	OwnerID             string
	SpaceID             *string
	Visibility          model.PluginVisibility
	Node                Node
	Resource            Resource
	MCPServerKeys       []string
}

func expectedResources(d Dataset, g expectedGraph) ([]expectedResource, error) {
	out := []expectedResource{}
	for _, en := range append(append([]expectedNode{}, g.nodes...), g.reuses...) {
		digest, keys, err := sourceContentDigest(d, en)
		if err != nil {
			return nil, err
		}
		out = append(out, expectedResource{
			SourceNodeKey:       en.key,
			OriginNodeKey:       en.originKey,
			PluginType:          en.node.Type,
			SourceContentDigest: digest,
			Action:              en.action,
			OwnerID:             en.node.Plugin.OwnerUID,
			SpaceID:             en.node.Plugin.SpaceID,
			Visibility:          en.node.Plugin.Visibility,
			Node:                en.node,
			Resource:            en.source,
			MCPServerKeys:       keys,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceNodeKey < out[j].SourceNodeKey })
	return out, nil
}

func validateResource(d Dataset, want expectedResource, got MigrationPlanResource) (MigrationPlanResource, string, error) {
	if got.SourceNodeKey != want.SourceNodeKey || got.OriginNodeKey != want.OriginNodeKey ||
		got.PluginType != want.PluginType || got.Action != want.Action ||
		got.SourceContentDigest != want.SourceContentDigest ||
		got.OwnerID != want.OwnerID || !sameStringPointer(got.SpaceID, want.SpaceID) ||
		normalizeVisibility(got.Visibility) != normalizeVisibility(want.Visibility) {
		return MigrationPlanResource{}, "", fmt.Errorf("%w: resource %s binding", ErrPlanInvalid, want.SourceNodeKey)
	}
	if !equalStrings(got.MCPServerKeys, want.MCPServerKeys) {
		return MigrationPlanResource{}, "", fmt.Errorf("%w: connector server coverage %s", ErrPlanInvalid, want.SourceNodeKey)
	}
	got.MCPServerKeys = append([]string(nil), want.MCPServerKeys...)
	sort.Strings(got.MCPServerKeys)
	switch want.Action {
	case PlanActionUpdate:
		if got.TargetPluginID == nil || *got.TargetPluginID != want.Node.SourceID {
			return MigrationPlanResource{}, "", fmt.Errorf("%w: update_source target %s", ErrPlanInvalid, want.SourceNodeKey)
		}
		if got.VersionLabel == nil {
			return MigrationPlanResource{}, "", fmt.Errorf("%w: label required %s", ErrPlanInvalid, want.SourceNodeKey)
		}
		if err := validateNewLabel(d, want.Node.SourceID, *got.VersionLabel); err != nil {
			return MigrationPlanResource{}, "", err
		}
		got.TargetVersionID = nil
		got.CategoryID = normalizeCategory(got.CategoryID)
		return got, *got.VersionLabel, nil
	case PlanActionCreate:
		if got.TargetPluginID != nil || got.TargetVersionID != nil {
			return MigrationPlanResource{}, "", fmt.Errorf("%w: create target ids %s", ErrPlanInvalid, want.SourceNodeKey)
		}
		if want.PluginType == model.PluginTypeConnector {
			if got.VersionLabel != nil || len(got.CategoryID) > 0 {
				return MigrationPlanResource{}, "", fmt.Errorf("%w: connector version/category %s", ErrPlanInvalid, want.SourceNodeKey)
			}
		} else {
			if got.VersionLabel == nil {
				return MigrationPlanResource{}, "", fmt.Errorf("%w: label required %s", ErrPlanInvalid, want.SourceNodeKey)
			}
			if err := validateNewLabel(d, want.Node.TargetID, *got.VersionLabel); err != nil {
				return MigrationPlanResource{}, "", err
			}
			got.CategoryID = normalizeCategory(got.CategoryID)
		}
		got.TargetEffectiveRevision = nil
		got.TargetEffectiveDigest = nil
		if got.VersionLabel != nil {
			return got, *got.VersionLabel, nil
		}
		return got, "", nil
	case PlanActionReuse:
		if got.TargetPluginID == nil || *got.TargetPluginID != want.Node.TargetID {
			return MigrationPlanResource{}, "", fmt.Errorf("%w: reuse target %s", ErrPlanInvalid, want.SourceNodeKey)
		}
		if got.VersionLabel != nil || len(got.CategoryID) > 0 || got.TargetVersionID != nil {
			return MigrationPlanResource{}, "", fmt.Errorf("%w: reuse mutable fields %s", ErrPlanInvalid, want.SourceNodeKey)
		}
		if want.PluginType == model.PluginTypeConnector {
			effective, ok := d.Effective[want.Node.TargetID]
			if !ok {
				return MigrationPlanResource{}, "", fmt.Errorf("%w: reuse effective missing", ErrPlanInvalid)
			}
			if got.TargetEffectiveRevision == nil || *got.TargetEffectiveRevision != effective.Revision ||
				got.TargetEffectiveDigest == nil || *got.TargetEffectiveDigest != effective.Digest {
				return MigrationPlanResource{}, "", fmt.Errorf("%w: reuse effective drift", ErrPlanStale)
			}
		} else if got.TargetEffectiveRevision != nil || got.TargetEffectiveDigest != nil {
			return MigrationPlanResource{}, "", fmt.Errorf("%w: non-connector effective", ErrPlanInvalid)
		}
		return got, "", nil
	default:
		return MigrationPlanResource{}, "", fmt.Errorf("%w: action", ErrPlanInvalid)
	}
}

func validateRelations(expected []MigrationPlanRelation, got []MigrationPlanRelation, resources map[string]MigrationPlanResource) ([]MigrationPlanRelation, error) {
	type key struct {
		source, target, kind string
		order                int
	}
	want := map[key]string{}
	for _, r := range expected {
		data, err := canonicalData(r.Data)
		if err != nil {
			return nil, err
		}
		want[key{r.SourceNodeKey, r.TargetNodeKey, r.RelationType, r.SortOrder}] = data
	}
	out := make([]MigrationPlanRelation, 0, len(got))
	seen := map[key]bool{}
	for _, r := range got {
		k := key{r.SourceNodeKey, r.TargetNodeKey, r.RelationType, r.SortOrder}
		expectedData, ok := want[k]
		if !ok {
			return nil, fmt.Errorf("%w: unexpected relation", ErrPlanInvalid)
		}
		if seen[k] {
			return nil, fmt.Errorf("%w: duplicate relation", ErrPlanInvalid)
		}
		seen[k] = true
		data, err := canonicalData(r.Data)
		if err != nil || data != expectedData {
			return nil, fmt.Errorf("%w: relation data", ErrPlanInvalid)
		}
		if resources[r.SourceNodeKey].SourceNodeKey == "" || resources[r.TargetNodeKey].SourceNodeKey == "" {
			return nil, fmt.Errorf("%w: relation endpoint", ErrPlanInvalid)
		}
		out = append(out, MigrationPlanRelation{
			SourceNodeKey: r.SourceNodeKey,
			TargetNodeKey: r.TargetNodeKey,
			RelationType:  r.RelationType,
			SortOrder:     r.SortOrder,
			Data:          json.RawMessage(data),
		})
	}
	if len(seen) != len(want) {
		return nil, fmt.Errorf("%w: relation coverage", ErrPlanInvalid)
	}
	return out, nil
}

func relationsForGraph(g expectedGraph) []MigrationPlanRelation {
	targetKeys := map[string]string{}
	for _, en := range g.nodes {
		targetKeys[en.node.TargetID] = en.key
	}
	for _, en := range g.reuses {
		targetKeys[en.node.TargetID] = en.key
	}
	out := []MigrationPlanRelation{}
	for _, en := range g.nodes {
		for _, edge := range en.node.Relations {
			target, ok := targetKeys[edge.TargetPluginID]
			if !ok {
				continue
			}
			data, err := canonicalData(edge.Data)
			if err != nil {
				data = "null"
			}
			out = append(out, MigrationPlanRelation{
				SourceNodeKey: en.key,
				TargetNodeKey: target,
				RelationType:  edge.Type,
				SortOrder:     edge.SortOrder,
				Data:          json.RawMessage(data),
			})
		}
	}
	return out
}

func sortPlan(plan *MigrationPlan) {
	sort.Slice(plan.Resources, func(i, j int) bool { return plan.Resources[i].SourceNodeKey < plan.Resources[j].SourceNodeKey })
	sort.Slice(plan.Relations, func(i, j int) bool {
		a, b := plan.Relations[i], plan.Relations[j]
		if a.SourceNodeKey != b.SourceNodeKey {
			return a.SourceNodeKey < b.SourceNodeKey
		}
		if a.RelationType != b.RelationType {
			return a.RelationType < b.RelationType
		}
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return a.TargetNodeKey < b.TargetNodeKey
	})
	for i := range plan.Resources {
		sort.Strings(plan.Resources[i].MCPServerKeys)
	}
}

func sourceContentDigest(d Dataset, en expectedNode) (string, []string, error) {
	if en.node.Type == model.PluginTypeConnector {
		keys, content, err := connectorSource(en.node.Plugin)
		if err != nil {
			return "", nil, fmt.Errorf("%w: connector content %s", ErrPlanInvalid, en.key)
		}
		digest, err := jcs.Digest(map[string]any{"kind": "connector", "servers": keys, "content": content})
		if err != nil {
			return "", nil, err
		}
		return digest, keys, nil
	}
	relations := append([]model.PluginRelation(nil), en.source.Relations...)
	sort.Slice(relations, func(i, j int) bool { return relations[i].ID < relations[j].ID })
	projection := struct {
		Key                string                 `json:"key"`
		Plugin             model.Plugin           `json:"plugin"`
		PublishedVersionID *string                `json:"published_version_id,omitempty"`
		Relations          []model.PluginRelation `json:"relations"`
		Files              map[string]string      `json:"files,omitempty"`
	}{
		Key: en.key, Plugin: en.source.Plugin, PublishedVersionID: en.source.PublishedVersionID,
		Relations: relations, Files: d.ResolvedSkillFiles[en.source.Plugin.ID],
	}
	digest, err := jcs.Digest(projection)
	if err != nil {
		return "", nil, err
	}
	return digest, nil, nil
}

func connectorSource(p model.Plugin) ([]string, string, error) {
	var doc struct {
		Attachments []struct {
			Path    string `json:"path"`
			Content string `json:"raw_content"`
		} `json:"attachments"`
	}
	if json.Unmarshal(p.Package, &doc) != nil {
		return nil, "", ErrPlanInvalid
	}
	for _, a := range doc.Attachments {
		if a.Path != "mcp.json" {
			continue
		}
		var config struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal([]byte(a.Content), &config) != nil || len(config.Servers) == 0 {
			return nil, "", ErrPlanInvalid
		}
		keys := make([]string, 0, len(config.Servers))
		for key := range config.Servers {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return keys, a.Content, nil
	}
	return nil, "", ErrPlanInvalid
}

func validateNewLabel(d Dataset, pluginID, label string) error {
	if strings.TrimSpace(label) != label || label == "" {
		return fmt.Errorf("%w: version label", ErrPlanInvalid)
	}
	if _, ok := model.ParseVersion(label); !ok {
		return fmt.Errorf("%w: version label format", ErrPlanInvalid)
	}
	var current *string
	for _, r := range d.Resources {
		if r.Plugin.ID == pluginID {
			r := r
			current = r.Plugin.CurrentVersion
			break
		}
	}
	if current != nil && !model.VersionNotRegressed(*current, label) {
		return fmt.Errorf("%w: version label regressed", ErrPlanInvalid)
	}
	for _, release := range d.Releases {
		if release.PluginID == pluginID && release.VersionLabel == label {
			return fmt.Errorf("%w: version label already released", ErrPlanInvalid)
		}
	}
	return nil
}

func sourceIdentity(d Dataset, rootID string) (string, string, uint64, error) {
	r, ok := findResource(d, rootID)
	if !ok || r.Plugin.CurrentVersionID == nil || *r.Plugin.CurrentVersionID == "" {
		return "", "", 0, fmt.Errorf("%w: root current version missing", ErrPlanStale)
	}
	return rootID + "@" + *r.Plugin.CurrentVersionID, *r.Plugin.CurrentVersionID, r.Plugin.EditRevision, nil
}

func suggestedLabel(d Dataset, en expectedNode) string {
	node := en.node
	if node.Type == model.PluginTypeConnector {
		return ""
	}
	if en.action == PlanActionUpdate {
		if label := forwardLabel(node.Plugin, d); label != "" {
			return label
		}
	}
	return "1.0.0"
}

func findResource(d Dataset, id string) (Resource, bool) {
	for _, r := range d.Resources {
		if r.Plugin.ID == id {
			return r, true
		}
	}
	return Resource{}, false
}

func canonicalData(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "null", nil
	}
	canonical, err := jcs.CanonicalizeJSON(raw)
	if err != nil {
		return "", fmt.Errorf("%w: relation data", ErrPlanInvalid)
	}
	return string(canonical), nil
}

func nullableString(value *string) json.RawMessage {
	if value == nil {
		return json.RawMessage("null")
	}
	raw, _ := json.Marshal(*value)
	return raw
}

func normalizeCategory(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage("null")
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" || len(value) > 64 {
		return json.RawMessage("null")
	}
	out, _ := json.Marshal(value)
	return out
}

func stringPtr(value string) *string { return &value }

func sameStringPointer(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// PlanLabels is the exported form of the execution label map. The CLI uses it
// when resuming a run whose root content is already its own committed output.
func PlanLabels(plan MigrationPlan) map[string]string {
	return planLabels(plan)
}
