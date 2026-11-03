package plugin

import (
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"testing"
)

func TestDependencyUpdatesUseOnlyDirectFormalReleases(t *testing.T) {
	space := "space"
	old := "skill-1"
	root := &visibilityNode{Plugin: model.Plugin{ID: "expert", Type: model.PluginTypeExpert}, PublishedID: "expert-1", Releases: []visibilityReleaseEdges{{ID: "expert-1", Relations: []model.PluginRelation{{Type: "expert_skill", TargetPluginID: "skill", TargetVersionID: &old, Status: 1}, {Type: "expert_connector", TargetPluginID: "mcp", Status: 1}}}}}
	skill := &visibilityNode{Plugin: model.Plugin{ID: "skill", Name: "Unpublished draft title", Type: model.PluginTypeSkill, SpaceID: &space, OwnerUID: "owner", Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStatePublished}, PublishedID: "skill-2", Releases: []visibilityReleaseEdges{{ID: old, Label: "1.0.0"}, {ID: "skill-2", Label: "1.1.0", Name: "Released title"}}}
	got := directDependencyUpdates(root, map[string]*visibilityNode{"skill": skill}, map[string]bool{})
	if len(got) != 1 || got[0].FromVersionID != old || got[0].TargetVersionID != "skill-2" {
		t.Fatalf("updates %+v", got)
	}
	if got[0].Name != "Released title" {
		t.Fatalf("draft name leaked: %q", got[0].Name)
	}
	got = directDependencyUpdates(root, map[string]*visibilityNode{"skill": skill}, map[string]bool{"skill\x00skill-2": true})
	if len(got) != 1 || !got[0].IsSkipped {
		t.Fatal("skip lost manual adoption entry")
	}
	skill.Releases = append(skill.Releases, visibilityReleaseEdges{ID: "skill-3", Label: "1.2.0"})
	if got[0].Name != "Released title" {
		t.Fatalf("draft name leaked: %q", got[0].Name)
	}
	got = directDependencyUpdates(root, map[string]*visibilityNode{"skill": skill}, map[string]bool{"skill\x00skill-2": true})
	if len(got) != 1 || got[0].IsSkipped || got[0].TargetVersionID != "skill-3" {
		t.Fatal("higher release did not re-alert")
	}
	skill.Plugin.ListingState = model.PluginListingStateDelisted
	if len(directDependencyUpdates(root, map[string]*visibilityNode{"skill": skill}, nil)) != 0 {
		t.Fatal("unavailable target alerted")
	}
}
