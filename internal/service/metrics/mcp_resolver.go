package metrics

import (
	"context"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	marketplacesvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service"
)

type MCPService interface {
	Get(ctx context.Context, caller marketplacesvc.Caller, mcpID string) (model.Detail, *apierr.Error)
}

type MCPResolver struct {
	mcpSvc MCPService
}

func NewMCPResolver(mcpSvc MCPService) *MCPResolver {
	return &MCPResolver{mcpSvc: mcpSvc}
}

func (r *MCPResolver) CanView(ctx context.Context, resourceID string, caller Caller) (bool, error) {
	detail, apiErr := r.mcpSvc.Get(ctx, marketplacesvc.Caller{
		UID:     caller.UID,
		SpaceID: caller.SpaceID,
	}, resourceID)
	if apiErr != nil {
		if apiErr.Code == apierr.CodeNotFound {
			return false, nil
		}
		return false, apiErr
	}
	return detail.ID != "", nil
}
