package plugin

import (
	"context"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

type VisibilityParams struct {
	PluginID                   string
	ExpectedPublishedVersionID string
	Visibility                 model.PluginVisibility
}

// ChangeVisibility manages publication audience independently of editable content.
func (s *Service) ChangeVisibility(ctx context.Context, caller Caller, p VisibilityParams) (*pluginrepo.VisibilityResult, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	if _, err := s.versionStore(); err != nil {
		return nil, err
	}
	id, err := parseStorageID(p.PluginID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.ExpectedPublishedVersionID) == "" {
		return nil, &ReviewFieldError{Field: "expected_published_version_id", Reason: "required"}
	}
	if p.Visibility != model.PluginVisibilityPrivate && p.Visibility != model.PluginVisibilitySpace {
		return nil, &ReviewFieldError{Field: "visibility", Reason: "invalid"}
	}
	// Read the frozen release for authorization and original-file integrity. No
	// content from a newer editable draft can become visible through this door.
	detail, err := s.currentDetail(ctx, caller, id, true)
	if err != nil {
		return nil, err
	}
	if detail.Plugin.OwnerUID != caller.UID || detail.Plugin.SpaceID == nil || *detail.Plugin.SpaceID != caller.SpaceID || detail.Plugin.IsEmbedded {
		return nil, ErrNotFound
	}
	if p.Visibility == model.PluginVisibilitySpace && detail.Plugin.Visibility != p.Visibility {
		v, err := s.ManagedVersion(ctx, caller, id, p.ExpectedPublishedVersionID, false)
		if err != nil {
			return nil, err
		}
		if v.Release == nil {
			return nil, ErrVersionBaselineUnavailable
		}
		if err = s.verifyVersionFiles(ctx, detail.Plugin, v); err != nil {
			return nil, err
		}
	}
	store, ok := s.repo.(interface {
		ChangeVisibility(context.Context, pluginrepo.Scope, pluginrepo.VisibilityParams) (*pluginrepo.VisibilityResult, error)
	})
	if !ok {
		return nil, ErrInvalidRequest
	}
	out, err := store.ChangeVisibility(ctx, scope(caller), pluginrepo.VisibilityParams{PluginID: id, ExpectedPublishedVersionID: p.ExpectedPublishedVersionID, Visibility: p.Visibility, OperatorID: caller.UID, OperatorName: caller.Name, RequestID: caller.RequestID})
	if err != nil {
		return nil, mapStoreError(err)
	}
	if out.Pending && out.ReviewCreated {
		review, err := s.repo.LoadReviewSnapshot(ctx, scope(caller), out.ReviewID, false)
		if err == nil {
			s.dispatchReviewCard(caller, review, detail.Plugin)
		}
	}
	return out, nil
}
