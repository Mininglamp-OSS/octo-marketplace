package plugin

import (
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"testing"
)

func TestReferenceAudience(t *testing.T) {
	space, other := "a", "b"
	for _, tt := range []struct {
		name           string
		source, target model.Plugin
		want           bool
	}{
		{"owner private", model.Plugin{Visibility: "private", OwnerUID: "one", SpaceID: &space}, model.Plugin{Visibility: "private", OwnerUID: "one", SpaceID: &space}, true},
		{"other owner private", model.Plugin{Visibility: "private", OwnerUID: "one", SpaceID: &space}, model.Plugin{Visibility: "private", OwnerUID: "two", SpaceID: &space}, false},
		{"space excludes private", model.Plugin{Visibility: "space", OwnerUID: "one", SpaceID: &space}, model.Plugin{Visibility: "private", OwnerUID: "one", SpaceID: &space}, false},
		{"space same space", model.Plugin{Visibility: "space", SpaceID: &space}, model.Plugin{Visibility: "space", SpaceID: &space}, true},
		{"cross space", model.Plugin{Visibility: "private", SpaceID: &space}, model.Plugin{Visibility: "space", SpaceID: &other}, false},
		{"system excludes space", model.Plugin{Visibility: "system", SpaceID: &space}, model.Plugin{Visibility: "space", SpaceID: &space}, false},
		{"system target", model.Plugin{Visibility: "space", SpaceID: &space}, model.Plugin{Visibility: "system"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := referenceAudience(tt.source, tt.target); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
func TestReferenceMetadataAllowsOptionalAuditAndRejectsOverrides(t *testing.T) {
	version := "released"
	valid := model.PluginRelation{Status: 1, TargetPluginID: "skill", TargetPluginType: model.PluginTypeSkill, TargetVersionID: &version, Type: "expert_skill"}
	if err := ValidateReferenceMetadata(model.PluginTypeExpert, []model.PluginRelation{valid}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*model.PluginRelation){func(r *model.PluginRelation) { r.Data = json.RawMessage(`{"instruction":"override"}`) }, func(r *model.PluginRelation) { r.TargetPluginType = model.PluginTypeConnector }} {
		r := valid
		change(&r)
		if ValidateReferenceMetadata(model.PluginTypeExpert, []model.PluginRelation{r}) == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	valid.TargetVersionID = nil
	if err := ValidateReferenceMetadata(model.PluginTypeExpert, []model.PluginRelation{valid}); err != nil {
		t.Fatal("ID-only floating reference rejected", err)
	}
	team := model.PluginRelation{Status: 1, TargetPluginID: "expert", TargetPluginType: model.PluginTypeExpert, TargetVersionID: &version, Type: "expert_team_expert", Data: json.RawMessage(`{"member_key":"lead","role":"lead","is_leader":true}`)}
	if err := ValidateReferenceMetadata(model.PluginTypeExpertTeam, []model.PluginRelation{team}); err != nil {
		t.Fatal(err)
	}
	team.Data = json.RawMessage(`{"member_key":"lead","is_leader":true,"skills":[]}`)
	if ValidateReferenceMetadata(model.PluginTypeExpertTeam, []model.PluginRelation{team}) == nil {
		t.Fatal("accepted member capability override")
	}
	connector := model.PluginRelation{Status: 1, TargetPluginID: "connector", TargetPluginType: model.PluginTypeConnector, Type: "expert_connector"}
	if ValidateReferenceMetadata(model.PluginTypeExpert, []model.PluginRelation{connector}) != nil {
		t.Fatal("rejected ID-only connector")
	}
	connector.TargetVersionID = &version
	if ValidateReferenceMetadata(model.PluginTypeExpert, []model.PluginRelation{connector}) == nil {
		t.Fatal("accepted connector version")
	}
}
