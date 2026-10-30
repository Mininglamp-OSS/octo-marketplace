package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func connectorFixture(id, script string) Resource {
	r := snapshotExpert(id, model.PluginTypeConnector, model.PluginListingStatePublished)
	r.Plugin.Name = "search"
	r.Plugin.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"search","plugin_type":"connector","name":"search","description":"test","labels":[],"examples":[]}`)
	r.Files = []File{{Path: "mcp.json", MIME: "application/json", Data: []byte(`{"mcpServers":{"search":{"command":"python","args":["scripts/client.py"]}}}`)}, {Path: "scripts/client.py", MIME: "text/plain", Data: []byte(script)}}
	r.Plugin.Package = connectorPackage(r.Files)
	return r
}

func connectorPackage(files []File) json.RawMessage {
	var pkg map[string]json.RawMessage
	json.Unmarshal(testPackage(files), &pkg)
	pkg["connector"] = json.RawMessage(`{"type":"mcp","source":"migration"}`)
	raw, _ := json.Marshal(pkg)
	return raw
}

func TestConnectorCanonicalizationPreservesCompleteResource(t *testing.T) {
	for _, difference := range []string{"bytes", "path", "mime", "descriptor", "manifest", "owner", "scope", "lifecycle"} {
		t.Run(difference, func(t *testing.T) {
			a, b := connectorFixture("first", "first implementation"), connectorFixture("second", "first implementation")
			switch difference {
			case "bytes":
				b.Files[1].Data = []byte("second implementation")
			case "path":
				b.Files[1].Path = "scripts/other.py"
			case "mime":
				b.Files[1].MIME = "text/x-python"
			case "descriptor":
				b.Plugin.Package = bytes.Replace(b.Plugin.Package, []byte(`"source":"migration"`), []byte(`"source":"different"`), 1)
			case "manifest":
				b.Plugin.Manifest = bytes.Replace(b.Plugin.Manifest, []byte(`"description":"test"`), []byte(`"description":"different"`), 1)
			case "owner":
				b.Plugin.OwnerUID = "different-owner"
			case "scope":
				other := "other-space"
				b.Plugin.SpaceID = &other
			case "lifecycle":
				b.Plugin.ListingState = model.PluginListingStateDraft
			}
			if difference != "descriptor" {
				b.Plugin.Package = connectorPackage(b.Files)
			}
			in := Input{SnapshotID: "connector-tree", Resources: []Resource{b, a}}
			plan, err := Build(in, Policy{})
			if err != nil || len(plan.Nodes) != 2 {
				t.Fatalf("distinct connector lost: err=%v nodes=%d issues=%v", err, len(plan.Nodes), plan.Issues)
			}
			for _, n := range plan.Nodes {
				original := a
				if n.SourceID == b.Plugin.ID {
					original = b
				}
				if len(n.Evidence) != 1 || len(n.Files) != len(original.Files) {
					t.Fatal("distinct source was merged")
				}
				for i, f := range n.Files {
					if f.Path != original.Files[i].Path || !bytes.Equal(f.Data, original.Files[i].Data) {
						t.Fatal("supporting file changed or removed")
					}
				}
			}
			if plan.Mappings[0].TargetID == plan.Mappings[1].TargetID {
				t.Fatal("distinct sources remapped to the same connector")
			}
		})
	}
}

func TestConnectorCanonicalizationReusesEquivalentTrees(t *testing.T) {
	a, b := connectorFixture("first", "same implementation"), connectorFixture("second", "same implementation")
	b.Files[0].Data = []byte(`{ "mcpServers": { "search": { "args": ["scripts/client.py"], "command": "python" } } }`)
	b.Plugin.Package = connectorPackage(b.Files)
	b.Files[1].MIME += "; charset=utf-8"
	p, err := Build(Input{SnapshotID: "connector-equivalent", Resources: []Resource{b, a}}, Policy{})
	if err != nil || len(p.Nodes) != 1 || len(p.Nodes[0].Evidence) != 2 || p.Mappings[0].TargetID != p.Mappings[1].TargetID {
		t.Fatalf("equivalent connectors not reused: err=%v nodes=%d issues=%v", err, len(p.Nodes), p.Issues)
	}
}

func TestConnectorNamesKeepSourceSuffixAndStableReferences(t *testing.T) {
	in := legacyReferenceInput(true)
	other := snapshotExpert("other-expert", model.PluginTypeExpert, model.PluginListingStatePublished)
	other.Plugin.Visibility, other.Plugin.SpaceID = model.PluginVisibilitySystem, nil
	other.Files = append(other.Files, File{Path: "mcp.json", MIME: "application/json", Data: []byte(`{"mcpServers":{"search":{"command":"other-search"}}}`)})
	other.Plugin.Package = testPackage(other.Files)
	in.Resources = append(in.Resources, other)
	in.Resources[0].Relations = append(in.Resources[0].Relations, model.PluginRelation{ID: "other-member", SourcePluginID: in.Resources[0].Plugin.ID, SourcePluginType: model.PluginTypeExpertTeam, TargetPluginID: other.Plugin.ID, TargetPluginType: model.PluginTypeExpert, Type: "expert_team_expert", Status: 1, Data: json.RawMessage(`{"member_key":"other","role":"worker","is_leader":false}`)})
	p, err := Build(in, Policy{})
	if err != nil {
		t.Fatalf("source suffix did not resolve collision: %v %v", err, p.Issues)
	}
	names := map[string]string{}
	for _, n := range p.Nodes {
		if n.Plugin.Type != model.PluginTypeConnector {
			continue
		}
		if n.Plugin.Name != "search-"+n.Source.Name {
			t.Fatalf("source suffix missing: %q", n.Plugin.Name)
		}
		var m map[string]any
		json.Unmarshal(n.Plugin.Manifest, &m)
		if m["name"] != n.Plugin.Name || m["plugin_name"] != n.Plugin.Name {
			t.Fatal("manifest name is stale")
		}
		if !strings.Contains(string(n.Files[0].Data), `"search"`) || strings.Contains(string(n.Files[0].Data), n.Plugin.Name) {
			t.Fatal("original MCP server key changed")
		}
		names[n.Plugin.ID] = n.Plugin.Name
	}
	if len(names) != 2 || len(connectorNameIssues(p)) != 0 {
		t.Fatal("connector collision remains")
	}
	for _, n := range p.Nodes {
		for _, edge := range n.Relations {
			if edge.Type == "expert_connector" && names[edge.TargetPluginID] != "search-"+n.Source.Name {
				t.Fatal("expert references another source connector")
			}
		}
	}
	in.Resources[0], in.Resources[len(in.Resources)-1] = in.Resources[len(in.Resources)-1], in.Resources[0]
	q, err := Build(in, Policy{})
	if err != nil || !reflect.DeepEqual(p, q) {
		t.Fatal("input order changed names or references")
	}
}

func TestConnectorSourceSuffixCollisionsAndLength(t *testing.T) {
	a, b, c := connectorFixture("first", "one"), connectorFixture("second", "two"), connectorFixture("third", "three")
	c.Plugin.Name = "search-search"
	c.Plugin.Manifest = bytes.ReplaceAll(c.Plugin.Manifest, []byte(`"search"`), []byte(`"search-search"`))
	p, err := Build(connectorTeamInput([]Resource{a, b, c}), Policy{})
	if err != nil || len(p.Nodes) != 7 {
		t.Fatalf("Build: %v %+v", err, p.Issues)
	}
	names := map[string]bool{}
	for _, n := range p.Nodes {
		if n.Plugin.Type != model.PluginTypeConnector {
			continue
		}
		key, err := model.NormalizePluginName(n.Plugin.Name)
		if err != nil || names[key] || !strings.HasPrefix(n.Plugin.Name, "search-search") {
			t.Fatalf("suffix collision unresolved: %q", n.Plugin.Name)
		}
		names[key] = true
		if n.SourceID == c.Plugin.ID && n.Plugin.Name != c.Plugin.Name {
			t.Fatal("existing non-conflicting name changed")
		}
	}
	for _, long := range []string{strings.Repeat("专家", 100), strings.Repeat("ß", 160)} {
		name := connectorSourceName(long, long, "0123456789ab")
		if _, err := model.NormalizePluginName(name); err != nil || len([]rune(name)) > 128 || !strings.HasSuffix(name, "-0123456789ab") {
			t.Fatal("suffix name exceeds database/installation bounds")
		}
	}
}

func connectorTeamInput(connectors []Resource) Input {
	in := Input{SnapshotID: "connector-team", Resources: append([]Resource(nil), connectors...)}
	team := snapshotExpert("team", model.PluginTypeExpertTeam, model.PluginListingStatePublished)
	for _, c := range connectors {
		expert := snapshotExpert("expert-"+c.Plugin.ID, model.PluginTypeExpert, model.PluginListingStatePublished)
		expert.Relations = []model.PluginRelation{{ID: "connector-" + c.Plugin.ID, SourcePluginID: expert.Plugin.ID, SourcePluginType: expert.Plugin.Type, TargetPluginID: c.Plugin.ID, TargetPluginType: c.Plugin.Type, Type: "expert_connector", Status: 1}}
		metadata, _ := json.Marshal(map[string]any{"member_key": expert.Plugin.ID, "role": "worker", "is_leader": len(team.Relations) == 0})
		team.Relations = append(team.Relations, model.PluginRelation{ID: "member-" + c.Plugin.ID, SourcePluginID: team.Plugin.ID, SourcePluginType: team.Plugin.Type, TargetPluginID: expert.Plugin.ID, TargetPluginType: expert.Plugin.Type, Type: "expert_team_expert", Status: 1, Data: metadata})
		in.Resources = append(in.Resources, expert)
	}
	in.Resources = append(in.Resources, team)
	return in
}

func TestSeparateInstallationsKeepOriginalConnectorNames(t *testing.T) {
	a, b := connectorFixture("first", "one"), connectorFixture("second", "two")
	in := connectorTeamInput([]Resource{a, b})
	in.Resources = in.Resources[:len(in.Resources)-1] // No shared team closure.
	p, err := Build(in, Policy{})
	if err != nil {
		t.Fatal(err, p.Issues)
	}
	for _, n := range p.Nodes {
		if n.Plugin.Type == model.PluginTypeConnector && n.Plugin.Name != "search" {
			t.Fatal("unrelated installation renamed")
		}
	}
}

func TestDefaultMCPServerKeyAliasesPreserveBothResources(t *testing.T) {
	a, b := connectorFixture("first", "one"), connectorFixture("second", "two")
	in := connectorTeamInput([]Resource{a, b})
	in.Resources[2].Relations = append(in.Resources[2].Relations, model.PluginRelation{ID: "second-connector", SourcePluginID: in.Resources[2].Plugin.ID, SourcePluginType: model.PluginTypeExpert, TargetPluginID: b.Plugin.ID, TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1})
	p, err := Build(in, Policy{})
	if err != nil {
		t.Fatal(err, p.Issues)
	}
	inputs := []pluginsvc.CurrentConnectorMCP{}
	aliases := map[string]bool{}
	for _, n := range p.Nodes {
		if n.Plugin.Type != model.PluginTypeConnector {
			continue
		}
		alias := n.MCPAliases["search"]
		if alias == "" || aliases[alias] || !mcpAliasName.MatchString(alias) || len(n.MCPOriginal) == 0 {
			t.Fatal("invalid/missing unique alias")
		}
		aliases[alias] = true
		_, original, _ := decodeMCPDocument(n.MCPOriginal)
		_, target, _ := decodeMCPDocument(n.Files[0].Data)
		if len(original) != 1 || len(target) != 1 || !sameJSON(original["search"], target[alias]) || len(n.Files) != 2 || string(n.Files[1].Data) != map[string]string{"first": "one", "second": "two"}[n.SourceID] {
			t.Fatal("server values or supporting bytes changed")
		}
		inputs = append(inputs, pluginsvc.CurrentConnectorMCP{ConnectorID: n.Plugin.ID, Config: string(n.Files[0].Data)})
	}
	if len(inputs) != 2 {
		t.Fatal("resource lost")
	}
	if _, err := pluginsvc.MergeCurrentConnectorMCP(inputs); err != nil {
		t.Fatal("default consumer still rejects aliases", err)
	}
	if issues := validateFinalContracts(p); len(issues) != 0 {
		t.Fatal(issues)
	}
	// Saved plans must reject aliases whose package changes the server values.
	for i := range p.Nodes {
		if len(p.Nodes[i].MCPAliases) > 0 {
			p.Nodes[i].Plugin.Package = bytes.ReplaceAll(p.Nodes[i].Plugin.Package, []byte("python"), []byte("changed-command"))
			break
		}
	}
	if _, err := Apply(context.Background(), nil, p, DatabaseIdentity{}); err != ErrIssues {
		t.Fatal("altered saved projection reached persistence", err)
	}
}

func TestMCPAliasEvidenceBindsRunAndReceiptIdentity(t *testing.T) {
	original := []byte(`{"mcpServers":{"search":{"command":"python"}}}`)
	projected, ok := projectMCPAliases(original, map[string]string{"search": "search-alias"})
	if !ok {
		t.Fatal("invalid fixture")
	}
	n := Node{MCPOriginal: original, MCPAliases: map[string]string{"search": "search-alias"}}
	beforeNode, beforeRun := nodeHash(n), applyIdentity(Plan{Nodes: []Node{n}})
	n.MCPOriginal = bytes.ReplaceAll(original, []byte("search"), []byte("legacy"))
	n.MCPAliases = map[string]string{"legacy": "search-alias"}
	after, ok := projectMCPAliases(n.MCPOriginal, n.MCPAliases)
	if !ok || !sameJSON(projected, after) {
		t.Fatal("fixture must retain identical target projection")
	}
	if nodeHash(n) == beforeNode || applyIdentity(Plan{Nodes: []Node{n}}) == beforeRun {
		t.Fatal("replaced original and alias map retained persistence identity")
	}
	for _, mutate := range []func(*Node){
		func(n *Node) { n.MCPOriginal = append(n.MCPOriginal, ' ') },
		func(n *Node) { n.MCPAliases = map[string]string{"legacy": "different-alias"} },
	} {
		beforeNode, beforeRun := nodeHash(n), applyIdentity(Plan{Nodes: []Node{n}})
		mutate(&n)
		if nodeHash(n) == beforeNode || applyIdentity(Plan{Nodes: []Node{n}}) == beforeRun {
			t.Fatal("alias evidence missing from persistence identity")
		}
	}
}
