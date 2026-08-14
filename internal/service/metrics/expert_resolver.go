package metrics

import (
	"context"
	"errors"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
)

// ExpertService is the subset of the expert service needed for visibility
// checks on experts (专家) and squads (专家团).
type ExpertService interface {
	GetExpert(ctx context.Context, caller expertsvc.Caller, id string) (*model.ExpertAgentDetail, error)
	GetSquad(ctx context.Context, caller expertsvc.Caller, id string) (*model.ExpertSquadDetail, error)
}

// visibleUnlessNotFound maps a Get* outcome onto the CanView contract shared by
// the expert and squad resolvers: not-found/not-visible → (false, nil), any
// other error propagates, success → visible.
func visibleUnlessNotFound(err error) (bool, error) {
	if err != nil {
		if errors.Is(err, expertsvc.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func expertCaller(caller Caller) expertsvc.Caller {
	return expertsvc.Caller{UID: caller.UID, SpaceID: caller.SpaceID}
}

// ExpertResolver checks whether an expert exists and is visible to the caller.
type ExpertResolver struct {
	expertSvc ExpertService
}

// NewExpertResolver creates an ExpertResolver.
func NewExpertResolver(expertSvc ExpertService) *ExpertResolver {
	return &ExpertResolver{expertSvc: expertSvc}
}

// CanView returns true if the expert exists and is visible to the caller.
func (r *ExpertResolver) CanView(ctx context.Context, resourceID string, caller Caller) (bool, error) {
	_, err := r.expertSvc.GetExpert(ctx, expertCaller(caller), resourceID)
	return visibleUnlessNotFound(err)
}

// SquadResolver checks whether a squad exists and is visible to the caller.
type SquadResolver struct {
	expertSvc ExpertService
}

// NewSquadResolver creates a SquadResolver.
func NewSquadResolver(expertSvc ExpertService) *SquadResolver {
	return &SquadResolver{expertSvc: expertSvc}
}

// CanView returns true if the squad exists and is visible to the caller.
func (r *SquadResolver) CanView(ctx context.Context, resourceID string, caller Caller) (bool, error) {
	_, err := r.expertSvc.GetSquad(ctx, expertCaller(caller), resourceID)
	return visibleUnlessNotFound(err)
}
