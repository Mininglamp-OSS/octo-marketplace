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

func TestCheckPlannedNamesRejectsBlankHistoricalDraft(t *testing.T) {
	space := "space-a"
	resources := []Resource{{
		Plugin:        model.Plugin{ID: "blank", Name: " ", Type: model.PluginTypeSkill, SpaceID: &space},
		PublishedName: "Alpha",
	}}
	if err := checkPlannedNames(&Unit{}, resources); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("blank historical draft was silently ignored: %v", err)
	}
}

func TestTeamPlannedNamesIncludeDraftAndPublishedReservations(t *testing.T) {
	space := "space-a"
	resources := []Resource{{Plugin: model.Plugin{ID: "team", Name: "Beta", Type: model.PluginTypeExpertTeam, SpaceID: &space}, PublishedName: "Alpha"}}
	for _, name := range []string{" beta ", "ＡＬＰＨＡ"} {
		node := Node{Clone: true, Mapping: Mapping{TargetID: "clone", Type: model.PluginTypeExpertTeam}, Plugin: model.Plugin{ID: "clone", Name: name, Type: model.PluginTypeExpertTeam, SpaceID: &space}}
		if err := checkPlannedNames(&Unit{Nodes: []Node{node}}, resources); !errors.Is(err, ErrNameConflict) {
			t.Fatalf("%q: %v", name, err)
		}
	}
}

func TestCheckPlannedNamesGlobalMarketReservation(t *testing.T) {
	space, foreign := "space-a", "space-b"
	for _, typ := range []model.PluginType{model.PluginTypeSkill, model.PluginTypeExpert, model.PluginTypeExpertTeam} {
		for _, tc := range []struct {
			name                                      string
			visibility                                model.PluginVisibility
			listing                                   model.PluginListingState
			status                                    int
			published, wanted                         string
			foreignScope, deleted, embedded, conflict bool
		}{
			{name: "public release", visibility: model.PluginVisibilityPublic, listing: model.PluginListingStatePublished, status: 1, published: "Official", wanted: " ＯＦＦＩＣＩＡＬ ", conflict: true},
			{name: "system release", visibility: model.PluginVisibilitySystem, listing: model.PluginListingStatePublished, status: 1, published: "Official", wanted: "official", conflict: true},
			{name: "public release in foreign space", visibility: model.PluginVisibilityPublic, listing: model.PluginListingStatePublished, status: 1, published: "Official", wanted: "official", foreignScope: true, conflict: true},
			{name: "legacy published name", visibility: model.PluginVisibilityPublic, listing: model.PluginListingStatePublished, status: 1, wanted: "draft", conflict: true},
			{name: "unreleased global rename is hidden", visibility: model.PluginVisibilityPublic, listing: model.PluginListingStatePublished, status: 1, published: "Official", wanted: "draft"},
			{name: "foreign private is hidden", visibility: model.PluginVisibilityPrivate, listing: model.PluginListingStatePublished, status: 1, published: "Official", wanted: "official", foreignScope: true},
			{name: "global draft is hidden", visibility: model.PluginVisibilitySystem, listing: model.PluginListingStateDraft, status: 1, wanted: "draft"},
			{name: "delisted global is hidden", visibility: model.PluginVisibilityPublic, listing: model.PluginListingStateDelisted, status: 1, published: "Official", wanted: "official"},
			{name: "inactive global is hidden", visibility: model.PluginVisibilityPublic, listing: model.PluginListingStatePublished, published: "Official", wanted: "official"},
			{name: "deleted global is hidden", visibility: model.PluginVisibilityPublic, listing: model.PluginListingStatePublished, status: 1, published: "Official", wanted: "official", deleted: true},
			{name: "embedded global does not reserve", visibility: model.PluginVisibilityPublic, listing: model.PluginListingStatePublished, status: 1, published: "Official", wanted: "official", embedded: true},
		} {
			t.Run(string(typ)+"/"+tc.name, func(t *testing.T) {
				existing := Resource{Plugin: model.Plugin{ID: "global", Name: "Draft", Type: typ, Visibility: tc.visibility, ListingState: tc.listing, Status: tc.status, IsEmbedded: tc.embedded}, PublishedName: tc.published}
				if tc.foreignScope {
					existing.Plugin.SpaceID = &foreign
				}
				if tc.deleted {
					existing.Plugin.DeletedAt = newTime()
				}
				node := Node{Clone: true, Mapping: Mapping{TargetID: "clone", Type: typ}, Plugin: model.Plugin{ID: "clone", Name: tc.wanted, Type: typ, SpaceID: &space}}
				err := checkPlannedNames(&Unit{Nodes: []Node{node}}, []Resource{existing})
				if tc.conflict {
					if !errors.Is(err, ErrNameConflict) {
						t.Fatalf("global name allowed: %v", err)
					}
				} else if err != nil {
					t.Fatalf("hidden name blocked clone: %v", err)
				}
			})
		}
	}
}
