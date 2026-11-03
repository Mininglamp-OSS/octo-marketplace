package plugin

import (
	"context"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

type VisibilityParams struct {
	ExpectedRevision           string
	PlanRevision               string
	Confirm                    bool
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
	if detail.Plugin.Type == model.PluginTypeConnector {
		if strings.TrimSpace(p.ExpectedRevision) == "" {
			return nil, &ReviewFieldError{Field: "expected_revision", Reason: "required"}
		}
	} else if strings.TrimSpace(p.ExpectedPublishedVersionID) == "" {
		return nil, &ReviewFieldError{Field: "expected_published_version_id", Reason: "required"}
	}
	if p.Visibility == model.PluginVisibilitySpace && detail.Plugin.Visibility != p.Visibility {
		if previewer, ok := s.repo.(interface {
			PreviewVisibility(context.Context, pluginrepo.Scope, string, model.PluginVisibility) (*pluginrepo.VisibilityPlan, error)
		}); ok {
			plan, e := previewer.PreviewVisibility(ctx, scope(caller), id, p.Visibility)
			if e != nil {
				return nil, mapStoreError(e)
			}
			if p.PlanRevision != "" && p.PlanRevision != plan.PlanRevision {
				return nil, ErrConflict
			}
			for _, item := range append([]pluginrepo.AudienceResource{plan.Resource}, plan.Dependencies...) {
				if item.Type == model.PluginTypeConnector {
					continue
				}
				if item.PublishedVersionID == nil {
					return nil, ErrVersionBaselineUnavailable
				}
				d, e := s.currentDetail(ctx, caller, item.PluginID, false)
				if e != nil {
					return nil, e
				}
				v, e := s.ManagedVersion(ctx, caller, item.PluginID, *item.PublishedVersionID, false)
				if e != nil {
					return nil, e
				}
				if e = s.verifyVersionFiles(ctx, d.Plugin, v); e != nil {
					return nil, e
				}
			}
		} else if detail.Plugin.Type != model.PluginTypeConnector {
			v, e := s.ManagedVersion(ctx, caller, id, p.ExpectedPublishedVersionID, false)
			if e != nil {
				return nil, e
			}
			if v.Release == nil {
				return nil, ErrVersionBaselineUnavailable
			}
			if e = s.verifyVersionFiles(ctx, detail.Plugin, v); e != nil {
				return nil, e
			}
		}
	}
	store, ok := s.repo.(interface {
		ChangeVisibility(context.Context, pluginrepo.Scope, pluginrepo.VisibilityParams) (*pluginrepo.VisibilityResult, error)
	})
	if !ok {
		return nil, ErrInvalidRequest
	}
	out, err := store.ChangeVisibility(ctx, scope(caller), pluginrepo.VisibilityParams{ExpectedRevision: p.ExpectedRevision, PlanRevision: p.PlanRevision, Confirm: p.Confirm, PluginID: id, ExpectedPublishedVersionID: p.ExpectedPublishedVersionID, Visibility: p.Visibility, OperatorID: caller.UID, OperatorName: caller.Name, RequestID: caller.RequestID})
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

func (s *Service) PreviewVisibility(ctx context.Context, caller Caller, id string, target model.PluginVisibility) (*pluginrepo.VisibilityPlan, error) {
	if validateCaller(caller) != nil {
		return nil, ErrInvalidRequest
	}
	if _, err := s.versionStore(); err != nil {
		return nil, err
	}
	id, err := parseStorageID(id)
	if err != nil {
		return nil, err
	}
	if target != model.PluginVisibilityPrivate && target != model.PluginVisibilitySpace {
		return nil, &ReviewFieldError{Field: "visibility", Reason: "invalid"}
	}
	store, ok := s.repo.(interface {
		PreviewVisibility(context.Context, pluginrepo.Scope, string, model.PluginVisibility) (*pluginrepo.VisibilityPlan, error)
	})
	if !ok {
		return nil, ErrInvalidRequest
	}
	plan, err := store.PreviewVisibility(ctx, scope(caller), id, target)
	return plan, mapStoreError(err)
}
