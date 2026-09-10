package plugin

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

var (
	// ErrAlreadyPublished is returned when publish is called on a plugin that is
	// already listed. A state conflict, not a validation problem: the request was
	// well formed and the caller may act on this plugin, it is simply already in
	// the state they asked for.
	ErrAlreadyPublished = errors.New("plugin is already published")
	// ErrNotPublished is returned when delist is called on a plugin that is not
	// listed — already delisted, still a draft, or a lost race.
	ErrNotPublished = errors.New("plugin is not published")
)

// PublishParams is one press of the 发布 button.
//
// There is deliberately no "submit for review" flag and no content: the caller
// says "publish this", and the plugin's DECLARED VISIBILITY decides whether that
// means listing it immediately or opening a review request. Putting the rule here
// rather than in the client is the point — a browser that guesses wrong either
// lists something unreviewed or strands a plugin in draft forever.
type PublishParams struct {
	PluginID string
	// Version and Changelog are only consulted on the review branch, where the
	// request needs a label. Version defaults to the draft's current_version.
	Version   string
	Changelog string
}

// PublishResult reports which branch fired so the client does not have to infer
// it from a second request.
type PublishResult struct {
	Plugin *Detail
	// Review is set only when the publish opened a review request.
	Review *model.PluginReviewRequest
}

// Publish lists an unlisted plugin, or opens the review request that will list
// it.
//
//	visibility=private -> listed immediately; there is no org audience to protect,
//	                      and no review channel exists for a row nobody else reads.
//	visibility=space   -> a pending review request; the plugin stays a draft and
//	                      ApproveReview is what publishes it.
//
// Owner-only. Refusing an already-published plugin with a conflict (rather than
// treating it as a no-op) keeps the button honest: a second press means the
// client's view is stale.
func (s *Service) Publish(ctx context.Context, caller Caller, params PublishParams) (*PublishResult, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	params.PluginID = strings.TrimSpace(params.PluginID)
	if params.PluginID == "" {
		return nil, ErrInvalidRequest
	}

	detail, err := s.Detail(ctx, caller, params.PluginID, true)
	if err != nil {
		return nil, err
	}
	if detail.Plugin.OwnerUID != caller.UID {
		// Not a 403: a non-owner must not learn that this plugin exists.
		return nil, ErrNotFound
	}
	// An embedded child is published by its container, never on its own — matching
	// SubmitReview, Update and Delete.
	if detail.Plugin.IsEmbedded {
		return nil, ErrNotFound
	}
	if detail.Plugin.ListingState == model.PluginListingStatePublished {
		return nil, ErrAlreadyPublished
	}
	pending, err := s.repo.HasPendingReview(ctx, scope(caller), detail.Plugin.ID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if pending {
		return nil, ErrReviewPending
	}

	if detail.Plugin.Visibility == model.PluginVisibilitySpace {
		policy, err := s.repo.GetReviewPolicy(ctx, scope(caller))
		if err != nil {
			return nil, mapStoreError(err)
		}
		// The review branch. Content fields are left empty on purpose: the draft row
		// IS the content, and freezeSubmission snapshots it — which is honest here
		// precisely because the plugin is not yet listed, so there is no live version
		// the snapshot could be confused with.
		version := strings.TrimSpace(params.Version)
		if version == "" && detail.Plugin.CurrentVersion != nil {
			version = strings.TrimSpace(*detail.Plugin.CurrentVersion)
		}
		if version == "" {
			version = defaultCurrentVersion
		}
		review, err := s.submitReview(ctx, caller, ReviewSubmitParams{
			PluginID:  detail.Plugin.ID,
			Version:   version,
			Changelog: params.Changelog,
		}, !policy.IsAutoApproveEnabled)
		if err != nil {
			return nil, err
		}
		if policy.IsAutoApproveEnabled {
			published, err := s.autoApproveReview(ctx, caller, review, "")
			if err != nil {
				return nil, err
			}
			published.IconURL = s.resolveIcon(ctx, published.Icon)
			return &PublishResult{Plugin: &Detail{Plugin: published, Relations: detail.Relations}}, nil
		}
		// The plugin is unchanged — still a draft — so re-read nothing and return the
		// detail already in hand alongside the request that will decide it.
		return &PublishResult{Plugin: detail, Review: review}, nil
	}

	// The immediate branch. `system` cannot reach here: validVisibility only admits
	// it for a system admin, whose rows are managed through /api/v1/admin and are
	// stamped published on create.
	if detail.Plugin.Visibility != model.PluginVisibilityPrivate {
		return nil, ErrInvalidRequest
	}
	published, err := s.repo.PublishPlugin(ctx, scope(caller), pluginrepo.PublishParams{
		PluginID:     detail.Plugin.ID,
		OperatorID:   caller.UID,
		OperatorName: caller.Name,
		RequestID:    caller.RequestID,
	})
	if err != nil {
		return nil, mapStoreError(err)
	}
	published.IconURL = s.resolveIcon(ctx, published.Icon)
	return &PublishResult{Plugin: &Detail{Plugin: published, Relations: detail.Relations}}, nil
}

// DelistParams is one Space-admin takedown.
type DelistParams struct {
	PluginID string
	Reason   string
}

// Delist removes a listed plugin from the marketplace.
//
// Space admins only, using the SAME predicate as approve and reject: taking
// something down is the same authority as putting it up, and a second notion of
// "who moderates this Space" would drift from the first. The author deliberately
// cannot do this — self-delisting through the write path was removed — so that a
// plugin the org depends on cannot vanish at its author's discretion.
//
// The plugin stays editable and re-publishable afterwards.
func (s *Service) Delist(ctx context.Context, caller Caller, params DelistParams) (*Detail, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	params.PluginID = strings.TrimSpace(params.PluginID)
	params.Reason = strings.TrimSpace(params.Reason)
	if params.PluginID == "" {
		return nil, ErrInvalidRequest
	}
	if utf8.RuneCountInString(params.Reason) > maxRejectReasonRunes {
		return nil, &ReviewFieldError{Field: "reason", Reason: "too_long"}
	}
	if !s.isReviewer(caller) {
		return nil, ErrReviewForbidden
	}
	delisted, err := s.repo.DelistPlugin(ctx, scope(caller), pluginrepo.DelistParams{
		PluginID:     params.PluginID,
		OperatorID:   caller.UID,
		OperatorName: caller.Name,
		RequestID:    caller.RequestID,
		Reason:       params.Reason,
	})
	if err != nil {
		// A plugin outside the caller's Space is ErrNotFound from the repository and
		// stays a 404: confirming existence across a Space boundary is a leak, even
		// to an admin of a different Space.
		if errors.Is(err, pluginrepo.ErrConflict) {
			return nil, ErrNotPublished
		}
		return nil, mapStoreError(err)
	}
	delisted.IconURL = s.resolveIcon(ctx, delisted.Icon)
	return &Detail{Plugin: delisted, Relations: []model.PluginRelation{}}, nil
}
