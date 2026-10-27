package stockmigration

import (
	"errors"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func newTime() *time.Time { t := time.Now(); return &t }

func TestCheckPlannedNames(t *testing.T) {
	spaceA, spaceB := "space-a", "space-b"
	resources := []Resource{
		{Plugin: model.Plugin{ID: "existing", Name: "Alpha", Type: model.PluginTypeSkill, SpaceID: &spaceA}},
		{Plugin: model.Plugin{ID: "deleted", Name: "Deleted", Type: model.PluginTypeSkill, SpaceID: &spaceA, DeletedAt: newTime()}},
	}
	for _, tc := range []struct {
		name  string
		nodes []Node
		want  error
	}{
		{"same space and type", []Node{{Clone: true, Mapping: Mapping{Type: model.PluginTypeSkill}, Plugin: model.Plugin{ID: "new", Name: " alpha ", Type: model.PluginTypeSkill, SpaceID: &spaceA}}}, ErrNameConflict},
		{"different type", []Node{{Clone: true, Mapping: Mapping{Type: model.PluginTypeExpert}, Plugin: model.Plugin{ID: "new", Name: "Alpha", Type: model.PluginTypeExpert, SpaceID: &spaceA}}}, nil},
		{"different space", []Node{{Clone: true, Mapping: Mapping{Type: model.PluginTypeSkill}, Plugin: model.Plugin{ID: "new", Name: "Alpha", Type: model.PluginTypeSkill, SpaceID: &spaceB}}}, nil},
		{"soft deleted does not occupy", []Node{{Clone: true, Mapping: Mapping{Type: model.PluginTypeSkill}, Plugin: model.Plugin{ID: "new", Name: "deleted", Type: model.PluginTypeSkill, SpaceID: &spaceA}}}, nil},
		{"reused target skips", []Node{{Clone: false, Mapping: Mapping{Type: model.PluginTypeSkill}, Plugin: model.Plugin{ID: "new", Name: "Alpha", Type: model.PluginTypeSkill, SpaceID: &spaceA}}}, nil},
		{"two planned clones collide", []Node{
			{Clone: true, Mapping: Mapping{SourceID: "a", TargetID: "a", Type: model.PluginTypeSkill}, Plugin: model.Plugin{ID: "a", Name: "Same", Type: model.PluginTypeSkill, SpaceID: &spaceA}},
			{Clone: true, Mapping: Mapping{SourceID: "b", TargetID: "b", Type: model.PluginTypeSkill}, Plugin: model.Plugin{ID: "b", Name: "same", Type: model.PluginTypeSkill, SpaceID: &spaceA}},
		}, ErrNameConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPlannedNames(&Unit{Nodes: tc.nodes}, resources)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestCheckPlannedNamesPublishedDraftReservation(t *testing.T) {
	space := "space-a"
	resources := []Resource{{
		Plugin:        model.Plugin{ID: "existing", Name: "Beta", Type: model.PluginTypeSkill, SpaceID: &space},
		PublishedName: "Alpha",
	}}
	node := Node{Clone: true, Mapping: Mapping{SourceID: "new", TargetID: "new", Type: model.PluginTypeSkill}, Plugin: model.Plugin{ID: "new", Name: "Beta", Type: model.PluginTypeSkill, SpaceID: &space}}
	if err := checkPlannedNames(&Unit{Nodes: []Node{node}}, resources); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("published Alpha / draft Beta reservation was not enforced: %v", err)
	}
}
