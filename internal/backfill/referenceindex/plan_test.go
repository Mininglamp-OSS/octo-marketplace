package referenceindex

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func sample(t *testing.T) Dataset {
	t.Helper()
	pin := "skill-v1"
	raw, err := model.EncodeSnapshotRelations([]model.PluginRelation{{ID: "edge", SourcePluginID: "expert", SourcePluginType: model.PluginTypeExpert, TargetPluginID: "skill", TargetPluginType: model.PluginTypeSkill, TargetVersionID: &pin, Type: "expert_skill", Status: 1}})
	if err != nil {
		t.Fatal(err)
	}
	return Dataset{Resources: []Resource{{ID: "expert", Type: model.PluginTypeExpert}, {ID: "skill", Type: model.PluginTypeSkill}}, Snapshots: []Snapshot{{PluginID: "expert", VersionID: "expert-v1", Relations: raw}, {PluginID: "skill", VersionID: "skill-v1", Relations: json.RawMessage("[]")}}, Releases: []Release{{PluginID: "skill", VersionID: "skill-v1"}}}
}

func TestBuildDoesNotGuessOrPartiallyIndex(t *testing.T) {
	for _, tt := range []struct {
		name, reason string
		change       func(*Dataset)
	}{
		{"released pin", "", func(*Dataset) {}},
		{"no release", "missing_release_evidence", func(d *Dataset) { d.Releases = nil }},
		{"foreign version", "wrong_target_version", func(d *Dataset) { d.Snapshots[1].PluginID = "expert" }},
		{"embedded", "embedded_target", func(d *Dataset) { d.Resources[1].Embedded = true }},
		{"missing pin", "missing_pin", func(d *Dataset) {
			d.Snapshots[0].Relations = json.RawMessage(`[{"relation_id":"edge","source_plugin_id":"expert","source_plugin_type":"expert","target_plugin_id":"skill","target_plugin_type":"skill","relation_type":"expert_skill","status":1}]`)
		}},
		{"null", "invalid_relations", func(d *Dataset) { d.Snapshots[0].Relations = json.RawMessage(`null`) }},
		{"missing identity", "invalid_relation_identity", func(d *Dataset) { d.Snapshots[0].Relations = json.RawMessage(`[{}]`) }},
		{"duplicate pin", "invalid_relations", func(d *Dataset) {
			d.Snapshots[0].Relations = json.RawMessage(`[{"target_version_id":"a","target_version_id":"b"}]`)
		}},
		{"mixed aliases", "invalid_relations", func(d *Dataset) {
			d.Snapshots[0].Relations = json.RawMessage(`[{"target_version_id":"a","TargetVersionID":"b"}]`)
		}},
		{"duplicate version", "ambiguous_source", func(d *Dataset) { d.Snapshots = append(d.Snapshots, d.Snapshots[0]) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := sample(t)
			tt.change(&d)
			p := Build(d)[0]
			if tt.reason == "" {
				if !p.Complete || len(p.Edges) != 1 {
					t.Fatalf("valid snapshot: %+v", p)
				}
				return
			}
			if p.Complete || len(p.Edges) != 0 {
				t.Fatalf("incomplete snapshot indexed: %+v", p)
			}
			found := false
			for _, r := range p.Reasons {
				found = found || r == tt.reason
			}
			if !found {
				t.Fatalf("missing reason %s: %+v", tt.reason, p)
			}
		})
	}
}

func TestConnectorHasNoVersionAndLegacyCodecIsPreserved(t *testing.T) {
	d := Dataset{Resources: []Resource{{ID: "e", Type: model.PluginTypeExpert}, {ID: "c", Type: model.PluginTypeConnector}}, Snapshots: []Snapshot{{PluginID: "e", VersionID: "v", Relations: json.RawMessage(`[{"ID":"r","SourcePluginID":"e","SourcePluginType":"expert","TargetPluginID":"c","TargetPluginType":"connector","Type":"expert_connector","Status":1}]`)}}}
	before := string(d.Snapshots[0].Relations)
	p := Build(d)[0]
	if !p.Complete || len(p.Edges) != 1 || p.Edges[0].TargetVersionID != nil {
		t.Fatalf("connector plan: %+v", p)
	}
	if string(d.Snapshots[0].Relations) != before {
		t.Fatal("modified history")
	}
	d.Snapshots[0].Relations = json.RawMessage(`[{"relation_id":"r","source_plugin_id":"e","source_plugin_type":"expert","target_plugin_id":"c","target_plugin_type":"connector","relation_type":"expert_connector","status":1,"target_version_id":"v"}]`)
	if got := Build(d)[0]; got.Complete || !reflect.DeepEqual(got.Reasons, []string{"connector_version_forbidden"}) {
		t.Fatalf("versioned connector: %+v", got)
	}
}
