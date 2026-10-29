package migration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestBuildMixedSkillAndExtractedMCPRelationsStayOnExpert(t *testing.T) {
	skill := resource("skill-source", "mixed-skill", "", "skill instructions")
	expert := resource("expert-source", "mixed-expert", "", "unused")
	expert.Plugin.Type = model.PluginTypeExpert
	expert.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"mixed-expert","plugin_type":"expert","name":"mixed-expert","description":"test","labels":[],"examples":[]}`)
	expert.Files = []File{
		{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("expert instructions")},
		{Path: "mcp.json", MIME: "application/json", Data: []byte(`{"mcpServers":{"search":{"command":"search"},"fetch":{"command":"fetch"}}}`)},
	}
	expert.Plugin.Package = testPackage(expert.Files)
	expert.Relations = []model.PluginRelation{{
		ID: "existing-skill-edge", SourcePluginID: expert.Plugin.ID, SourcePluginType: model.PluginTypeExpert,
		TargetPluginID: skill.Plugin.ID, TargetPluginType: model.PluginTypeSkill,
		Type: "expert_skill", Status: 1,
	}}

	plan, err := Build(Input{SnapshotID: "mixed-dependencies", Resources: []Resource{expert, skill}}, Policy{})
	if err != nil || len(plan.Issues) != 0 {
		t.Fatalf("Build: %v, issues: %+v", err, plan.Issues)
	}
	if len(plan.Nodes) != 4 {
		t.Fatalf("nodes = %d, want expert, skill, and two connectors", len(plan.Nodes))
	}
	byID := make(map[string]Node)
	position := make(map[string]int)
	var parent, migratedSkill Node
	connectors := make(map[string]Node)
	for i, n := range plan.Nodes {
		byID[n.Plugin.ID], position[n.Plugin.ID] = n, i
		if n.Plugin.SpaceID != nil || n.Plugin.Visibility != model.PluginVisibilitySystem {
			t.Fatalf("node %s lost platform scope: %+v", n.Plugin.ID, n.Plugin)
		}
		switch n.Plugin.Type {
		case model.PluginTypeExpert:
			parent = n
		case model.PluginTypeSkill:
			migratedSkill = n
		case model.PluginTypeConnector:
			connectors[n.Plugin.Name] = n
			if len(n.Relations) != 0 {
				t.Fatalf("connector %s replayed parent relations: %+v", n.Plugin.Name, n.Relations)
			}
			if n.SourceID != expert.Plugin.ID || n.Source.ID != expert.Plugin.ID || n.Source.Type != model.PluginTypeExpert {
				t.Fatalf("connector %s lost parent source lineage: %+v", n.Plugin.Name, n)
			}
		}
	}
	if parent.SourceID != expert.Plugin.ID || migratedSkill.SourceID != skill.Plugin.ID || len(connectors) != 2 {
		t.Fatalf("unexpected node identities: expert=%s skill=%s connectors=%v", parent.SourceID, migratedSkill.SourceID, connectors)
	}
	for _, name := range []string{"search", "fetch"} {
		if connectors[name].Plugin.ID == "" {
			t.Fatalf("missing extracted connector %s", name)
		}
	}
	if len(parent.Relations) != 3 {
		t.Fatalf("expert relations = %+v, want one skill and two connectors", parent.Relations)
	}
	skills, connectorEdges := 0, make(map[string]bool)
	for _, edge := range parent.Relations {
		if edge.SourcePluginID != parent.Plugin.ID || edge.SourcePluginType != model.PluginTypeExpert || edge.Status != 1 {
			t.Fatalf("wrong parent edge source: %+v", edge)
		}
		target, ok := byID[edge.TargetPluginID]
		if !ok || position[edge.TargetPluginID] >= position[parent.Plugin.ID] {
			t.Fatalf("dependency %s must exist before expert", edge.TargetPluginID)
		}
		switch edge.Type {
		case "expert_skill":
			skills++
			if edge.TargetPluginID != migratedSkill.Plugin.ID || edge.TargetPluginType != model.PluginTypeSkill || edge.ID == "existing-skill-edge" || edge.TargetVersionID == nil || migratedSkill.VersionID == "" || *edge.TargetVersionID != migratedSkill.VersionID {
				t.Fatalf("skill relation not rewritten to migrated exact version: %+v, skill: %+v", edge, migratedSkill)
			}
		case "expert_connector":
			if target.Plugin.Type != model.PluginTypeConnector || edge.TargetPluginType != model.PluginTypeConnector || connectorEdges[edge.TargetPluginID] {
				t.Fatalf("invalid or duplicated connector edge: %+v", edge)
			}
			connectorEdges[edge.TargetPluginID] = true
		default:
			t.Fatalf("unexpected relation: %+v", edge)
		}
	}
	if skills != 1 || len(connectorEdges) != 2 {
		t.Fatalf("skill edges = %d, connector edges = %d", skills, len(connectorEdges))
	}
}

func TestBuildRemovesEmptyMCPFromAllCurrentRepresentations(t *testing.T) {
	for _, raw := range []string{`{}`, `{"mcpServers":{}}`} {
		in := legacyReferenceInput(false)
		expert := &in.Resources[1]
		expert.Files = append(expert.Files, File{Path: "mcp.json", MIME: "application/json", Data: []byte(raw)})
		expert.Plugin.Package = testPackage(expert.Files)
		p, err := Build(in, Policy{})
		if err != nil {
			t.Fatalf("Build: %v %+v", err, p.Issues)
		}
		for _, n := range p.Nodes {
			if n.Plugin.Type == model.PluginTypeConnector {
				t.Fatal("empty configuration created a connector")
			}
			if n.Plugin.Type != model.PluginTypeExpert {
				continue
			}
			for _, f := range n.Files {
				if f.Path == "mcp.json" {
					t.Fatal("empty MCP still in final files")
				}
			}
			for _, raw := range []json.RawMessage{n.Plugin.Package, n.Artifacts, n.Plugin.AttachmentKeys} {
				if strings.Contains(string(raw), `mcp.json`) {
					t.Fatalf("empty MCP still in final representation: %s", raw)
				}
			}
		}
	}
}

func TestBuildBlocksAmbiguousConnectorNamesOnlyWithinOneInstallation(t *testing.T) {
	for _, sharedTeam := range []bool{false, true} {
		in := legacyReferenceInput(true)
		expert := resource("second-expert", "second-expert", "", "unused")
		expert.Plugin.Type = model.PluginTypeExpert
		expert.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"second-expert","plugin_type":"expert","name":"second-expert","description":"test","labels":[],"examples":[]}`)
		expert.Files = []File{{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("second expert")}, {Path: "mcp.json", MIME: "application/json", Data: []byte(`{"mcpServers":{"search":{"command":"second-search"}}}`)}}
		expert.Plugin.Package = testPackage(expert.Files)
		in.Resources = append(in.Resources, expert)
		if sharedTeam {
			team := &in.Resources[0]
			team.Relations = append(team.Relations, model.PluginRelation{ID: "second-member", SourcePluginID: team.Plugin.ID, SourcePluginType: team.Plugin.Type, TargetPluginID: expert.Plugin.ID, TargetPluginType: expert.Plugin.Type, Type: "expert_team_expert", Status: 1, Data: json.RawMessage(`{"member_key":"second","role":"worker","is_leader":false}`)})
		}
		p, err := Build(in, Policy{})
		if !sharedTeam {
			if err != nil {
				t.Fatalf("unrelated installations incorrectly blocked: %v %+v", err, p.Issues)
			}
			continue
		}
		found := false
		for _, issue := range p.Issues {
			if issue.Code == "connector_name_ambiguous" {
				found = true
			}
		}
		if err != ErrIssues || !found {
			t.Fatalf("ambiguous team accepted: %v %+v", err, p.Issues)
		}
		// Resume/caller-built plans cannot bypass the same guard before writes.
		p.Issues = nil
		if _, err := Apply(context.Background(), nil, p, DatabaseIdentity{}); err != ErrIssues {
			t.Fatalf("ambiguous resumed graph reached database: %v", err)
		}
	}
}
