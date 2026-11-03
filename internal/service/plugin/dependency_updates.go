package plugin

import (
	"context"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func (s *Service) DependencyUpdates(ctx context.Context, caller Caller, id string, skip []pluginrepo.DependencyChoice) (*pluginrepo.DependencyPreview, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	if !s.references || !s.versionManagement {
		return nil, ErrNotFound
	}
	id, err := parseStorageID(id)
	if err != nil {
		return nil, err
	}
	store, ok := s.repo.(interface {
		DependencyUpdates(context.Context, pluginrepo.Scope, string, []pluginrepo.DependencyChoice) (*pluginrepo.DependencyPreview, error)
	})
	if !ok {
		return nil, ErrInvalidRequest
	}
	plan, err := store.DependencyUpdates(ctx, scope(caller), id, skip)
	return plan, mapStoreError(err)
}
