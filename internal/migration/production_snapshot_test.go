package migration

import (
	"bytes"
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"testing"
)

func snapshotExpert(id string, typ model.PluginType, state model.PluginListingState) Resource {
	r := resource(id, id, "space", "unused")
	r.Plugin.Type = typ
	r.Plugin.ListingState = state
	r.Plugin.OwnerUID = "owner"
	r.Plugin.Manifest, _ = json.Marshal(map[string]any{"$schema": "cowork-plugin-manifest-2.0.json", "plugin_name": id, "plugin_type": typ, "name": id, "description": "test", "labels": []string{}, "examples": []string{}})
	r.Files = []File{{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("Original instructions")}}
	r.Plugin.Package = testPackage(r.Files)
	return r
}
func TestProductionMIMEParametersDoNotInvalidateFiles(t *testing.T) {
	r := resource("mime", "mime", "", "Original skill instructions")
	r.Files = append(r.Files, File{Path: "index.html", MIME: "text/html; charset=utf-8", Data: []byte("<p>Keep these exact bytes</p>")})
	// The source package was valid; the parameter comes from archive MIME inference.
	r.Plugin.Package = testPackage(r.Files[:1])
	p, err := Build(Input{SnapshotID: "mime", Resources: []Resource{r}}, Policy{})
	if err != nil {
		t.Fatalf("MIME conversion blocked: %v", p.Issues)
	}
	for _, f := range p.Nodes[0].Files {
		if f.Path == "index.html" && (f.MIME != "text/html" || !bytes.Equal(f.Data, r.Files[1].Data)) {
			t.Fatal("incorrect normalization")
		}
	}
}
func TestProductionMCPUsesOneIdentityPerExactConfigurationAndScope(t *testing.T) {
	a := snapshotExpert("a", model.PluginTypeExpert, model.PluginListingStatePublished)
	b := snapshotExpert("b", model.PluginTypeExpert, model.PluginListingStatePublished)
	mcp := File{Path: "mcp.json", MIME: "application/json", Data: []byte(`{"mcpServers":{"search":{"url":"https://example.invalid/mcp","headers":{"Authorization":"fixture"}}},"extension":{"keep":true}}`)}
	for _, r := range []*Resource{&a, &b} {
		r.Files = append(r.Files, mcp)
		r.Plugin.Package = testPackage(r.Files)
	}
	p, err := Build(Input{SnapshotID: "same-mcp", Resources: []Resource{b, a}}, Policy{})
	if err != nil {
		t.Fatal(p.Issues)
	}
	connectors := 0
	targets := map[string]bool{}
	for _, n := range p.Nodes {
		if n.Plugin.Type == model.PluginTypeConnector {
			connectors++
			if len(n.Evidence) != 2 {
				t.Fatal("merged source evidence missing")
			}
		}
		for _, e := range n.Relations {
			targets[e.TargetPluginID] = true
		}
	}
	if connectors != 1 || len(targets) != 1 {
		t.Fatalf("connectors=%d distinct references=%d", connectors, len(targets))
	}
	other := "other-space"
	b.Plugin.SpaceID = &other
	p, err = Build(Input{SnapshotID: "different-scope", Resources: []Resource{a, b}}, Policy{})
	if err != nil {
		t.Fatal(p.Issues)
	}
	connectors = 0
	for _, n := range p.Nodes {
		if n.Plugin.Type == model.PluginTypeConnector {
			connectors++
		}
	}
	if connectors != 2 {
		t.Fatal("cross-space MCP merged")
	}
}
func TestBodyDescriptionRequiresExplicitCatalogMetadata(t *testing.T) {
	for _, body := range []string{
		"# Fixture\n\nOriginal usage paragraph.",
		"# Fixture\n\nInternal deployment note: rotate the staging credential after the Q4 cutover; owner Alice",
		"---\nname: fixture\ndescription: broken: YAML\n---\nSafe looking prose with hidden business context.",
	} {
		r := skillWithDescription(body, "")
		p, err := Build(Input{SnapshotID: "explicit-description", Resources: []Resource{r}}, Policy{})
		if err == nil {
			t.Fatal("body silently promoted to catalog")
		}
		for _, n := range p.Nodes {
			if descriptionOf(t, n.Plugin.Manifest) != "" || !bytes.Equal(n.Files[0].Data, []byte(body)) {
				t.Fatal("body promoted or rewritten")
			}
		}
		approved := CatalogDescription{SourcePackageHash: sourceDescriptionHash(r.Plugin), Description: "Operator-approved catalog description."}
		policy := Policy{CatalogDescriptions: map[string]CatalogDescription{r.Plugin.ID: approved}}
		p, err = Build(Input{SnapshotID: "explicit-description", Resources: []Resource{r}}, policy)
		if err != nil || len(p.Nodes) != 1 {
			t.Fatal(err, p.Issues)
		}
		if descriptionOf(t, p.Nodes[0].Plugin.Manifest) != approved.Description || !bytes.Equal(p.Nodes[0].Files[0].Data, []byte(body)) {
			t.Fatal("explicit metadata lost or bytes rewritten")
		}
		if issues := validateFinalContracts(p); len(issues) != 0 {
			t.Fatal(issues)
		}
		beforeNode, beforeRun := nodeHash(p.Nodes[0]), applyIdentity(p)
		p.Nodes[0].CatalogDescription.Description = "Changed after approval"
		if nodeHash(p.Nodes[0]) == beforeNode || applyIdentity(p) == beforeRun {
			t.Fatal("approval omitted from identity")
		}
		if issues := validateFinalContracts(p); len(issues) == 0 {
			t.Fatal("saved projection mismatch accepted")
		}
		approved.SourcePackageHash = "sha256:wrong-source"
		policy.CatalogDescriptions[r.Plugin.ID] = approved
		if _, err = Build(Input{SnapshotID: "explicit-description", Resources: []Resource{r}}, policy); err == nil {
			t.Fatal("stale approval accepted")
		}
	}
}

func TestProductionDraftsAreIndependentWithoutDependencies(t *testing.T) {
	team := snapshotExpert("draft-team", model.PluginTypeExpertTeam, model.PluginListingStateDraft)
	expert := snapshotExpert("draft-expert", model.PluginTypeExpert, model.PluginListingStateDraft)
	expert.Plugin.IsEmbedded = true
	skill := resource("draft-skill", "draft-skill", "space", "Complete original skill instructions")
	skill.Plugin.ListingState = model.PluginListingStateDraft
	skill.Plugin.OwnerUID = "owner"
	skill.Plugin.IsEmbedded = true
	team.Relations = []model.PluginRelation{{ID: "member", SourcePluginID: team.Plugin.ID, SourcePluginType: team.Plugin.Type, TargetPluginID: expert.Plugin.ID, TargetPluginType: expert.Plugin.Type, Type: "expert_team_expert", Status: 1, Data: json.RawMessage(`{"member_key":"worker","role":"member","is_leader":false}`)}}
	expert.Relations = []model.PluginRelation{{ID: "skill", SourcePluginID: expert.Plugin.ID, SourcePluginType: expert.Plugin.Type, TargetPluginID: skill.Plugin.ID, TargetPluginType: skill.Plugin.Type, Type: "expert_skill", Status: 1}}
	in := Input{SnapshotID: "draft-graph", Resources: []Resource{team, expert, skill}}
	p, err := Build(in, Policy{})
	if err != nil {
		t.Fatalf("draft conversion blocked: %v", p.Issues)
	}

	for _, n := range p.Nodes {

		if n.Release || n.Plugin.IsEmbedded || n.Plugin.ListingState != model.PluginListingStateDraft || len(n.Relations) != 0 {
			t.Fatal("draft published or formal reference created")
		}
	}
	for _, n := range p.Nodes {
		for _, f := range n.Files {
			if f.Path == "migration/pending-relations.json" {
				t.Fatal("invented pending relationship file")
			}
		}
	}
	team.Plugin.ListingState = model.PluginListingStatePublished
	in.Resources[0] = team
	if p, err = Build(in, Policy{}); err == nil {
		t.Fatalf("published root references draft: %v", p.Issues)
	}
}

func TestIndependentDraftIgnoresDiscardedLegacyEdges(t *testing.T) {
	for _, kind := range []string{"missing target", "stale pin", "invalid member metadata", "different space"} {
		t.Run(kind, func(t *testing.T) {
			team := snapshotExpert("draft-team", model.PluginTypeExpertTeam, model.PluginListingStateDraft)
			expert := snapshotExpert("draft-member", model.PluginTypeExpert, model.PluginListingStateDraft)
			expert.Plugin.IsEmbedded = true
			edge := model.PluginRelation{ID: "legacy", SourcePluginID: team.Plugin.ID, SourcePluginType: team.Plugin.Type, TargetPluginID: expert.Plugin.ID, TargetPluginType: expert.Plugin.Type, Type: "expert_team_expert", Status: 1, Data: json.RawMessage(`{"member_key":"worker","role":"member","is_leader":false}`)}
			switch kind {
			case "missing target":
				edge.TargetPluginID = "deleted-child"
			case "stale pin":
				missing := "missing-version"
				edge.TargetVersionID = &missing
			case "invalid member metadata":
				edge.Data = json.RawMessage(`{"role":"leader","is_leader":false}`)
			case "different space":
				other := "other-space"
				expert.Plugin.SpaceID = &other
			}
			team.Relations = []model.PluginRelation{edge}
			in := Input{SnapshotID: "stale-draft", Resources: []Resource{team, expert}}
			plan, err := Build(in, Policy{})
			if err != nil || len(plan.Nodes) != 2 {
				t.Fatalf("independent draft blocked: %v issues=%v", err, plan.Issues)
			}
			for _, n := range plan.Nodes {
				if n.Release || len(n.Relations) != 0 || n.Plugin.IsEmbedded || n.Plugin.ListingState != model.PluginListingStateDraft {
					t.Fatal("discarded edge persisted or draft was published")
				}
			}
			team.Plugin.ListingState = model.PluginListingStatePublished
			in.Resources[0] = team
			if _, err := Build(in, Policy{}); err == nil {
				t.Fatal("published graph accepted an invalid dependency")
			}
		})
	}
}

func TestCatalogDescriptionPolicyBindsCanonicalPlanID(t *testing.T) {
	a := skillWithDescription("# Fixture\n\nBody stays private.", "")
	b := resource("other-skill", "other-skill", "", "# Other\n\nOther body stays private.")
	in := Input{SnapshotID: "catalog-policy-identity", Resources: []Resource{a, b}}
	first := CatalogDescription{SourcePackageHash: sourceDescriptionHash(a.Plugin), Description: "First approved description."}
	second := CatalogDescription{SourcePackageHash: sourceDescriptionHash(b.Plugin), Description: "Second approved description."}
	build := func(entries map[string]CatalogDescription) Plan {
		t.Helper()
		p, e := Build(in, Policy{CatalogDescriptions: entries})
		if e != nil {
			t.Fatal(e, p.Issues)
		}
		return p
	}
	p := build(map[string]CatalogDescription{a.Plugin.ID: first, b.Plugin.ID: second})
	reverse := map[string]CatalogDescription{}
	reverse[b.Plugin.ID] = second
	reverse[a.Plugin.ID] = first
	if q := build(reverse); q.ID != p.ID {
		t.Fatal("map insertion order changed canonical plan ID")
	}
	first.Description = "Different approved description."
	if q := build(map[string]CatalogDescription{a.Plugin.ID: first, b.Plugin.ID: second}); q.ID == p.ID {
		t.Fatal("distinct catalog decisions share a plan ID")
	}
	// Hash changes must also bind even when an entry does not supply a fallback.
	in.Resources[0] = skillWithDescription("---\nname: x\ndescription: Original metadata\n---\nBody", "")
	first.SourcePackageHash = sourceDescriptionHash(in.Resources[0].Plugin)
	p = build(map[string]CatalogDescription{a.Plugin.ID: first, b.Plugin.ID: second})
	first.SourcePackageHash = "sha256:different"
	if q := build(map[string]CatalogDescription{a.Plugin.ID: first, b.Plugin.ID: second}); q.ID == p.ID {
		t.Fatal("policy source hash omitted from plan identity")
	}
	emptyInput := Input{SnapshotID: "empty-catalog-policy", Resources: []Resource{in.Resources[0]}}
	nilPlan, e := Build(emptyInput, Policy{})
	if e != nil {
		t.Fatal(e)
	}
	emptyPlan, e := Build(emptyInput, Policy{CatalogDescriptions: map[string]CatalogDescription{}})
	if e != nil || nilPlan.ID != emptyPlan.ID {
		t.Fatal("nil and empty catalog policies differ", e)
	}
}
