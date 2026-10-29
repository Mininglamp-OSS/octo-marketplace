package migration

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func legacyReferenceInput(withMCP bool) Input {
	skill := resource("legacy-skill", "legacy-skill", "", "skill body")
	expert := resource("legacy-expert", "legacy-expert", "", "unused")
	expert.Plugin.Type = model.PluginTypeExpert
	expert.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"legacy-expert","plugin_type":"expert","name":"legacy-expert","description":"test","labels":[],"examples":[]}`)
	expert.Files = []File{{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("member instructions")}, {Path: "expert/context.json", MIME: "application/json", Data: []byte(`{"member_key":"member","role":"researcher","is_leader":true,"template_id":"original"}`)}}
	if withMCP {
		expert.Files = append(expert.Files, File{Path: "mcp.json", MIME: "application/json", Data: []byte(`{"mcpServers":{"search":{"command":"search"}}}`)})
	}
	expert.Plugin.Package = testPackage(expert.Files)
	expert.Relations = []model.PluginRelation{{ID: "skill-edge", SourcePluginID: expert.Plugin.ID, SourcePluginType: expert.Plugin.Type, TargetPluginID: skill.Plugin.ID, TargetPluginType: skill.Plugin.Type, Type: "expert_skill", SortOrder: 3, Data: json.RawMessage(`{"source_index":3}`), Status: 1}}
	team := resource("legacy-team", "legacy-team", "", "unused")
	team.Plugin.Type = model.PluginTypeExpertTeam
	team.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"legacy-team","plugin_type":"expert_team","name":"legacy-team","description":"test","labels":[],"examples":[]}`)
	team.Files = []File{{Path: "AGENTS.md", MIME: "text/markdown", Data: []byte("team instructions")}}
	team.Plugin.Package = testPackage(team.Files)
	team.Relations = []model.PluginRelation{{ID: "member-edge", SourcePluginID: team.Plugin.ID, SourcePluginType: team.Plugin.Type, TargetPluginID: expert.Plugin.ID, TargetPluginType: expert.Plugin.Type, Type: "expert_team_expert", SortOrder: 7, Data: json.RawMessage(`{"source_index":7,"member_key":"member","role":"researcher","is_leader":true}`), Status: 1}}
	return Input{SnapshotID: "legacy-references", Resources: []Resource{team, expert, skill}}
}

func TestLegacyRelationMetadataMeetsReferenceWriteContract(t *testing.T) {
	p, err := Build(legacyReferenceInput(true), Policy{})
	if err != nil {
		t.Fatalf("Build: %v %+v", err, p.Issues)
	}
	for _, n := range p.Nodes {
		if err := pluginrepo.ValidateReferenceMetadata(n.Plugin.Type, n.Relations); err != nil {
			t.Errorf("%s cannot be edited/installed under reference rules: %v", n.SourceID, err)
		}
		if n.Plugin.Type == model.PluginTypeExpertTeam {
			e := n.Relations[0]
			if e.SortOrder != 7 || !sameJSON(e.Data, json.RawMessage(`{"member_key":"member","role":"researcher","is_leader":true}`)) {
				t.Errorf("member order/role/leadership changed: %+v", e)
			}
		}
	}
}

func TestLegacyExpertContextIsArchivedWithoutReservedAttachment(t *testing.T) {
	for _, withMCP := range []bool{false, true} {
		in := legacyReferenceInput(withMCP)
		original := in.Resources[1].Files[1]
		p, err := Build(in, Policy{})
		if err != nil {
			t.Fatalf("Build: %v %+v", err, p.Issues)
		}
		found := false
		for _, n := range p.Nodes {
			if n.Plugin.Type != model.PluginTypeExpert {
				continue
			}
			for _, f := range n.Files {
				if f.Path == "expert/context.json" {
					t.Error("reserved legacy context still blocks pinned installation")
				}
				if f.Path == "migration/legacy/expert/context.json" && bytes.Equal(f.Data, original.Data) {
					found = true
				}
			}
		}
		if !found || in.Resources[1].Files[1].Path != original.Path {
			t.Fatal("context bytes lost or input mutated")
		}
	}
}

func TestLegacyReferenceConversionRejectsUnknownMetadataAndArchiveCollision(t *testing.T) {
	for _, kind := range []string{"unknown metadata", "duplicate metadata", "archive collision"} {
		t.Run(kind, func(t *testing.T) {
			in := legacyReferenceInput(false)
			switch kind {
			case "unknown metadata":
				in.Resources[0].Relations[0].Data = json.RawMessage(`{"member_key":"member","role":"researcher","is_leader":true,"custom":"must not disappear"}`)
			case "duplicate metadata":
				in.Resources[0].Relations[0].Data = json.RawMessage(`{"member_key":"member","is_leader":true,"source_index":7,"source_index":8}`)
			case "archive collision":
				in.Resources[1].Files = append(in.Resources[1].Files, File{Path: "migration/legacy/expert/context.json", MIME: "application/json", Data: []byte(`{"other":"must not be overwritten"}`)})
				in.Resources[1].Plugin.Package = testPackage(in.Resources[1].Files)
			}
			if _, err := Build(in, Policy{}); err == nil {
				t.Fatal("unsafe conversion accepted")
			}
		})
	}
}
