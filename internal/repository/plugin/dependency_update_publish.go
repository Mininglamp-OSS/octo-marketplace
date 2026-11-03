package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type DependencyUpdatePublishParams struct {
	Mutation                   Mutation
	ExpectedPublishedVersionID string
	ExpectedDraftRevision      string
	Targets                    []model.DependencyUpdateTarget
	Version, Changelog         string
}
type DependencyUpdatePublishResult struct {
	Plugin *model.Plugin
	Review *model.PluginReviewRequest
}

func (r *Repo) SubmitDependencyUpdates(ctx context.Context, sc Scope, p DependencyUpdatePublishParams) (out *DependencyUpdatePublishResult, err error) {
	defer func() { err = classifyDeadlock(err) }()
	if p.ExpectedPublishedVersionID == "" || p.ExpectedDraftRevision == "" || !ValidUpdateTargets(p.Targets) {
		return nil, ErrConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids := []string{p.Mutation.Plugin.ID}
	for _, target := range p.Targets {
		ids = append(ids, target.PluginID)
	}
	sortStringsUnique(&ids)
	// Target locks include global readable dependencies, so lock by identity then
	// enforce access separately; only the root is ever written.
	for _, id := range ids {
		var found string
		if err = tx.QueryRowContext(ctx, `SELECT plugin_id FROM plugins WHERE plugin_id=? AND status=1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&found); err != nil {
			return nil, ErrNotFound
		}
	}
	root, err := getOwnedForUpdate(ctx, tx, sc, p.Mutation.Plugin.ID)
	if err != nil {
		return nil, err
	}
	if root.Type != model.PluginTypeExpert && root.Type != model.PluginTypeExpertTeam {
		return nil, ErrNotFound
	}
	if model.PluginDraftRevision(root) != p.ExpectedDraftRevision || root.Visibility != p.Mutation.Plugin.Visibility {
		return nil, ErrConflict
	}
	var published sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT published_version_id FROM plugins WHERE plugin_id=?`, root.ID).Scan(&published); err != nil {
		return nil, err
	}
	if published.String != p.ExpectedPublishedVersionID {
		return nil, ErrConflict
	}
	if root.IsEmbedded || root.ListingState != model.PluginListingStatePublished {
		return nil, ErrConflict
	}
	candidates, err := queryDependencyUpdates(ctx, tx, sc, []string{root.ID})
	if err != nil {
		return nil, err
	}
	allowed := candidateKeys(candidates[root.ID])
	acks := map[string]string{}
	for _, target := range p.Targets {
		if !allowed[target] {
			return nil, ErrConflict
		}
		matched := false
		for _, ref := range p.Mutation.Relations {
			if ref.TargetPluginID == target.PluginID && ref.Type == "expert_connector" && ref.TargetVersionID == nil && target.TargetConfigRevision != "" {
				matched = true
				acks[target.PluginID] = target.TargetConfigRevision
			}
			if ref.TargetPluginID == target.PluginID && ref.TargetVersionID != nil && *ref.TargetVersionID == target.TargetVersionID && ref.Type != "expert_connector" {
				matched = true
			}
		}
		if !matched {
			return nil, ErrInvalidRelation
		}
	}
	m := p.Mutation
	m.ExpectedDraftRevision = &p.ExpectedDraftRevision
	m.RefusePendingReview = true
	m.SnapshotVersion = true
	m.EnforceListingGate = true
	m.EnforceForwardOnlyVersion = true
	saved, err := r.updateInTx(ctx, tx, sc, m)
	if err != nil {
		return nil, err
	}
	current, err := getOwnedForUpdate(ctx, tx, sc, root.ID)
	if err != nil {
		return nil, err
	}
	if current.Visibility == model.PluginVisibilityPrivate {
		if current.CurrentVersionID == nil {
			return nil, ErrConflict
		}
		if err = ackConnectors(ctx, tx, root.ID, acks); err != nil {
			return nil, err
		}
		if err = r.recordRelease(ctx, tx, current, *current.CurrentVersionID, p.Version, m.OperatorID, r.now()); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE plugins SET listing_state='published',updated_at=? WHERE plugin_id=?`, r.now(), root.ID); err != nil {
			return nil, err
		}
		if err = ensureVisibleDefaultPlacement(ctx, tx, r.id, r.now(), root.ID, current.CategoryID); err != nil {
			return nil, err
		}
		current.ListingState = model.PluginListingStatePublished
		return &DependencyUpdatePublishResult{Plugin: current}, tx.Commit()
	}
	if current.Visibility != model.PluginVisibilitySpace {
		return nil, ErrNotFound
	}
	note := p.Changelog
	review := &model.PluginReviewRequest{PluginID: root.ID, SpaceID: sc.SpaceID, TargetScope: "space", Kind: model.ReviewKindUpgrade, Status: model.ReviewStatusPending, Version: p.Version, Changelog: &note, ApplicantUID: sc.CallerUID, ApplicantName: m.OperatorName}
	snap := FrozenSnapshot{Manifest: current.Manifest, Package: current.Package, AttachmentKeys: current.AttachmentKeys, Relations: saved.Relations, ManifestHash: current.ManifestHash, PluginHash: current.PluginHash, DeclaredContent: true}
	if err = r.insertReviewInTx(ctx, tx, sc, review, snap); err != nil {
		return nil, err
	}
	if len(acks) > 0 {
		raw, e := json.Marshal(acks)
		if e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO plugin_review_config_targets(review_id,selection_json) VALUES(?,?)", review.ID, raw); e != nil {
			return nil, e
		}
	}
	auto := true
	err = tx.QueryRowContext(ctx, `SELECT is_auto_approve_enabled FROM plugin_review_policies WHERE space_id=? FOR UPDATE`, sc.SpaceID).Scan(&auto)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if auto {
		rels, err := model.EncodeSnapshotRelations(saved.Relations)
		if err != nil {
			return nil, err
		}
		current, err = r.approveManagedReviewInTx(ctx, tx, sc, ApproveReviewParams{ReviewID: review.ID, ReviewerUID: m.OperatorID, ReviewerName: m.OperatorName, DecisionSource: model.ReviewDecisionSourcePolicy, RequestID: m.RequestID}, current, p.Version, sql.NullString{String: note, Valid: true}, current.Manifest, current.Package, current.AttachmentKeys, rels, current.ManifestHash, current.PluginHash)
		if err != nil {
			return nil, err
		}
		review.Status = model.ReviewStatusApproved
	}
	return &DependencyUpdatePublishResult{Plugin: current, Review: review}, tx.Commit()
}
