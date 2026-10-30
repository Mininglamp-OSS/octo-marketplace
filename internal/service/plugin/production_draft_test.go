package plugin

import (
	"context"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"testing"
)

func TestIndependentEmptyDraftAllowedButDraftReferenceStillRequiresPublishedSnapshot(t *testing.T) {
	s := &Service{references: true}
	p := &model.Plugin{Type: model.PluginTypeExpertTeam, ListingState: model.PluginListingStateDraft}
	if err := s.validateReferenceShape(context.Background(), p, nil); err != nil {
		t.Fatalf("independent draft rejected: %v", err)
	}
	edge := model.PluginRelation{Type: "expert_team_expert", TargetPluginID: "draft-member", TargetPluginType: model.PluginTypeExpert, Status: 1}
	if err := s.validateReferenceShape(context.Background(), p, []model.PluginRelation{edge}); err == nil {
		t.Fatal("unreleased unpinned draft reference accepted")
	}
	p.ListingState = model.PluginListingStatePublished
	if err := s.validateReferenceShape(context.Background(), p, nil); err == nil {
		t.Fatal("published team without leader accepted")
	}
}
