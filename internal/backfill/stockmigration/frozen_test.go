package stockmigration

import (
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func planFromPreview(t *testing.T, preview MigrationPreview) MigrationPlan {
	t.Helper()
	if preview.ProposedPlan == nil {
		t.Fatalf("preview exceptions: %+v", preview.Exceptions)
	}
	return *preview.ProposedPlan
}

func TestFreezeProducesCanonicalPlanAndDigestVector(t *testing.T) {
	d := fixture()
	preview, err := PreviewDiscovery(d, "team")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Phase != "discovery" || preview.CanApply || preview.Plan != nil {
		t.Fatalf("discovery must not approve: %+v", preview)
	}
	if len(preview.Resources) != 4 || len(preview.Exceptions) != 0 {
		t.Fatalf("inventory: resources=%d exceptions=%+v", len(preview.Resources), preview.Exceptions)
	}
	for _, resource := range preview.Resources {
		if len(resource.SuggestedTargets) != 0 {
			t.Fatalf("discovery suggested a target as approval: %+v", resource)
		}
	}
	proposal := planFromPreview(t, preview)
	frozen, digest, err := Freeze(d, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 || digest != mustDigest(t, frozen) {
		t.Fatalf("digest: %s", digest)
	}
	if frozen.SourceIdentity != "team@old-team" || frozen.SourcePluginID != "team" || frozen.SourceVersionID != "old-team" {
		t.Fatalf("source binding: %+v", frozen)
	}
	if frozen.ExpectedEditRevision == nil || *frozen.ExpectedEditRevision != 1 {
		t.Fatalf("edit revision: %+v", frozen.ExpectedEditRevision)
	}
	if !sort.SliceIsSorted(frozen.Resources, func(i, j int) bool { return frozen.Resources[i].SourceNodeKey < frozen.Resources[j].SourceNodeKey }) {
		t.Fatal("resources are not sorted by source_node_key")
	}
	if !sort.SliceIsSorted(frozen.Relations, func(i, j int) bool {
		a, b := frozen.Relations[i], frozen.Relations[j]
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
	}) {
		t.Fatal("relations are not in the contract order")
	}
	for _, resource := range frozen.Resources {
		if resource.PluginType == model.PluginTypeConnector {
			if len(resource.MCPServerKeys) != 1 || resource.MCPServerKeys[0] != "search" {
				t.Fatalf("connector server keys: %+v", resource)
			}
		}
	}
	// The digest must be recomputable from the serialized frozen plan alone.
	raw, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	var decoded MigrationPlan
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	again, err := PlanDigest(decoded)
	if err != nil || again != digest {
		t.Fatalf("round trip digest: %s err=%v", again, err)
	}
	// A hand edit changes the digest and therefore cannot reuse the approval.
	edited := decoded
	edited.Resources = append([]MigrationPlanResource(nil), decoded.Resources...)
	label := "9.9.9"
	for i := range edited.Resources {
		if edited.Resources[i].VersionLabel != nil {
			edited.Resources[i].VersionLabel = &label
		}
	}
	if editedDigest, _ := PlanDigest(edited); editedDigest == digest {
		t.Fatal("hand-edited plan kept the same digest")
	}
}

func TestFreezeRejectsChangedOrAmbiguousProposedPlan(t *testing.T) {
	d := fixture()
	preview, err := PreviewDiscovery(d, "team")
	if err != nil {
		t.Fatal(err)
	}
	base := planFromPreview(t, preview)

	for name, mutate := range map[string]func(*MigrationPlan){
		"missing-resource": func(p *MigrationPlan) { p.Resources = p.Resources[:len(p.Resources)-1] },
		"extra-resource":   func(p *MigrationPlan) { p.Resources = append(p.Resources, p.Resources[0]) },
		"wrong-action":     func(p *MigrationPlan) { p.Resources[0].Action = PlanActionReuse },
		"wrong-owner":      func(p *MigrationPlan) { p.Resources[0].OwnerID = "other" },
		"wrong-digest":     func(p *MigrationPlan) { p.Resources[0].SourceContentDigest = "00" },
		"missing-relation": func(p *MigrationPlan) { p.Relations = p.Relations[:len(p.Relations)-1] },
		"wrong-relation":   func(p *MigrationPlan) { p.Relations[0].TargetNodeKey = "other" },
		"mcp-keys": func(p *MigrationPlan) {
			for i := range p.Resources {
				if p.Resources[i].PluginType == model.PluginTypeConnector {
					p.Resources[i].MCPServerKeys = []string{"other"}
				}
			}
		},
		"label-format": func(p *MigrationPlan) {
			bad := "not-a-version"
			for i := range p.Resources {
				if p.Resources[i].VersionLabel != nil {
					p.Resources[i].VersionLabel = &bad
				}
			}
		},
		"target-on-create": func(p *MigrationPlan) {
			target := "x"
			for i := range p.Resources {
				if p.Resources[i].Action == PlanActionCreate {
					p.Resources[i].TargetPluginID = &target
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			proposal := base
			proposal.Resources = append([]MigrationPlanResource(nil), base.Resources...)
			proposal.Relations = append([]MigrationPlanRelation(nil), base.Relations...)
			mutate(&proposal)
			if _, _, err := Freeze(d, proposal); !errors.Is(err, ErrPlanInvalid) {
				t.Fatalf("accepted invalid plan: %v", err)
			}
		})
	}
}

func TestFreezeRejectsStaleSourceIdentityAndRevision(t *testing.T) {
	d := fixture()
	preview, err := PreviewDiscovery(d, "team")
	if err != nil {
		t.Fatal(err)
	}
	base := planFromPreview(t, preview)
	if _, _, err = Freeze(d, base); err != nil {
		t.Fatal(err)
	}
	base.ExpectedEditRevision = uint64Ptr(2)
	if _, _, err = Freeze(d, base); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	base.ExpectedEditRevision = uint64Ptr(1)
	changed := d
	changed.Resources = append([]Resource(nil), d.Resources...)
	changed.Resources[0].Plugin.CurrentVersionID = stringPtr("other")
	if _, _, err = Freeze(changed, base); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("stale version accepted: %v", err)
	}
}

func TestInputDigestBindsResolvedFilesAndScope(t *testing.T) {
	d := fixture()
	first, err := InputDigest(d, "team")
	if err != nil {
		t.Fatal(err)
	}
	d.ResolvedSkillFiles = map[string]map[string]string{"skill": {"SKILL.md": "new verified bytes"}}
	second, err := InputDigest(d, "team")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("file bytes not bound to input_digest")
	}
	scopeA, err := SourceScope(d, "team")
	if err != nil {
		t.Fatal(err)
	}
	d.Resources[0].Plugin.Visibility = "space"
	scopeB, err := SourceScope(d, "team")
	if err != nil {
		t.Fatal(err)
	}
	if scopeA == scopeB {
		t.Fatal("scope digest ignored visibility")
	}
}

func TestConnectorReuseEffectiveDriftIsPlanStale(t *testing.T) {
	d := reuseConnectorFixture()
	preview, err := PreviewDiscovery(d, "root")
	if err != nil {
		t.Fatal(err)
	}
	proposal := planFromPreview(t, preview)
	_, digest, err := Freeze(d, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Resources) != 3 {
		t.Fatalf("reuse inventory: %+v", preview.Resources)
	}
	drifted := d
	drifted.Effective = map[string]ConnectorEffective{"conn": {Revision: 8, Digest: "sha256:changed"}}
	if _, next, err := Freeze(drifted, proposal); !errors.Is(err, ErrPlanStale) || next == digest {
		t.Fatalf("reuse drift accepted: digest=%s err=%v", next, err)
	}
}

func reuseConnectorFixture() Dataset {
	space := "space-a"
	manifest := func(kind, name string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-manifest-2.0.json", "plugin_name": name, "plugin_type": kind, "name": name, "description": "fixture", "labels": []any{}, "examples": []any{}})
		return raw
	}
	pkg := func(files map[string]string) json.RawMessage {
		atts := []any{}
		for path, content := range files {
			atts = append(atts, map[string]any{"path": path, "content_type": "raw", "mime_type": "text/plain", "raw_content": content})
		}
		raw, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-package-2.0.json", "attachments": atts})
		return raw
	}
	root := model.Plugin{ID: "root", Name: "root", Type: model.PluginTypeExpert, OwnerUID: "owner", SpaceID: &space, Visibility: "private", ListingState: "published", Status: 1, Tags: json.RawMessage(`[]`), CurrentVersion: stringPtr("1.0.0"), CurrentVersionID: stringPtr("old-root"), EditRevision: 1}
	root.Manifest = manifest("expert", "root")
	root.Package = pkg(map[string]string{"AGENTS.md": "root instructions"})
	conn := model.Plugin{ID: "conn", Name: "conn", Type: model.PluginTypeConnector, OwnerUID: "owner", SpaceID: &space, Visibility: "private", ListingState: "published", Status: 1, Tags: json.RawMessage(`[]`)}
	conn.Manifest = manifest("connector", "conn")
	conn.Package = pkg(map[string]string{"mcp.json": `{"mcpServers":{"search":{"command":"search"}}}`})
	skill := model.Plugin{ID: "skill", Name: "skill", Type: model.PluginTypeSkill, IsEmbedded: true, OwnerUID: "owner", SpaceID: &space, Visibility: "private", ListingState: "published", Status: 1, Tags: json.RawMessage(`[]`)}
	skill.Manifest = manifest("skill", "skill")
	skill.Package = pkg(map[string]string{"SKILL.md": "---\nname: skill\ndescription: fixture\n---\nbody"})
	rootRel := model.PluginRelation{ID: "root-conn", SourcePluginID: "root", TargetPluginID: "conn", Type: "expert_connector", Status: 1}
	skillRel := model.PluginRelation{ID: "root-skill", SourcePluginID: "root", TargetPluginID: "skill", Type: "expert_skill", Status: 1, SortOrder: 1}
	return Dataset{
		Resources: []Resource{{Plugin: root, Relations: []model.PluginRelation{rootRel, skillRel}}, {Plugin: conn}, {Plugin: skill}},
		Versions:  []model.PluginVersion{{ID: "old-root", PluginID: "root"}},
		Effective: map[string]ConnectorEffective{"conn": {Revision: 7, Digest: "sha256:effective"}},
	}
}

func mustDigest(t *testing.T, plan MigrationPlan) string {
	t.Helper()
	digest, err := PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func uint64Ptr(v uint64) *uint64 { return &v }
