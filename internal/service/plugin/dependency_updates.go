package plugin

import (
	"context"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	"strings"
)

type DependencyUpdateSubmitParams struct {
	ExpectedPublishedVersionID string
	ExpectedDraftRevision      string
	Targets                    []model.DependencyUpdateTarget
	Draft                      WriteRequest
	Version, Changelog         string
}

func (s *Service) SubmitDependencyUpdates(ctx context.Context, caller Caller, id string, params DependencyUpdateSubmitParams) (*pluginrepo.DependencyUpdatePublishResult, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	id, err := parseStorageID(id)
	if err != nil {
		return nil, err
	}
	if !s.versionManagement {
		return nil, ErrVersionBaselineUnavailable
	}
	if params.ExpectedPublishedVersionID == "" || params.ExpectedDraftRevision == "" || len(params.Targets) == 0 {
		return nil, ErrInvalidRequest
	}
	// Normalize once here: buildWrite trims the label into the snapshot while
	// recordRelease compares the declared label byte-for-byte, so forwarding the
	// raw request would reject an otherwise valid padded version at release time.
	version := strings.TrimSpace(params.Version)
	if !validVersion(version) || strings.TrimSpace(params.Changelog) == "" {
		return nil, &ReviewFieldError{Field: "version", Reason: "version_and_changelog_required"}
	}
	detail, err := s.currentDetail(ctx, caller, id, true)
	if err != nil {
		return nil, err
	}
	old := detail.Plugin
	if old.OwnerUID != caller.UID || old.IsEmbedded || (old.Type != model.PluginTypeExpert && old.Type != model.PluginTypeExpertTeam) {
		return nil, ErrNotFound
	}
	if params.Draft.Type != old.Type || params.Draft.Visibility != old.Visibility {
		return nil, ErrInvalidRequest
	}
	req := params.Draft
	req.Version = version
	req.Changelog = &params.Changelog
	req.Package = reinjectUpdateStorageKeys(req.Package, old.Package, old.AttachmentKeys)
	p, rels, err := s.buildWrite(ctx, caller, id, req, s.now(), false, old)
	if err != nil {
		return nil, err
	}
	p.CreatedAt = old.CreatedAt
	p.ListingState = old.ListingState
	p.CurrentVersionID = old.CurrentVersionID
	p.CreatorName = old.CreatorName
	p.CreatedByType = old.CreatedByType
	p.CreatedByBotUID = old.CreatedByBotUID
	p.CreatedByBotName = old.CreatedByBotName
	p.Rating = old.Rating
	if err = s.verifyVersionFiles(ctx, p, &model.PluginVersion{Package: p.Package, AttachmentKeys: p.AttachmentKeys}); err != nil {
		return nil, err
	}
	audit := s.audit(caller, id, "update", old, p, s.now())
	m := mutation(*p, rels, audit)
	m.Changelog = &params.Changelog
	store, ok := s.repo.(interface {
		SubmitDependencyUpdates(context.Context, pluginrepo.Scope, pluginrepo.DependencyUpdatePublishParams) (*pluginrepo.DependencyUpdatePublishResult, error)
	})
	if !ok {
		return nil, ErrInvalidRequest
	}
	out, err := store.SubmitDependencyUpdates(ctx, scope(caller), pluginrepo.DependencyUpdatePublishParams{Mutation: m, ExpectedPublishedVersionID: params.ExpectedPublishedVersionID, ExpectedDraftRevision: params.ExpectedDraftRevision, Targets: params.Targets, Version: version, Changelog: params.Changelog})
	if err != nil {
		return nil, mapStoreError(err)
	}
	if out.Review != nil && out.Review.Status == model.ReviewStatusPending {
		s.dispatchReviewCard(caller, out.Review, out.Plugin)
	}
	return out, nil
}

type dependencyUpdateStore interface {
	DependencyUpdates(context.Context, pluginrepo.Scope, string) (*model.DependencyUpdates, error)
	SkipDependencyUpdates(context.Context, pluginrepo.Scope, string, []model.DependencyUpdateTarget) error
}

func (s *Service) DependencyUpdates(ctx context.Context, caller Caller, id string) (*model.DependencyUpdates, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	id, err := parseStorageID(id)
	if err != nil {
		return nil, err
	}
	store, ok := s.repo.(dependencyUpdateStore)
	if !ok {
		return nil, ErrInvalidRequest
	}
	out, err := store.DependencyUpdates(ctx, scope(caller), id)
	return out, mapStoreError(err)
}
func (s *Service) SkipDependencyUpdates(ctx context.Context, caller Caller, id string, targets []model.DependencyUpdateTarget) error {
	if err := validateCaller(caller); err != nil {
		return err
	}
	id, err := parseStorageID(id)
	if err != nil {
		return err
	}
	store, ok := s.repo.(dependencyUpdateStore)
	if !ok {
		return ErrInvalidRequest
	}
	return mapStoreError(store.SkipDependencyUpdates(ctx, scope(caller), id, targets))
}
