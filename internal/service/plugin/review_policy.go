package plugin

import (
	"context"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func (s *Service) GetReviewPolicy(ctx context.Context, caller Caller) (model.PluginReviewPolicy, error) {
	if err := validateCaller(caller); err != nil {
		return model.PluginReviewPolicy{}, err
	}
	policy, err := s.repo.GetReviewPolicy(ctx, scope(caller))
	if err != nil {
		return model.PluginReviewPolicy{}, mapStoreError(err)
	}
	return policy, nil
}

func (s *Service) UpdateReviewPolicy(ctx context.Context, caller Caller, enabled bool) (model.PluginReviewPolicy, error) {
	if err := validateCaller(caller); err != nil {
		return model.PluginReviewPolicy{}, err
	}
	if caller.SpaceRole < SpaceRoleAdmin {
		return model.PluginReviewPolicy{}, ErrReviewPolicyForbidden
	}
	policy, err := s.repo.UpsertReviewPolicy(ctx, scope(caller), enabled, caller.UID, caller.Name)
	if err != nil {
		return model.PluginReviewPolicy{}, mapStoreError(err)
	}
	return policy, nil
}
