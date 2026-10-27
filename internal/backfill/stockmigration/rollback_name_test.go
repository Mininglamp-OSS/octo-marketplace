package stockmigration

import (
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestRollbackNameColumnsRestoresBothReservations(t *testing.T) {
	space := "space-a"
	r := Resource{
		Plugin:        model.Plugin{ID: "p", Name: "Beta", Type: model.PluginTypeSkill, SpaceID: &space},
		PublishedName: "Alpha",
	}
	draft, published, scope, err := rollbackNameColumns(r)
	if err != nil || draft != "beta" || published != "alpha" || scope != "space-a" {
		t.Fatalf("draft=%v published=%v scope=%q err=%v", draft, published, scope, err)
	}
}

func TestRollbackNameColumnsKeepsNonTargetEmpty(t *testing.T) {
	space := "space-a"
	r := Resource{Plugin: model.Plugin{ID: "t", Name: "Team", Type: model.PluginTypeExpertTeam, SpaceID: &space}}
	draft, published, scope, err := rollbackNameColumns(r)
	if err != nil || draft != nil || published != nil || scope != "space-a" {
		t.Fatalf("draft=%v published=%v scope=%q err=%v", draft, published, scope, err)
	}
}
