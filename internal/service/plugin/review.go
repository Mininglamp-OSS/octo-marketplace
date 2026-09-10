package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/notify"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
	"go.uber.org/zap"
)

// CardActionTypeReviewDecision is the action_type shared with octo-server. It
// must match the OCTO_CARD_ACTION_ROUTES entry that points at this service's
// /v1/card-actions/decide endpoint, or cards dispatch to nowhere.
const CardActionTypeReviewDecision = "marketplace.plugin_review.decision"

// SpaceRole values match octo-server's space_member.role encoding exactly.
// octo-web displays the inverse (1=owner, 2=admin); that mapping is the
// frontend's problem and never crosses this boundary.
const (
	SpaceRoleMember = 0
	SpaceRoleAdmin  = 1
	SpaceRoleOwner  = 2
)

// maxRejectReasonRunes bounds the operator-supplied reject reason. The column is
// TEXT; the bound exists so an applicant-visible field cannot be used as bulk
// storage.
const maxRejectReasonRunes = 1000

var (
	ErrReviewInvalid = errors.New("review request invalid")
	// ErrReviewForbidden is an authorization refusal, distinct from ErrNotFound:
	// the caller may know the resource exists (it is in their Space) but lacks the
	// reviewer role. writeServiceError maps it to 403.
	ErrReviewForbidden = errors.New("review operation not permitted")
	// ErrReviewPolicyForbidden is returned when an ordinary Space member tries
	// to change the Space-wide automatic-review policy. Owners and admins share
	// the same per-Space policy and may both update it.
	ErrReviewPolicyForbidden = errors.New("review policy operation requires a Space owner or admin")
	ErrReasonRequired        = errors.New("reject reason is required")
	// ErrReviewContentRequired is returned when an upgrade submission carries no
	// content. Freezing the live row for an already-listed plugin would make the
	// review theatre: the content is already visible org-wide, and approval would
	// only mint a version label for something that shipped days ago.
	ErrReviewContentRequired = errors.New("review submission must carry the reviewed content")
	// ErrListedRequiresReview is returned when a tenant tries to modify an
	// already-listed, org-visible plugin through the ordinary write path. It is a
	// STATE conflict, not a permission problem — the owner may change this plugin,
	// just through a review request. Self-delisting is no longer an escape hatch:
	// taking a listed plugin down is a Space-admin action.
	ErrListedRequiresReview = errors.New("a listed plugin may only be changed through review")
	// ErrVersionRegressed is returned when a write would move a plugin's version
	// label backwards. It names the field so the form can point at the input
	// rather than failing the whole body. Terminal on the IM card path.
	ErrVersionRegressed = errors.New("version must not go backwards")
	// ErrLabelTaken is returned when a request's version label is already in
	// the published set — either at insert or because a concurrent writer
	// published the same label while the request sat pending. Permanent on the
	// card path: the admin's click is answered as a terminal refusal rather than
	// being silently ack'd as conflict/pending on a still-pending request.
	ErrLabelTaken = errors.New("version label is already published")
	// ErrReviewPending is returned when a change cannot be applied while a review
	// request is open on the plugin. Content edits during a pending review are
	// deliberately fine — the reviewer acts on a frozen snapshot — so this covers
	// only changes that would contradict the pending request's own outcome, namely
	// switching visibility out from under an approval that is about to stamp it.
	ErrReviewPending = errors.New("a review request is pending on this plugin")
	// ErrReviewFieldConflict is returned when mutually exclusive submit fields are
	// sent together (e.g. both parse_task_id and manifest_json). It maps to
	// VALIDATION_ERROR with the conflicting field named.
	ErrReviewFieldConflict = errors.New("review submission fields conflict")
	// ErrReviewParseTaskType is returned when parse_task_id is supplied for a
	// non-skill plugin. Parse tasks exist only for skill zip uploads.
	ErrReviewParseTaskType = errors.New("parse_task_id is only valid for skill plugins")
	// ErrReviewNameMismatch is returned when a zip-submitted snapshot carries a
	// plugin_name that disagrees with the live row. The submission reviews
	// content, not identity; a rename must go through an upsert after delisting.
	ErrReviewNameMismatch = errors.New("zip package name does not match the plugin")
)

// ReviewFieldError is a validation error that names the offending field and
// reason, so writeServiceError can surface it in details instead of collapsing
// every cause into {"field":"body","reason":"invalid"}.
type ReviewFieldError struct {
	Field  string
	Reason string
	Err    error
}

func (e *ReviewFieldError) Error() string {
	if e.Err != nil {
		return e.Field + ": " + e.Err.Error()
	}
	return e.Field + ": " + e.Reason
}

func (e *ReviewFieldError) Unwrap() error { return e.Err }

// ReviewSubmitParams is the user-supplied input for a submit.
type ReviewSubmitParams struct {
	PluginID  string
	Version   string
	Changelog string
	// ParseTaskID, when set, materializes the reviewed package server-side from
	// a completed skill parse task (zip upload). It is mutually exclusive with
	// Manifest/Package.
	ParseTaskID string
	// Manifest and Package are the reviewed CONTENT, supplied together or not at
	// all. REQUIRED when the plugin is already listed (kind=upgrade) and optional
	// while it is a private draft (kind=first), where the plugin row is nobody
	// else's business and snapshotting it is honest.
	Manifest json.RawMessage
	Package  json.RawMessage
	// Relations, when non-nil, is the authoritative target state of the reviewed
	// relation graph, with the same semantics as /plugins/upsert: an empty list
	// means "no relations". A nil pointer means "not submitted" and inherits the
	// plugin's live graph, so a client that only edits documents cannot silently
	// empty an expert team by omitting the field.
	Relations *[]RelationRequest
}

// SubmitReview freezes the plugin's current draft content under the applicant's
// version label, then either approves it under the Space policy or queues it for
// manual review.
func (s *Service) SubmitReview(ctx context.Context, caller Caller, params ReviewSubmitParams) (*model.PluginReviewRequest, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	policy, err := s.repo.GetReviewPolicy(ctx, scope(caller))
	if err != nil {
		return nil, mapStoreError(err)
	}
	stored, err := s.submitReview(ctx, caller, params, !policy.IsAutoApproveEnabled)
	if err != nil || !policy.IsAutoApproveEnabled {
		return stored, err
	}
	published, err := s.autoApproveReview(ctx, caller, stored, params.ParseTaskID)
	if err != nil {
		return nil, err
	}
	// Prefer the committed request projection, which carries the repository's
	// decision timestamp and joined plugin state. A transient post-commit read must
	// not turn a successful approval into an error, so fall back to a consistent
	// projection built from the plugin returned by the approve transaction.
	approved, readErr := s.repo.GetReviewRequest(ctx, scope(caller), stored.ID, false)
	if readErr == nil {
		s.decorateReview(ctx, approved)
		return approved, nil
	}
	now := s.now()
	source := model.ReviewDecisionSourcePolicy
	stored.Status = model.ReviewStatusApproved
	stored.ReviewerUID = &caller.UID
	stored.ReviewerName = &caller.Name
	stored.DecisionSource = &source
	stored.ReviewedAt = &now
	if published != nil {
		stored.CurrentVersion = published.CurrentVersion
		stored.PluginListingState = published.ListingState
	}
	return stored, nil
}

func (s *Service) submitReview(ctx context.Context, caller Caller, params ReviewSubmitParams, dispatchCard bool) (*model.PluginReviewRequest, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	params.PluginID = strings.TrimSpace(params.PluginID)
	params.Version = strings.TrimSpace(params.Version)
	params.Changelog = strings.TrimSpace(params.Changelog)
	params.ParseTaskID = strings.TrimSpace(params.ParseTaskID)
	if params.PluginID == "" || !validVersion(params.Version) {
		return nil, &ReviewFieldError{Field: "version", Reason: "invalid"}
	}
	if utf8.RuneCountInString(params.Changelog) > maxRejectReasonRunes {
		return nil, &ReviewFieldError{Field: "changelog", Reason: "too_long"}
	}
	hasManifest := len(bytes.TrimSpace(params.Manifest)) > 0
	hasPackage := len(bytes.TrimSpace(params.Package)) > 0
	if hasManifest != hasPackage {
		return nil, &ReviewFieldError{Field: "manifest_json", Reason: "manifest_and_package_required_together"}
	}
	hasParseTask := params.ParseTaskID != ""
	if hasManifest && hasParseTask {
		// parse_task_id and manifest_json/package_json are mutually exclusive: the
		// browser picks one path (zip upload vs declared JSON) and must not send
		// both silently.
		return nil, &ReviewFieldError{Field: "parse_task_id", Reason: "mutually_exclusive_with_manifest_json", Err: ErrReviewFieldConflict}
	}
	sc := scope(caller)
	// includeRelations: the membership graph of an expert/expert_team is part of
	// what is being reviewed and must be frozen with the documents.
	detail, err := s.Detail(ctx, caller, params.PluginID, true)
	if err != nil {
		return nil, err
	}
	if detail.Plugin.OwnerUID != caller.UID {
		return nil, ErrNotFound
	}
	// A submission cannot carry a label older than what the plugin already shows.
	// publishedVersionLabels separately refuses REUSING a label the org has seen,
	// so together they force an upgrade strictly forward.
	if detail.Plugin.CurrentVersion != nil &&
		!versionNotRegressed(*detail.Plugin.CurrentVersion, params.Version) {
		return nil, ErrVersionRegressed
	}
	// An embedded child versions with its container and is never independently
	// reviewable, matching Update/Delete.
	if detail.Plugin.IsEmbedded {
		return nil, ErrNotFound
	}
	// Review exists to gate ORG exposure, and `space` is the only declared
	// visibility that asks for it: a private plugin is readable by nobody but its
	// author, so there is nothing for a reviewer to admit to the org. Approving
	// such a request cannot list it either — ApproveReview derives isFirst from the
	// row's own state and would either no-op a published+private row or, on a
	// draft, stamp `space` against an author who never asked for org visibility.
	// Publish already routes `private` to the immediate branch and only `space` to
	// here; this refuses the direct SubmitReview endpoint the same way so the
	// invariant does not depend on the caller. Not ErrNotFound — the plugin is
	// visible to its owner, this is a state conflict on that plugin.
	if detail.Plugin.Visibility != model.PluginVisibilitySpace {
		return nil, ErrInvalidRequest
	}
	if hasParseTask && detail.Plugin.Type != model.PluginTypeSkill {
		// Parse tasks exist only for skill zip uploads; connectors and experts
		// carry declared JSON through manifest_json/plugin_json.
		return nil, &ReviewFieldError{Field: "parse_task_id", Reason: "only_valid_for_skill_plugins", Err: ErrReviewParseTaskType}
	}

	// For a parse-task submission, consume the task BEFORE materializing (optimistic
	// lock, same as Import) so two concurrent submits cannot both proceed. The
	// materialization downloads the zip and uploads spilled objects; if anything
	// after that fails, the task is released (best-effort) so it remains retryable.
	// We bind to the plugin ID because MarkParseTaskConsumed uses skill_id as part
	// of its CAS: initial uploads have skill_id="" (unbound), but for an UPGRADE of
	// an existing listed plugin the import path would have already consumed the task
	// against a prior plugin row. For review, the task is produced by the "发布新版本"
	// UI flow which uploads a fresh zip without binding it to any skill_id (the
	// binding happens at approve time, not submit), so we pass "" for skill_id to
	// match how a fresh import does it.
	var parseTask *skillrepo.ParseTaskRow
	var uploadedKeys []string
	var consumedTask bool
	if hasParseTask {
		if s.parseTasks == nil || s.storage == nil {
			return nil, errors.New("plugin import is not configured")
		}
		parseTask, err = s.parseTasks.GetParseTask(ctx, params.ParseTaskID)
		if err != nil {
			return nil, fmt.Errorf("load parse task: %w", err)
		}
		// Ownership, Space, completion, and binding checks collapse to one error so
		// a caller cannot probe foreign tasks. Same guard as Import.
		if parseTask == nil || parseTask.OwnerID != caller.UID || parseTask.SpaceID != caller.SpaceID || parseTask.Status != "success" || parseTask.SkillID != "" {
			return nil, &ReviewFieldError{Field: "parse_task_id", Reason: "invalid_or_consumed", Err: ErrInvalidParseTask}
		}
		// Consume first: the optimistic status flip prevents duplicate submission.
		if err := s.parseTasks.MarkParseTaskConsumed(ctx, parseTask.ID, caller.UID, caller.SpaceID, ""); err != nil {
			if errors.Is(err, skillrepo.ErrParseTaskAlreadyConsumed) {
				return nil, &ReviewFieldError{Field: "parse_task_id", Reason: "already_consumed", Err: ErrInvalidParseTask}
			}
			return nil, fmt.Errorf("consume parse task: %w", err)
		}
		consumedTask = true
	}
	// Materialize/build snapshot. If this fails and we consumed a parse task,
	// release it.
	snap, err := s.freezeSubmission(ctx, caller, detail, params, parseTask, &uploadedKeys)
	if err != nil {
		if consumedTask {
			_ = s.parseTasks.ReleaseConsumedParseTask(context.WithoutCancel(ctx), parseTask.ID)
		}
		s.deleteObjects(ctx, uploadedKeys...)
		return nil, err
	}
	now := s.now()
	req := &model.PluginReviewRequest{
		PluginID:      params.PluginID,
		SpaceID:       sc.SpaceID,
		TargetScope:   "space",
		Status:        model.ReviewStatusPending,
		Version:       params.Version,
		ApplicantUID:  caller.UID,
		ApplicantName: caller.Name,
		SubmittedAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if params.Changelog != "" {
		req.Changelog = &params.Changelog
	}
	if err := s.repo.InsertReviewRequest(ctx, sc, req, snap); err != nil {
		// Insert failed — roll back uploaded objects and release the parse task so
		// the submission is retryable.
		s.deleteObjects(ctx, uploadedKeys...)
		if consumedTask {
			_ = s.parseTasks.ReleaseConsumedParseTask(context.WithoutCancel(ctx), parseTask.ID)
		}
		return nil, mapStoreError(err)
	}
	// InsertReviewRequest assigns ID/kind/timestamps on req. Build the remaining
	// response projection from the frozen snapshot and the already-authorized
	// plugin detail instead of doing a fallible post-commit read. In auto mode a
	// read failure here previously returned before approval or compensation,
	// stranding an unnotified pending request in the single-pending slot.
	req.ManifestHash = snap.ManifestHash
	req.PluginHash = snap.PluginHash
	req.PluginName = detail.Plugin.Name
	req.PluginType = detail.Plugin.Type
	req.PluginIcon = detail.Plugin.Icon
	req.CurrentVersion = detail.Plugin.CurrentVersion
	req.PluginListingState = detail.Plugin.ListingState
	s.decorateReview(ctx, req)
	if dispatchCard {
		s.dispatchReviewCard(caller, req, detail.Plugin)
	}
	return req, nil
}

// autoApproveReview applies an auto-policy decision and compensates if the
// second transaction fails. InsertReviewRequest and ApproveReview are separate
// repository operations, so without this cancellation a transient approve
// failure leaves a pending request for which no approval card was dispatched.
// Canceling restores the single-pending slot and cleans up snapshot-only objects,
// allowing the caller to retry the original operation.
func (s *Service) autoApproveReview(ctx context.Context, caller Caller, review *model.PluginReviewRequest, parseTaskID string) (*model.Plugin, error) {
	published, approveErr := s.repo.ApproveReview(ctx, scope(caller), pluginrepo.ApproveReviewParams{
		ReviewID: review.ID, ReviewerUID: caller.UID, ReviewerName: caller.Name,
		DecisionSource: model.ReviewDecisionSourcePolicy, RequestID: caller.RequestID,
	})
	if approveErr == nil {
		return published, nil
	}

	compensationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	frozenKeys, retained, cancelErr := s.repo.CancelReview(compensationCtx, scope(caller), review.ID, caller.UID)
	if cancelErr != nil {
		// Compensation lost a decision race or the store is unavailable. Dispatch a
		// card so a request that may still be pending is never left unnotified.
		s.dispatchReviewCard(caller, review, nil)
		return nil, fmt.Errorf("auto-approve review: %w (cancel compensation: %v)", mapStoreError(approveErr), mapStoreError(cancelErr))
	}
	// CancelReview returns the frozen and retained sidecars from its transaction;
	// use the normal cancel/reject GC so shared and version-owned objects survive.
	// Cleanup MUST finish before the consumed parse task becomes retryable. Object
	// keys are content-addressed: a concurrent retry could otherwise observe and
	// reuse a key that this failed attempt is about to delete.
	s.cleanupOrphanedReviewObjects(compensationCtx, frozenKeys, retained)
	// Unlike a user cancellation, this rollback represents a failed submit. A
	// consumed parse task therefore becomes retryable after its orphaned objects
	// have been removed.
	parseTaskID = strings.TrimSpace(parseTaskID)
	if parseTaskID != "" && s.parseTasks != nil {
		if releaseErr := s.parseTasks.ReleaseConsumedParseTask(compensationCtx, parseTaskID); releaseErr != nil {
			logging.Warn("review: failed to release parse task after auto-approve rollback",
				zap.String("review_id", review.ID),
				zap.String("parse_task_id", parseTaskID),
				zap.Error(releaseErr))
		}
	}
	return nil, mapStoreError(approveErr)
}

// freezeSubmission builds the frozen snapshot a reviewer will decide on.
//
// For an already-listed plugin the submission MUST carry its own content.
// Snapshotting the plugin row instead would make the review theatre: that row is
// what the org is already reading, so approval would only mint a version label
// for content that shipped the moment the author saved it. (The complementary
// half of this rule lives in Service.update, which refuses to let a tenant touch
// a listed row at all.) While the plugin is a private draft the row is nobody
// else's business, so snapshotting it is honest and content stays optional.
//
// Content arrives one of three ways, enforced as mutually exclusive by the
// caller:
//
//   - parseTask != nil: server-side materialization from a completed skill zip
//     parse task. Used by the browser "发布新版本" flow for skills, where the
//     canonical package is built by expanding the uploaded zip (binary/oversize
//     files spilled to content-addressed keys in this Space's managed prefix).
//     Returns freshly uploaded object keys in *uploaded so callers can roll back.
//   - hasManifest: declared JSON (manifest_json + plugin_json together). Used by
//     connectors, experts, expert teams, and by a skill author who edits content
//     without re-uploading a zip.
//   - neither: snapshot the live draft row (private first-submission only).
//
// Submitted content goes through the SAME canonicalization an ordinary write
// uses, so a malformed manifest is a 400 at submit rather than a surprise at
// approve time. Declared-JSON content is validated against the plugin's existing
// name/type/tags: this endpoint reviews CONTENT, not market metadata. For a
// zip-materialized snapshot, the rewritten package's display name is forced to
// match the live row (same rule as the reupload path) so CanonicalizeManifest
// agrees and the snapshot is internally coherent — a zip carrying a different
// name is rejected explicitly rather than failing at approve time.
func (s *Service) freezeSubmission(ctx context.Context, caller Caller, detail *Detail, params ReviewSubmitParams, parseTask *skillrepo.ParseTaskRow, uploaded *[]string) (pluginrepo.FrozenSnapshot, error) {
	plugin := detail.Plugin
	hasManifest := len(bytes.TrimSpace(params.Manifest)) > 0
	hasPackage := len(bytes.TrimSpace(params.Package)) > 0

	// Zip-materialized path.
	if parseTask != nil {
		return s.freezeSubmissionFromParseTask(ctx, caller, detail, params, parseTask, uploaded)
	}

	if hasManifest != hasPackage {
		// Half a document set cannot be reviewed, and silently filling the other half
		// from the live row would reintroduce exactly the no-op above.
		return pluginrepo.FrozenSnapshot{}, &ReviewFieldError{Field: "manifest_json", Reason: "manifest_and_package_required_together"}
	}
	// A contentless submit snapshots the plugin row as it stands. That is honest
	// for a plugin that is not yet listed — the draft row IS what the reviewer
	// should see — but for an already-listed plugin it would freeze the LIVE
	// content and produce an approval that changes nothing.
	if !hasManifest && plugin.ListingState == model.PluginListingStatePublished {
		return pluginrepo.FrozenSnapshot{}, ErrReviewContentRequired
	}

	snap := pluginrepo.FrozenSnapshot{
		Manifest:       cloneJSON(plugin.Manifest),
		Package:        cloneJSON(plugin.Package),
		AttachmentKeys: cloneJSON(plugin.AttachmentKeys),
		Relations:      detail.Relations,
		ManifestHash:   plugin.ManifestHash,
		PluginHash:     plugin.PluginHash,
	}
	if hasManifest {
		// A fetch-edit-save client echoes back the GET package, whose storage
		// attachments no longer carry an inline key (it lives in the host sidecar).
		// Re-inject the stored key for unchanged storage content so the round trip is
		// not rejected, exactly as Service.update does.
		pkg := reinjectUpdateStorageKeys(params.Package, plugin.Package, plugin.AttachmentKeys)
		docs, err := CanonicalizeDocuments(plugin.Name, plugin.Type, plugin.Tags, params.Manifest, pkg, caller.SpaceID)
		if err != nil {
			// CanonicalizeDocuments errors are all ErrInvalidRequest; surface as
			// manifest_json so the client knows which field to fix.
			return pluginrepo.FrozenSnapshot{}, &ReviewFieldError{Field: "manifest_json", Reason: "invalid", Err: err}
		}
		// Declared-JSON submissions may not introduce new storage attachments: the
		// client cannot mint storage content through a raw upsert (same rule as
		// /plugins/upsert), and allowing it here would bypass the object-lifecycle
		// ownership the import path provides. A sidecar change is rejected the same
		// way Service.update rejects it for listed plugins.
		if !sameAttachmentSidecar(docs.AttachmentKeys, plugin.AttachmentKeys) {
			return pluginrepo.FrozenSnapshot{}, &ReviewFieldError{Field: "plugin_json", Reason: "storage_attachment_change_requires_zip_upload"}
		}
		snap.Manifest, snap.Package, snap.AttachmentKeys = docs.Manifest, docs.Package, docs.AttachmentKeys
		snap.ManifestHash, snap.PluginHash = docs.ManifestHash, docs.PluginHash
		// The submission carries its own content, so the repository's under-lock
		// re-check of the content-required rule must let it through even if a
		// concurrent approval turned the row published in the meantime.
		snap.DeclaredContent = true
	}
	if params.Relations != nil {
		rels, err := s.buildRelations(ctx, caller, false, plugin, *params.Relations, s.now())
		if err != nil {
			return pluginrepo.FrozenSnapshot{}, err
		}
		snap.Relations = rels
	}
	return snap, nil
}

// freezeSubmissionFromParseTask materializes a frozen snapshot from a completed
// skill parse task by downloading the verified zip, rewriting it under the
// existing plugin identity, expanding into the flat attachment tree, and
// canonicalizing through the same machinery the import path uses.
func (s *Service) freezeSubmissionFromParseTask(ctx context.Context, caller Caller, detail *Detail, params ReviewSubmitParams, task *skillrepo.ParseTaskRow, uploaded *[]string) (pluginrepo.FrozenSnapshot, error) {
	plugin := detail.Plugin

	// Reuse buildImportedSkillWrite for the heavy lifting: it verifies the zip
	// (size + SHA-256), rewrites frontmatter, spills binaries to content-addressed
	// keys in this Space's managed prefix (with safeObjectSegment check and path
	// normalization enforced by buildSkillAttachmentTree / normalizedArchivePath),
	// and returns the WriteRequest plus freshly-uploaded keys for rollback.
	//
	// We build importFields that pin identity to the existing plugin row, matching
	// how a package-only reupload preserves the row's name/description/category
	// rather than resetting them to whatever the zip declares: this submission
	// reviews CONTENT, not market metadata. The zip's own name is verified to
	// AGREE with the existing manifest name below (CanonicalizeManifest enforces
	// it), so a renamed zip is rejected rather than silently applied.
	desc := ""
	if d := manifestDescription(plugin.Manifest); d != "" {
		desc = d
	}
	f := &importFields{
		pluginName:       plugin.Name,
		name:             manifestName(plugin.Manifest),
		description:      desc,
		version:          params.Version,
		versionSubmitted: true, // the review submit carries an explicit label
		tags:             decodeTagsJSON(plugin.Tags),
		visibility:       plugin.Visibility, // ignored: review never changes visibility directly
		categoryID:       plugin.CategoryID,
		icon:             plugin.Icon,
	}
	// If the zip declares a different machine name than the live row, fail
	// explicitly. CanonicalizeManifest below would also catch this (it checks
	// manifest.plugin_name against plugin.Name), but surfacing it here gives a
	// clearer error than a generic "manifest invalid".
	zipName := strings.TrimSpace(task.ResultName)
	if zipName != "" && zipName != f.name {
		return pluginrepo.FrozenSnapshot{}, &ReviewFieldError{Field: "parse_task_id", Reason: "name_mismatch", Err: ErrReviewNameMismatch}
	}

	req, _, uploadedKeys, err := s.buildImportedSkillWrite(ctx, caller.SpaceID, plugin.ID, task, f, true)
	if err != nil {
		return pluginrepo.FrozenSnapshot{}, err
	}
	*uploaded = append(*uploaded, uploadedKeys...)

	// buildImportedSkillWrite built a WriteRequest with its own canonicalized
	// manifest/package/sidecar using the plugin name/type/tags we passed in.
	// Verify the result goes through our own CanonicalizeDocuments path too (belt
	// and suspenders — buildImportWriteRequest already calls CanonicalizeManifest
	// but buildImportedSkillWrite also uses splitStorageKeys through buildWrite's
	// CanonicalizeDocuments in the Create/Update flow; here we only built the
	// WriteRequest).
	docs, err := CanonicalizeDocuments(plugin.Name, plugin.Type, req.Tags, req.Manifest, req.Package, caller.SpaceID)
	if err != nil {
		s.deleteObjects(ctx, uploadedKeys...)
		*uploaded = (*uploaded)[:0]
		return pluginrepo.FrozenSnapshot{}, &ReviewFieldError{Field: "parse_task_id", Reason: "invalid_package", Err: err}
	}

	// Relations: if the submit body included relations, use them; otherwise
	// inherit the live graph (same semantics as the declared-JSON path).
	rels := detail.Relations
	if params.Relations != nil {
		built, err := s.buildRelations(ctx, caller, false, plugin, *params.Relations, s.now())
		if err != nil {
			s.deleteObjects(ctx, uploadedKeys...)
			*uploaded = (*uploaded)[:0]
			return pluginrepo.FrozenSnapshot{}, err
		}
		rels = built
	}

	return pluginrepo.FrozenSnapshot{
		Manifest:       docs.Manifest,
		Package:        docs.Package,
		AttachmentKeys: docs.AttachmentKeys,
		Relations:      rels,
		ManifestHash:   docs.ManifestHash,
		PluginHash:     docs.PluginHash,
		// A materialized zip is declared content by definition.
		DeclaredContent: true,
	}, nil
}

// sameAttachmentSidecar reports whether two storage sidecars hold the same
// path -> object-key map, tolerant of nil/empty/key ordering.
func sameAttachmentSidecar(a, b json.RawMessage) bool {
	ma, mb := attachmentKeyMap(a), attachmentKeyMap(b)
	if len(ma) != len(mb) {
		return false
	}
	for path, key := range ma {
		if other, ok := mb[path]; !ok || other != key {
			return false
		}
	}
	return true
}

// dispatchReviewCard queues the IM approval card. Everything it does — including
// building the payload — happens inside the post-commit best-effort hook, on a
// detached context: the request is already committed, so nothing here may fail
// the submit, and nothing here may add a round trip to the response path. An
// earlier design fetched the Space-admin roster synchronously before responding,
// which put an octo-server call (3s timeout) in front of every submit and lost
// the card entirely when the client disconnected right after commit.
//
// TODO(metrics pending): the brief asks for counters on top of these logs —
// review_card_dispatch_delivered_total / _filtered_total / _errors_total here,
// plus card-action decision (applied|forbidden|conflict|not_found) and
// signature/timestamp/auth-failure counters in the card_action handler. There is
// no existing counter framework in the notify surface: internal/repository/metrics
// is a MySQL table of per-resource business counters, and the module pulls in no
// Prometheus/OTel/expvar dependency, so wiring one (plus its exposition endpoint
// and scrape story) is a change of its own. Every outcome a counter would record
// is logged below in the meantime. See divergence item 27 in
// .octospec/tasks/plugin-space-review/brief.md.
func (s *Service) dispatchReviewCard(caller Caller, stored *model.PluginReviewRequest, plugin *model.Plugin) {
	if s.notify == nil || !s.notify.Enabled() || s.bestEffort == nil || stored == nil {
		return
	}
	pluginName := stored.PluginName
	if pluginName == "" && plugin != nil {
		pluginName = plugin.Name
	}
	var currentVersion string
	if stored.CurrentVersion != nil {
		currentVersion = *stored.CurrentVersion
	}
	var changelog string
	if stored.Changelog != nil {
		changelog = *stored.Changelog
	}
	req := notify.NotifyRequest{
		SpaceID:  stored.SpaceID,
		Service:  "marketplace",
		ActorUID: caller.UID,
		ApprovalCard: &notify.ApprovalCard{
			ActionType:  CardActionTypeReviewDecision,
			Title:       notify.CardTitle(pluginName),
			Description: notify.CardDescription(stored.PluginType, caller.Name, stored.Version, currentVersion, stored.Kind, changelog),
			Data:        map[string]string{"review_id": stored.ID, "plugin_id": stored.PluginID},
		},
	}
	reviewID := stored.ID
	n := s.notify
	s.bestEffort("review.submit", func(ctx context.Context) error {
		resp, err := n.NotifySpaceAdmins(ctx, req)
		if err != nil {
			return err
		}
		// HTTP 200 does not mean every admin received the card. With target_role
		// the caller never learns the roster, so `delivered` is the only delivery
		// record there is, and an empty one means the Space has no active human
		// admin — a legitimate state, but one an operator needs to see.
		if len(resp.Delivered) == 0 {
			logging.Warn("review: approval card reached no admin",
				zap.String("review_id", reviewID),
				zap.String("space_id", req.SpaceID),
				zap.Int("filtered", len(resp.Filtered)))
		} else {
			logging.Info("review: approval card dispatched",
				zap.String("review_id", reviewID),
				zap.Int("delivered", len(resp.Delivered)),
				zap.Int("filtered", len(resp.Filtered)))
		}
		for uid, reason := range resp.Filtered {
			logging.Warn("review: approval card filtered for target",
				zap.String("review_id", reviewID),
				zap.String("target_uid", uid),
				zap.String("reason", reason))
		}
		return nil
	})
}

func (s *Service) ListReviews(ctx context.Context, caller Caller, mode string, status model.ReviewStatus, page, pageSize int) ([]*model.PluginReviewRequest, int64, error) {
	if err := validateCaller(caller); err != nil {
		return nil, 0, err
	}
	if pageSize <= 0 || pageSize > maxListLimit {
		pageSize = 20
	}
	if page < 1 {
		page = 1
	}
	sc := scope(caller)
	f := pluginrepo.ReviewListFilter{
		SpaceID: sc.SpaceID,
		Status:  status,
		Limit:   pageSize,
		Offset:  (page - 1) * pageSize,
	}
	switch mode {
	case "mine":
		f.ApplicantUID = caller.UID
	case "space":
		if !s.isReviewer(caller) {
			return nil, 0, ErrReviewForbidden
		}
	default:
		return nil, 0, ErrReviewInvalid
	}
	items, total, err := s.repo.ListReviewRequests(ctx, sc, f)
	if err != nil {
		return nil, 0, mapStoreError(err)
	}
	for _, item := range items {
		s.decorateReview(ctx, item)
	}
	return items, total, nil
}

func (s *Service) GetReview(ctx context.Context, caller Caller, reviewID string) (*model.PluginReviewRequest, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	reviewID = strings.TrimSpace(reviewID)
	if reviewID == "" {
		return nil, ErrReviewInvalid
	}
	sc := scope(caller)
	rr, err := s.repo.LoadReviewSnapshot(ctx, sc, reviewID, s.isReviewer(caller))
	if err != nil {
		return nil, mapStoreError(err)
	}
	s.decorateReview(ctx, rr)
	// The reviewer is deciding on the frozen text, not on whatever the live plugin
	// says now, so the preview is extracted from the snapshot.
	rr.ReadmeContent = frozenReadme(rr.PluginJSON, rr.ManifestJSON)
	return rr, nil
}

func (s *Service) ApproveReview(ctx context.Context, caller Caller, reviewID string) (*model.Plugin, error) {
	if err := validateCaller(caller); err != nil {
		return nil, err
	}
	reviewID = strings.TrimSpace(reviewID)
	if reviewID == "" {
		return nil, ErrReviewInvalid
	}
	if !s.isReviewer(caller) {
		return nil, ErrReviewForbidden
	}
	plug, err := s.repo.ApproveReview(ctx, scope(caller), pluginrepo.ApproveReviewParams{
		ReviewID:       reviewID,
		ReviewerUID:    caller.UID,
		ReviewerName:   caller.Name,
		DecisionSource: model.ReviewDecisionSourceWeb,
		RequestID:      caller.RequestID,
	})
	if err != nil {
		return nil, mapStoreError(err)
	}
	plug.IconURL = s.resolveIcon(ctx, plug.Icon)
	return plug, nil
}

func (s *Service) RejectReview(ctx context.Context, caller Caller, reviewID, reason string) error {
	if err := validateCaller(caller); err != nil {
		return err
	}
	reviewID = strings.TrimSpace(reviewID)
	if reviewID == "" {
		return ErrReviewInvalid
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ErrReasonRequired
	}
	if utf8.RuneCountInString(reason) > maxRejectReasonRunes {
		return ErrReviewInvalid
	}
	if !s.isReviewer(caller) {
		return ErrReviewForbidden
	}
	frozenKeys, retained, err := s.repo.RejectReview(ctx, scope(caller), pluginrepo.RejectReviewParams{
		ReviewID:       reviewID,
		ReviewerUID:    caller.UID,
		ReviewerName:   caller.Name,
		Reason:         reason,
		DecisionSource: model.ReviewDecisionSourceWeb,
		RequestID:      caller.RequestID,
	})
	if err != nil {
		return mapStoreError(err)
	}
	// Clean up any storage objects the submission uploaded that the live row
	// does not reference. The commit has already succeeded; this is best-effort
	// GC on a detached context so a transient storage failure never rolls back
	// the rejection.
	s.cleanupOrphanedReviewObjects(context.WithoutCancel(ctx), frozenKeys, retained)
	return nil
}

// CancelReview withdraws the applicant's own pending request. It carries no role
// check: the repository predicate is applicant_uid, and a reviewer who wants the
// request gone rejects it (with a reason) rather than silently withdrawing it.
func (s *Service) CancelReview(ctx context.Context, caller Caller, reviewID string) error {
	if err := validateCaller(caller); err != nil {
		return err
	}
	reviewID = strings.TrimSpace(reviewID)
	if reviewID == "" {
		return ErrReviewInvalid
	}
	frozenKeys, retained, err := s.repo.CancelReview(ctx, scope(caller), reviewID, caller.UID)
	if err != nil {
		return mapStoreError(err)
	}
	// Same cleanup as reject: best-effort on detached context. A frozen key
	// that appears in the live sidecar is shared and kept; any other key was
	// uploaded for this submission alone and is now unreachable.
	s.cleanupOrphanedReviewObjects(context.WithoutCancel(ctx), frozenKeys, retained)
	// Parse task lifecycle: a task consumed at submit time STAYS consumed after
	// cancel (and after reject), even though the request did not result in an
	// approved change. This is the deliberate conservative choice:
	//
	//   1. MarkParseTaskConsumed is a CAS on status='success' that prevents the
	//      same zip from being submitted twice, even against two different
	//      plugins. Releasing it after cancel would let a rejected zip's bytes
	//      be re-submitted without a fresh upload, which means the author does
	//      not see the result of the edits they were asked to make and the
	//      reviewer has no guarantee the package they rejected is not the one
	//      that ships.
	//   2. The alternative (release on reject/cancel) makes the cancellation
	//      race with a second submission of the same task: a release is not a
	//      CAS against the request_id, so if the author quickly uploads a new
	//      zip and submits while the old rejection's cleanup is running, we
	//      could release the NEW task by mistake.
	//   3. Object storage cost for the unreferenced zip is bounded by the
	//      upload TTL (the parse-task cleanup in the skill service already
	//      garbage-collects file_url objects for consumed/failed tasks). The
	//      spilled attachment keys are cleaned up above.
	//
	// This matches the import path's behavior: a successfully-imported-then-
	// deleted plugin does not release its parse task either. The author always
	// uploads a fresh zip to retry.
	return nil
}

// cleanupOrphanedReviewObjects deletes object keys in the frozen sidecar that
// the live (post-decision) plugin row does NOT reference. Called after reject
// or cancel to clean up files spilled at submit time.
//
// `retained` is every key the plugin still references — its live sidecar AND the
// sidecar frozen onto each plugin_versions snapshot — collected by the repository
// inside the decision transaction. Both halves matter: approve writes the frozen
// sidecar into plugin_versions, so an older approved version owns keys the live
// row no longer mentions. Diffing against the live row alone deleted those, and
// object-storage deletes do not come back.
//
// Matching is on the KEY, not the path it was mounted at. A content-addressed key
// is the same object wherever it appears, and a retained key must survive even if
// some other version references it under a different path.
//
// This runs on a detached context and errors are silently ignored: a storage
// failure must not roll back an already-committed decision.
func (s *Service) cleanupOrphanedReviewObjects(ctx context.Context, frozenKeys json.RawMessage, retained map[string]struct{}) {
	frozen := attachmentKeyMap(frozenKeys)
	if len(frozen) == 0 {
		return
	}
	var orphaned []string
	for _, key := range frozen {
		if key == "" {
			continue
		}
		if _, kept := retained[key]; kept {
			continue
		}
		orphaned = append(orphaned, key)
	}
	if len(orphaned) > 0 {
		s.deleteObjects(ctx, orphaned...)
	}
}

// --- card protocol ------------------------------------------------------------

// CardActionResult is the typed response returned to octo-server for an IM
// decision. The field set and both enums are fixed by octo-server's
// DecodeDecisionResult (internal/cardactiondispatch/contract.go): it decodes with
// DisallowUnknownFields and rejects any value outside the enums, so this struct
// must stay exactly four fields.
type CardActionResult struct {
	Disposition string            `json:"disposition"`
	State       string            `json:"state"`
	Requester   string            `json:"requester_uid,omitempty"`
	Display     map[string]string `json:"display,omitempty"`
}

// Card-action protocol enums. These are octo-server's vocabulary, not ours.
const (
	cardDispositionApplied   = "applied"
	cardDispositionForbidden = "forbidden"
	cardDispositionConflict  = "conflict"
	cardDispositionNotFound  = "not_found"

	cardStatePending   = "pending"
	cardStateApproved  = "approved"
	cardStateDenied    = "denied"
	cardStateCancelled = "cancelled"
)

// cardState translates our review status into the card protocol's state enum.
// The vocabularies deliberately differ — we say rejected/canceled, the protocol
// says denied/cancelled — and emitting our spelling makes octo-server reject the
// whole response as an invalid enum, so never hand it string(status) directly.
func cardState(status model.ReviewStatus) string {
	switch status {
	case model.ReviewStatusApproved:
		return cardStateApproved
	case model.ReviewStatusRejected:
		return cardStateDenied
	case model.ReviewStatusCanceled:
		return cardStateCancelled
	default:
		return cardStatePending
	}
}

var (
	// ErrCardBadDecision is returned from DecideReviewFromCard when the decision
	// value is not one of the handled vocabulary ("approve"/"deny"). It is
	// distinct from ErrReviewInvalid (which covers missing fields / bad review_id)
	// because an unrecognized decision represents a permanent protocol drift —
	// a vocabulary mismatch between this service and octo-server — and must be
	// surfaced as a malformed payload (400 → DLQ) rather than acked as
	// "forbidden" (which would render every admin click as 无权限 and stop
	// retries silently).
	ErrCardBadDecision = errors.New("unrecognized card decision value")
)

// DecideReviewFromCard is the IM-callback entry: idempotency, operator
// re-verification, and the decision itself.
//
// Every value it RETURNS is a handled outcome the transport answers with 200. A
// returned ERROR means "transient, ask octo-server to retry" — the caller turns
// it into a 5xx. Keeping that distinction is the whole reliability story: a
// forbidden/conflict/not_found body is acked and never redelivered, so nothing
// that might succeed later may be reported that way.
//
// Missing/invalid review_id, empty event_id or operator_uid are treated as
// malformed payloads (ErrCardBadDecision) rather than authorization failures,
// because a well-formed card always carries these fields. A genuinely
// unrecognized decision value also returns ErrCardBadDecision: the caller maps
// it to 400 DLQ instead of 200 forbidden.
func (s *Service) DecideReviewFromCard(ctx context.Context, eventID, operatorUID, decision, reviewID string) (*CardActionResult, error) {
	eventID = strings.TrimSpace(eventID)
	operatorUID = strings.TrimSpace(operatorUID)
	reviewID = strings.TrimSpace(reviewID)
	decision = strings.TrimSpace(decision)
	// Field presence: a signed but missing review_id or operator_uid is a
	// malformed payload, not a permission problem.
	if eventID == "" || operatorUID == "" || reviewID == "" {
		return nil, ErrCardBadDecision
	}
	if decision != "approve" && decision != "deny" {
		// Unrecognized decision vocabulary: DLQ the event instead of silently
		// acking as "forbidden" so protocol drift is loud, not silent.
		return nil, ErrCardBadDecision
	}
	// Idempotency first: a redelivered event must replay the stored answer
	// verbatim rather than re-running the decision. Cross-check the receipt's
	// ReviewID against the signed path param so an event_id reused across two
	// reviews cannot silently render the wrong response.
	if existing, err := s.repo.GetCardActionReceipt(ctx, eventID); err != nil {
		return nil, err
	} else if existing != nil {
		if existing.ReviewID != reviewID {
			return nil, ErrCardBadDecision
		}
		var out CardActionResult
		if err := json.Unmarshal([]byte(existing.StoredResponse), &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	// Load the request cross-scope to learn its Space. There is no tenant context
	// on this path — the review_id arrived inside a signed card payload — so the
	// operator is authorized against the Space this lookup returns, below.
	req, err := s.repo.GetReviewRequestAnySpace(ctx, reviewID)
	if err != nil {
		// A vanished request is terminal, not a server fault: report it in-band so
		// octo-server renders the card and stops redelivering instead of pushing the
		// event to the DLQ. `cancelled` is the same state the two not_found branches
		// below emit, so one physical condition — the request is gone — renders as
		// one thing on a card keyed by state. Every path that removes the plugin
		// cascade-cancels its request in the same transaction
		// (cancelPendingReviewFor), so cancelled is also the accurate reading; an
		// id that never existed at all is indistinguishable from here and renders
		// the same way. No requester is knowable on this branch, and cancelled does
		// not require one (see the contract note on the applied result below).
		if errors.Is(mapStoreError(err), ErrNotFound) {
			return &CardActionResult{Disposition: cardDispositionNotFound, State: cardStateCancelled}, nil
		}
		return nil, mapStoreError(err)
	}
	// octo-server's operator_uid is an identity assertion, not an authorization
	// grant: an admin who lost the role between card delivery and the click must
	// not be able to decide. A LOOKUP FAILURE IS NOT A REFUSAL — collapsing "the
	// role service is down" into forbidden returns a 200 that octo-server acks and
	// never redelivers, silently discarding a real admin's click.
	role, err := s.operatorRole(ctx, req.SpaceID, operatorUID)
	if err != nil {
		return nil, err
	}
	if role == nil || *role < SpaceRoleAdmin {
		return &CardActionResult{Disposition: cardDispositionForbidden, State: cardStatePending}, nil
	}
	if req.Status != model.ReviewStatusPending {
		return s.cardConflict(ctx, req)
	}

	adminScope := pluginrepo.Scope{CallerUID: operatorUID, SpaceID: req.SpaceID}
	var state string
	var applyErr error
	if decision == "approve" {
		_, applyErr = s.repo.ApproveReview(ctx, adminScope, pluginrepo.ApproveReviewParams{
			ReviewID:    reviewID,
			ReviewerUID: operatorUID,
			// TODO(cross-repo): stamp the operator's DISPLAY NAME here once the
			// companion octo-server role-lookup returns it. MemberRole currently
			// yields only a role, so IM decisions record the raw operator UID as
			// reviewer_name while web decisions record a human name — the audit
			// trail is half-attributable until the server change lands.
			ReviewerName:   operatorUID,
			DecisionSource: model.ReviewDecisionSourceIM,
		})
		state = cardStateApproved
	} else {
		var frozenKeys json.RawMessage
		var retained map[string]struct{}
		frozenKeys, retained, applyErr = s.repo.RejectReview(ctx, adminScope, pluginrepo.RejectReviewParams{
			ReviewID:    reviewID,
			ReviewerUID: operatorUID,
			// TODO(cross-repo): same as the approve path above — record the
			// operator's display name once octo-server returns it.
			ReviewerName:   operatorUID,
			Reason:         model.DefaultIMDenyReason,
			DecisionSource: model.ReviewDecisionSourceIM,
		})
		if applyErr == nil {
			// The same GC the web reject runs. RejectReview already returns both
			// sidecars out of its own transaction, so wiring it here costs no extra
			// round trip — an earlier comment claimed it did and used that to justify
			// leaving IM denies leaking every object their submission spilled.
			s.cleanupOrphanedReviewObjects(context.WithoutCancel(ctx), frozenKeys, retained)
		}
		state = cardStateDenied
	}
	if applyErr != nil {
		// Losing the CAS race is an expected outcome, not an error: another admin
		// (or a web decision) already settled this request. Report the authoritative
		// terminal state so the card renders correctly, rather than returning 5xx
		// and having the event retried into the DLQ.
		//
		// ErrDeadlock is deliberately NOT in this terminal set. A deadlock-victim
		// abort is transient — the transaction rolled back and did NOT settle the
		// request — so treating it as a conflict would ack the event as handled and
		// silently discard a real admin's decision. It falls through to the fault
		// return below, which the handler answers with 503 so octo-server
		// redelivers.
		//
		// ErrVersionRegressed is permanent: the plugin's current_version moved past
		// the frozen label between card delivery and the click, and nothing can
		// lower it back. Stamping max(current, frozen) would publish content under
		// a label the reviewer never saw, which is wrong for a review workflow.
		// Ack the event as a handled refusal so octo-server stops redelivering; the
		// applicant can cancel+resubmit (CancelReview is applicant-scoped and
		// publishedVersionLabels excludes the draft's own label, so the request
		// stays settleable). We reuse disposition=forbidden (the terminal-refusal
		// slot shared with "operator is no longer an admin") because none of the
		// other four enum values fit: the request is NOT applied, NOT replayed
		// (this is the first delivery for this event_id), NOT a same-resource
		// conflict (the row is still pending; nobody else decided), and NOT
		// not_found. The rendering of "forbidden" as a generic refusal on the
		// octo-server side is imperfect but acceptable — the critical fix is
		// stopping the infinite 503 retry loop. Cross-repo: if octo-server adds a
		// dedicated "stale" disposition, switch to it; display.reason carries the
		// actionable message ("version moved past; applicant must resubmit") for a
		// future richer renderer.
		mapped := mapStoreError(applyErr)
		switch {
		case errors.Is(mapped, ErrNotFound):
			// A concurrent soft-delete (delete or delist winning the row lock between
			// the request FOR UPDATE and the approve/reject body) is PERMANENT — the
			// request is gone and no redelivery will change that. cardConflict would
			// try to re-read and re-return ErrNotFound; treat it as terminal here so
			// the re-read does not loop as 503.
			//
			// Deliberately UNRECEIPTED, like the two refusals below: the event is
			// answered in-band but nothing is stored under its event_id, so a
			// redelivery of the same event re-runs the decision. That is the right
			// trade for a transient-looking outcome (a retry that finds the row back
			// — restored, or a re-read that now succeeds — must be able to apply it)
			// and it is why the answer for one event_id is not guaranteed verbatim
			// across deliveries. Only the applied path is receipted, which is the
			// case where a differing replay would double-apply.
			return &CardActionResult{
				Disposition: cardDispositionNotFound,
				State:       cardStateCancelled,
				Requester:   req.ApplicantUID,
				Display:     map[string]string{"title": req.PluginName},
			}, nil
		case errors.Is(mapped, ErrConflict):
			return s.cardConflict(ctx, req)
		case errors.Is(mapped, ErrVersionRegressed), errors.Is(mapped, ErrLabelTaken):
			// Both are permanent on a still-pending row: the label is either behind
			// current (draft label moved) or already-taken (concurrent label
			// collision). Ack as a handled refusal; display.reason distinguishes the
			// two for a future richer renderer.
			//
			// Unreceipted on purpose. The row is still PENDING, so a later delivery
			// of this event must be free to apply the decision once the applicant has
			// resubmitted — storing a refusal under this event_id would swallow that
			// retry. The cost is that one event_id can answer `{forbidden, pending}`
			// now and `{conflict, cancelled}` after the applicant cancels; the answer
			// is not verbatim-idempotent, only never-double-applying. Receipting is
			// reserved for the applied path, the only one where a differing replay
			// would apply a decision twice.
			reason := "version_moved_past_resubmit_required"
			if errors.Is(mapped, ErrLabelTaken) {
				reason = "version_label_taken_resubmit_required"
			}
			return &CardActionResult{
				Disposition: cardDispositionForbidden,
				State:       cardStatePending,
				Requester:   req.ApplicantUID,
				Display: map[string]string{
					"title":  req.PluginName,
					"reason": reason,
				},
			}, nil
		}
		return nil, mapped
	}
	result := &CardActionResult{
		Disposition: cardDispositionApplied,
		State:       state,
		// approved/denied MUST carry requester_uid. Those two are what octo-server
		// calls a terminal decision, and both of its layers reject a response
		// without one: DecodeDecisionResult (cardactiondispatch/contract.go:228)
		// errors on `(approved|denied) && requester_uid == ""`, and
		// StandardActionFinalizer (modules/notify/standard_action_finalizer.go:34-36)
		// repeats the check before DMing the applicant. A rejected response reads as
		// a callback failure, which octo-server redelivers.
		//
		// `cancelled` is NOT in that set on either side — the finalizer edits the
		// card and returns before the applicant DM — so the not_found branches below
		// are accepted without a requester. They send one anyway: it costs nothing
		// and the field stays present if octo-server ever widens its terminal set.
		// Do not read this as "every terminal state requires it"; an earlier
		// revision of this comment said so and that is not what the companion
		// repository enforces.
		Requester: req.ApplicantUID,
		Display:   map[string]string{"title": req.PluginName},
	}
	body, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := s.repo.InsertCardActionReceipt(ctx, &model.CardActionReceipt{
		EventID:        eventID,
		ReviewID:       reviewID,
		Decision:       decision,
		StoredResponse: string(body),
		CreatedAt:      s.now(),
	}); err != nil {
		// Two deliveries of the SAME event raced past the read above; the winner's
		// stored response is authoritative.
		if errors.Is(err, pluginrepo.ErrConflict) {
			if existing, _ := s.repo.GetCardActionReceipt(ctx, eventID); existing != nil {
				var out CardActionResult
				if json.Unmarshal([]byte(existing.StoredResponse), &out) == nil {
					return &out, nil
				}
			}
		}
		return nil, err
	}
	return result, nil
}

// cardConflict reports the authoritative terminal state of an already-decided
// request, re-reading it so the answer reflects the winner's outcome rather than
// the stale row this callback started from.
//
// A RE-READ FAILURE IS NOT A CONFLICT. At the call sites `req` was proven
// pending moments ago (either by the pre-apply status gate or by the CAS-race
// branch), so falling back to it produces disposition=conflict/state=pending —
// self-contradictory, since conflict means already settled — on a card octo-server
// will never redeliver. Return the error so the handler's 503 branch triggers
// redelivery, matching the same invariant enforced for the role lookup at :857-864
// above.
func (s *Service) cardConflict(ctx context.Context, req *model.PluginReviewRequest) (*CardActionResult, error) {
	cur, err := s.repo.GetReviewRequestAnySpace(ctx, req.ID)
	if errors.Is(err, pluginrepo.ErrNotFound) {
		// The request (or its plugin) was soft-deleted between the CAS race and
		// this re-read. Permanent: return not_found/cancelled as a terminal
		// outcome instead of letting the error fall through to 503-redelivery,
		// which would loop forever on a row that cannot come back. Same state the
		// other two request-is-gone branches emit, and `req` is still in hand from
		// the caller, so the requester and title come along.
		return &CardActionResult{
			Disposition: cardDispositionNotFound,
			State:       cardStateCancelled,
			Requester:   req.ApplicantUID,
			Display:     map[string]string{"title": req.PluginName},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	settled := req
	if cur != nil {
		settled = cur
	}
	return &CardActionResult{
		Disposition: cardDispositionConflict,
		State:       cardState(settled.Status),
		Requester:   settled.ApplicantUID,
		Display:     map[string]string{"title": settled.PluginName},
	}, nil
}

// operatorRole re-checks a role for the IM card-action path, which carries an
// asserted operator_uid instead of a resolved Caller. It returns (nil, nil) for
// "not a member" and an error for "could not find out" — the caller must keep
// those apart.
func (s *Service) operatorRole(ctx context.Context, spaceID, uid string) (*int, error) {
	if s.notify == nil || !s.notify.Enabled() {
		// Not a refusal: a deployment that mounted the callback without the internal
		// token cannot authorize anyone, and answering "forbidden" would burn the
		// event. Surface it as a fault so the operator sees it and a redelivery
		// after the fix still lands.
		return nil, errors.New("review: space role lookup is not configured")
	}
	return s.notify.MemberRole(ctx, spaceID, uid)
}

// --- helpers ------------------------------------------------------------------

// isReviewer reports whether the caller may act on the Space review queue.
//
// It reads Caller.SpaceRole, which the HTTP layer populates from the verified
// octo-server identity — never from request JSON, and deliberately never through
// the IM notifier: an earlier design resolved the web role through the
// notification wiring, so nobody could approve anything unless IM notification
// happened to be configured.
//
// adminCaller (internal/service/plugin/admin.go) builds a Caller with SpaceID=""
// and IsSystemAdmin=true for the cross-Space admin surface. That surface does not
// route here, but the system-admin short-circuit comes first so such a Caller can
// never be demoted to member by the empty-Space comparison.
func (s *Service) isReviewer(caller Caller) bool {
	return caller.IsSystemAdmin || caller.SpaceRole >= SpaceRoleAdmin
}

// decorateReview turns storage-side values into display values: the icon column
// holds an object key for uploaded icons, which is a 404 if handed to a browser.
// Resolved through the same path the plugin list uses.
func (s *Service) decorateReview(ctx context.Context, rr *model.PluginReviewRequest) {
	if rr == nil {
		return
	}
	rr.PluginIcon = s.resolveIcon(ctx, rr.PluginIcon)
}

// frozenReadmePaths are the primary documents, most specific first. A skill
// package carries SKILL.md; the other types have no mandated entry document, so
// the common conventions are tried before falling back to the manifest.
var frozenReadmePaths = []string{"SKILL.md", "README.md", "AGENTS.md"}

// frozenReadme extracts the reviewable body from the FROZEN package snapshot.
// Storage-backed attachments are not fetched: this runs on a read request, the
// object may be large, and the inline entry document is what a reviewer needs.
// Falling back to the manifest description keeps the field populated for
// connectors, which carry no markdown at all.
func frozenReadme(pkg, manifest json.RawMessage) string {
	for _, path := range frozenReadmePaths {
		if raw, ok := rawAttachmentContent(pkg, path); ok && strings.TrimSpace(raw) != "" {
			return raw
		}
	}
	return manifestDescription(manifest)
}

// decodeTagsJSON decodes a tags_json column (a JSON array of strings) into a
// []string. Used when building importFields for a review submission from an
// existing plugin row.
func decodeTagsJSON(raw json.RawMessage) []string {
	var out []string
	if len(raw) == 0 || json.Unmarshal(raw, &out) != nil {
		return []string{}
	}
	return out
}
