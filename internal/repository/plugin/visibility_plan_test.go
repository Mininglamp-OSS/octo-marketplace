package plugin

import (
	"context"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"testing"
)

func TestVisibilityPlanIncludesHistoricalOwnedDependencies(t *testing.T) {
	nodes := map[string]*visibilityNode{}
	makeNode := func(id string, typ model.PluginType, vis model.PluginVisibility) *visibilityNode {
		space := "space"
		n := &visibilityNode{Plugin: model.Plugin{ID: id, Name: id, Type: typ, OwnerUID: "owner", SpaceID: &space, Visibility: vis, ListingState: model.PluginListingStatePublished}, PublishedID: id + "-v2"}
		nodes[id] = n
		return n
	}
	root := makeNode("expert", model.PluginTypeExpert, model.PluginVisibilityPrivate)
	a := makeNode("a", model.PluginTypeSkill, model.PluginVisibilityPrivate)
	b := makeNode("b", model.PluginTypeSkill, model.PluginVisibilityPrivate)
	c := makeNode("connector", model.PluginTypeConnector, model.PluginVisibilityPrivate)
	a.Releases = []visibilityReleaseEdges{{ID: "a-v1"}}
	b.Releases = []visibilityReleaseEdges{{ID: "b-v1"}}
	aID, bID := "a-v1", "b-v1"
	root.Releases = []visibilityReleaseEdges{{ID: "expert-v1", Relations: []model.PluginRelation{{Type: "expert_skill", TargetPluginID: "a", TargetVersionID: &aID, Status: 1}}}, {ID: "expert-v2", Relations: []model.PluginRelation{{Type: "expert_skill", TargetPluginID: "b", TargetVersionID: &bID, Status: 1}, {Type: "expert_skill", TargetPluginID: "a", TargetVersionID: &aID, Status: 1}, {Type: "expert_connector", TargetPluginID: "connector", Status: 1}}}}
	reader := func(_ context.Context, id string) (*visibilityNode, error) {
		if n := nodes[id]; n != nil {
			return n, nil
		}
		return nil, ErrNotFound
	}
	plan, err := buildVisibilityPlan(context.Background(), Scope{CallerUID: "owner", SpaceID: "space"}, root, model.PluginVisibilitySpace, reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) != 0 || len(plan.Changes) != 4 {
		t.Fatalf("plan=%+v", plan)
	}
	if c.PublishedID == "" {
		t.Fatal("test fixture invalid")
	}
	for _, change := range plan.Changes {
		if change.PluginID == "connector" && change.PublishedVersionID != "" {
			t.Fatal("connector received a formal version")
		}
	}
	second, _ := buildVisibilityPlan(context.Background(), Scope{CallerUID: "owner", SpaceID: "space"}, root, model.PluginVisibilitySpace, reader)
	if plan.Revision != second.Revision {
		t.Fatal("unstable plan revision")
	}
	a.Plugin.Visibility = model.PluginVisibilitySpace
	changed, _ := buildVisibilityPlan(context.Background(), Scope{CallerUID: "owner", SpaceID: "space"}, root, model.PluginVisibilitySpace, reader)
	if plan.Revision == changed.Revision {
		t.Fatal("audience change did not invalidate preview")
	}
}

func TestVisibilityPlanNeverPromotesOtherOwnersPrivateDependency(t *testing.T) {
	space := "space"
	version := "skill-v1"
	root := &visibilityNode{Plugin: model.Plugin{ID: "expert", OwnerUID: "owner", SpaceID: &space, Visibility: model.PluginVisibilityPrivate, ListingState: model.PluginListingStatePublished}, PublishedID: "v1", Releases: []visibilityReleaseEdges{{ID: "v1", Relations: []model.PluginRelation{{Type: "expert_skill", TargetPluginID: "skill", TargetVersionID: &version, Status: 1}}}}}
	child := &visibilityNode{Plugin: model.Plugin{ID: "skill", Name: "hidden name", OwnerUID: "someone-else", SpaceID: &space, Visibility: model.PluginVisibilityPrivate, ListingState: model.PluginListingStatePublished}, PublishedID: version, Releases: []visibilityReleaseEdges{{ID: version}}}
	plan, err := buildVisibilityPlan(context.Background(), Scope{CallerUID: "owner", SpaceID: space}, root, model.PluginVisibilitySpace, func(context.Context, string) (*visibilityNode, error) { return child, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || len(plan.Blockers) != 1 {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.Blockers[0].Name != "" {
		t.Fatal("private dependency name leaked")
	}
}
