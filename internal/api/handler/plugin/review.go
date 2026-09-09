package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// maxReviewBodyBytes bounds a review request body. It MUST stay equal to
// `maxBodyBytes` (the /plugins/upsert cap in handler.go): since the upgrade
// amendment a submit carries the full declared manifest/package — the same
// documents upsert accepts — and `parse_task_id` is only available to skills,
// so a connector/expert/expert_team with more content than this cap has no
// other door to an upgrade (direct edits of a listed plugin are 409). Keep the
// two constants in sync; the cheaper cap here would silently freeze upgrades.
const maxReviewBodyBytes = maxBodyBytes

// decodeReviewBody is a size-bounded, unknown-field-TOLERANT decoder. The strict
// `decode` used by /plugins/upsert would reject a client that sends a harmless
// extra field, and the octo-web review client is already built and shipped
// against this contract — rejecting its payload outright is a worse failure than
// ignoring a field we do not read.
func decodeReviewBody(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxReviewBodyBytes)
	if err := json.NewDecoder(c.Request.Body).Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apiresponse.Fail(c, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body is too large", map[string]any{"max_bytes": maxReviewBodyBytes}, "Reduce the request size.")
			return false
		}
		if errors.Is(err, io.EOF) {
			validation(c, "body")
			return false
		}
		validation(c, "body")
		return false
	}
	return true
}

// reviewRequestResponse is the wire form of a review request.
//
// The LIST shape is deliberately lean: it omits the frozen manifest, package,
// attachment sidecar and relation graph so the queue page stays small and never
// ships document bytes to rows the caller did not click into.
//
// The DETAIL shape (returned by GET /review_requests/{review_id}) adds two
// reviewer-facing projections of the FROZEN snapshot: readme_content (the
// primary document text the decision will actually ship) and frozen_relations
// (the membership graph as it was when the applicant submitted). For containers
// (expert / expert_team) the membership graph IS part of the reviewable content,
// and approval applies that exact graph atomically — shipping it alongside
// whatever the live membership happened to be when the reviewer clicked approve
// would defeat the purpose of the freeze. frozen_relations is populated only on
// the detail read and is parsed defensively: a missing or malformed snapshot
// degrades to an empty array rather than 500ing the endpoint.
type reviewRequestResponse struct {
	ReviewID       string                      `json:"review_id"`
	PluginID       string                      `json:"plugin_id"`
	SpaceID        string                      `json:"space_id"`
	TargetScope    string                      `json:"target_scope"`
	Status         model.ReviewStatus          `json:"status"`
	Kind           model.ReviewKind            `json:"kind"`
	Version        string                      `json:"version"`
	Changelog      *string                     `json:"changelog,omitempty"`
	ManifestHash   string                      `json:"manifest_hash"`
	PluginHash     string                      `json:"plugin_hash"`
	ApplicantID    string                      `json:"applicant_id"`
	ApplicantName  string                      `json:"applicant_name"`
	ReviewerID     *string                     `json:"reviewer_id,omitempty"`
	ReviewerName   *string                     `json:"reviewer_name,omitempty"`
	Reason         *string                     `json:"reason,omitempty"`
	DecisionSource *model.ReviewDecisionSource `json:"decision_source,omitempty"`
	SubmittedAt    time.Time                   `json:"submitted_at" swaggertype:"string,date-time"`
	ReviewedAt     *time.Time                  `json:"reviewed_at,omitempty" swaggertype:"string,date-time"`
	PluginName     string                      `json:"plugin_name,omitempty"`
	PluginType     model.PluginType            `json:"plugin_type,omitempty"`
	// PluginIcon is a display URL (an uploaded icon is stored as an object key and
	// must be presigned), resolved by the service through the same path the plugin
	// list uses.
	PluginIcon     string  `json:"plugin_icon,omitempty"`
	CurrentVersion *string `json:"current_version,omitempty"`
	// PluginListingState is the plugin's CURRENT listing state, which can have
	// moved on since this request was decided — an approved plugin a Space admin
	// later delisted reads `delisted`. The review queue renders the row status from
	// it and uses it to stop offering 下架 on something already down.
	PluginListingState model.PluginListingState `json:"plugin_listing_state,omitempty"`
	// ReadmeContent is the primary document of the FROZEN submission; populated
	// on the detail read only.
	ReadmeContent string `json:"readme_content,omitempty"`
	// FrozenRelations is the FROZEN relation graph the reviewer is approving.
	// Populated ONLY on the DETAIL response (GET /review_requests/{review_id}) via
	// the LoadReviewSnapshot read. The list projection (reviewListDTO) keeps this
	// pointer nil so omitempty drops the key and per-row graph data never leaks to
	// the queue. On a detail response the pointer is always non-nil and points at
	// a slice that is either empty (for missing/empty/malformed/zero-edge
	// snapshots — renders as JSON []) or populated (the decoded edges in stable
	// snake_case DTO). Each element carries the edge approval will apply: target
	// plugin id, relation type, sort order and data payload (which for
	// expert_team members encodes is_leader/role/member_key). Target display name
	// is intentionally not carried here — the snapshot is of the frozen edges,
	// not of whatever the target rows are named right now, and the detail
	// endpoint must not reach back to the live plugin table to enrich frozen
	// data with mutable state.
	FrozenRelations *[]reviewRelationResponse `json:"frozen_relations,omitempty"`
}

// reviewRelationResponse is one edge in the frozen membership graph carried on
// the review detail response. The field set is deliberately narrow: it covers
// what a reviewer needs to inspect membership (which target, what kind of edge,
// in what order, with what role/data) and nothing the snapshot does not contain.
// Fields that reference mutable plugin-row state (display name, embedded flag)
// are intentionally omitted — shipping them would mislead by showing the live
// value rather than the one under review.
type reviewRelationResponse struct {
	RelationID     string           `json:"relation_id"`
	TargetPluginID string           `json:"target_plugin_id"`
	TargetType     model.PluginType `json:"target_plugin_type,omitempty"`
	RelationType   string           `json:"relation_type"`
	SortOrder      int              `json:"sort_order"`
	Data           json.RawMessage  `json:"data,omitempty" swaggertype:"object"`
}

func decodeFrozenRelations(raw json.RawMessage) []reviewRelationResponse {
	if len(raw) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	// model.PluginRelation has no json tags, so plugin_review_requests.relations_json
	// stores Go default field names (ID, TargetPluginID, Type, SortOrder, Data, …).
	// Decode with those tags and project onto the stable snake_case wire DTO.
	// Use a sentinel pointer so we can distinguish "empty array in JSON" (return
	// non-nil empty) from "decoder error" (also return non-nil empty for the
	// degrade-to-[] contract) — both surface as [] on the wire, but the pointer
	// trick below is only needed if we ever need to distinguish them. We don't
	// here; the non-nil empty slice encodes as the JSON array [] via the custom
	// marshaler we add on the response type.
	var edges []struct {
		ID               string           `json:"ID"`
		TargetPluginID   string           `json:"TargetPluginID"`
		TargetPluginType model.PluginType `json:"TargetPluginType"`
		Type             string           `json:"Type"`
		SortOrder        int              `json:"SortOrder"`
		Data             json.RawMessage  `json:"Data"`
	}
	if err := json.Unmarshal(raw, &edges); err != nil {
		// A malformed snapshot must not 500 the detail read: the reviewer still
		// needs to see the documents and reject. Surface as an explicitly tagged
		// empty (non-nil, zero-length) slice so the wire key renders as [].
		return []reviewRelationResponse{}
	}
	out := make([]reviewRelationResponse, 0, len(edges))
	for _, e := range edges {
		var data json.RawMessage
		if len(e.Data) > 0 {
			t := bytes.TrimSpace(e.Data)
			if len(t) > 0 && !bytes.Equal(t, []byte("null")) {
				data = normalizedObjectRaw(e.Data)
			}
		}
		out = append(out, reviewRelationResponse{
			RelationID:     e.ID,
			TargetPluginID: e.TargetPluginID,
			TargetType:     e.TargetPluginType,
			RelationType:   e.Type,
			SortOrder:      e.SortOrder,
			Data:           data,
		})
	}
	// Always return a non-nil slice here: either the populated edges, or a 0-len
	// allocated slice for a literal JSON `[]`. The caller (reviewDTO) treats nil
	// as "no snapshot loaded, omit the field"; a non-nil slice makes the key
	// render as []/[..].
	return out
}

// reviewSubmitRequest is the submit body.
//
// Content arrives one of three mutually-exclusive ways:
//
//   - parse_task_id: server-side materialization from a completed skill zip
//     parse task. This is how the browser "发布新版本" flow submits a skill
//     upgrade when the author uploaded a new zip. The canonical package is
//     built server-side by expanding the zip exactly as /plugins/import does;
//     binary/oversize files are spilled to content-addressed object keys. Only
//     valid for skill plugins.
//   - manifest_json + plugin_json together: declared JSON documents. Used by
//     connectors, experts, expert teams, and by skill authors who edit content
//     without re-uploading a zip.
//   - neither: snapshot the live draft row (private first-submission only;
//     listed plugins require content).
//
// relations uses the same target-state shape and semantics as /plugins/upsert:
// present (even empty) replaces the reviewed graph, ABSENT inherits the plugin's
// live graph — so a client editing only documents cannot empty an expert team by
// forgetting the field.
type reviewSubmitRequest struct {
	PluginID string `json:"plugin_id"`
	// Version label as MAJOR.MINOR.PATCH, each part 1-9 digits. Must be at least the
	// plugin's current label, and must not already be published.
	Version      string             `json:"version" pattern:"^\\d{1,9}\\.\\d{1,9}\\.\\d{1,9}$"`
	Changelog    string             `json:"changelog,omitempty"`
	ParseTaskID  string             `json:"parse_task_id,omitempty"`
	ManifestJSON json.RawMessage    `json:"manifest_json,omitempty" swaggertype:"object"`
	PluginJSON   json.RawMessage    `json:"plugin_json,omitempty" swaggertype:"object"`
	Relations    *[]relationRequest `json:"relations,omitempty"`
}

type reviewRejectRequest struct {
	Reason string `json:"reason"`
}

func reviewDTO(r *model.PluginReviewRequest) reviewRequestResponse {
	if r == nil {
		empty := []reviewRelationResponse{}
		return reviewRequestResponse{FrozenRelations: &empty}
	}
	out := reviewRequestResponse{
		ReviewID:           r.ID,
		PluginID:           r.PluginID,
		SpaceID:            r.SpaceID,
		TargetScope:        r.TargetScope,
		Status:             r.Status,
		Kind:               r.Kind,
		Version:            r.Version,
		Changelog:          r.Changelog,
		ManifestHash:       r.ManifestHash,
		PluginHash:         r.PluginHash,
		ApplicantID:        r.ApplicantUID,
		ApplicantName:      r.ApplicantName,
		ReviewerID:         r.ReviewerUID,
		ReviewerName:       r.ReviewerName,
		Reason:             r.Reason,
		DecisionSource:     r.DecisionSource,
		SubmittedAt:        r.SubmittedAt,
		ReviewedAt:         r.ReviewedAt,
		PluginName:         r.PluginName,
		PluginType:         r.PluginType,
		PluginIcon:         r.PluginIcon,
		CurrentVersion:     r.CurrentVersion,
		PluginListingState: r.PluginListingState,
		ReadmeContent:      r.ReadmeContent,
	}
	// decodeFrozenRelations returns:
	//   - nil           when RelationsJSON is nil/empty/NULL (list path, which
	//                   does not select the column at all);
	//   - non-nil 0-len when snapshot bytes were present but decode failed
	//                   (malformed) or the JSON is an empty array;
	//   - populated     when the snapshot decodes to one or more edges.
	//
	// We take the address for the wire field so omitempty can distinguish
	// "absent" (nil pointer, list path) from "present but empty" (pointer to
	// 0-len slice, detail path with no edges or corrupt bytes) from "present
	// with edges".
	if edges := decodeFrozenRelations(r.RelationsJSON); edges != nil {
		out.FrozenRelations = &edges
	}
	return out
}

// reviewListDTO is the list projection. It is identical to reviewDTO but with
// FrozenRelations forced to nil so the per-row graph never reaches the list
// response even if a future refactor makes the service load snapshot bytes on
// the list path (defense in depth against the reviewer-graph leak this change
// closes).
func reviewListDTO(r *model.PluginReviewRequest) reviewRequestResponse {
	d := reviewDTO(r)
	d.FrozenRelations = nil
	return d
}

// SubmitReview submits a plugin for Space visibility review.
//
// @Summary Submit a plugin for Space review
// @Description Freezes the reviewed content under a caller-supplied version label and queues it for Space owner/admin approval. Content is supplied one of three ways: (1) parse_task_id for a skill zip upload processed server-side, materializing the package exactly as /plugins/import does; (2) manifest_json and plugin_json together for declared JSON documents (connectors, experts, expert teams, or a skill text edit without reupload); (3) neither, which snapshots the live draft row and is valid only while the plugin is private. Submitting NEVER changes the plugin row — the listed content only changes when a reviewer approves. Only the plugin owner may submit, only one request per plugin may be pending, and a version label already published for that plugin is refused.
// @Tags plugin
// @ID plugin.review_request.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param payload body reviewSubmitRequest true "Submission"
// @Success 200 {object} apiresponse.Data[reviewRequestResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR: missing/invalid fields; parse_task_id is mutually exclusive with manifest_json and is only valid for skills; listed plugins require content"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT: a request is already pending, or the version label is already published"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 429 {object} apiresponse.Error "RATE_LIMITED"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/review_requests [post]
func (h *Handler) SubmitReview(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var body reviewSubmitRequest
	if !decodeReviewBody(c, &body) {
		return
	}
	params := pluginsvc.ReviewSubmitParams{
		PluginID:    body.PluginID,
		Version:     body.Version,
		Changelog:   body.Changelog,
		ParseTaskID: body.ParseTaskID,
		Manifest:    body.ManifestJSON,
		Package:     body.PluginJSON,
	}
	if body.Relations != nil {
		rels := make([]pluginsvc.RelationRequest, 0, len(*body.Relations))
		for _, r := range *body.Relations {
			rels = append(rels, pluginsvc.RelationRequest{
				ID:             r.RelationID,
				SourcePluginID: r.SourcePluginID,
				TargetPluginID: r.TargetPluginID,
				Type:           r.RelationType,
				SortOrder:      r.SortOrder,
				Data:           r.Data,
			})
		}
		params.Relations = &rels
	}
	out, err := h.svc.SubmitReview(c.Request.Context(), caller, params)
	if err != nil {
		writeServiceError(c, err, "plugin_review_request.create")
		return
	}
	apiresponse.OK(c, reviewDTO(out))
}

// ListReviews lists review requests for the caller or for the Space queue.
//
// @Summary List plugin review requests
// @Description mode=mine lists the caller's own submissions; mode=space lists every request in the Space and requires the Space owner/admin role. Both are always scoped to the Space of the request.
// @Tags plugin
// @ID plugin.review_request.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param mode query string true "List mode" Enums(mine,space)
// @Param status query string false "Filter by status" Enums(pending,approved,rejected,canceled)
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[reviewRequestResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN: mode=space requires the Space owner/admin role"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 429 {object} apiresponse.Error "RATE_LIMITED"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/review_requests [get]
func (h *Handler) ListReviews(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	page, pageSize, ok := pagination(c)
	if !ok {
		validation(c, "page")
		return
	}
	status := model.ReviewStatus(strings.TrimSpace(c.Query("status")))
	switch status {
	case "", model.ReviewStatusPending, model.ReviewStatusApproved, model.ReviewStatusRejected, model.ReviewStatusCanceled:
	default:
		validation(c, "status")
		return
	}
	// Validated here rather than left to the service so the 400 names the
	// offending query parameter instead of a generic "body".
	mode := strings.TrimSpace(c.Query("mode"))
	if mode != "mine" && mode != "space" {
		validation(c, "mode")
		return
	}
	items, total, err := h.svc.ListReviews(c.Request.Context(), caller, mode, status, page, pageSize)
	if err != nil {
		writeServiceError(c, err, "plugin_review_request.list")
		return
	}
	out := make([]reviewRequestResponse, 0, len(items))
	for _, item := range items {
		out = append(out, reviewListDTO(item))
	}
	apiresponse.Offset(c, out, int(total), page, pageSize)
}

// GetReview returns one review request including the frozen submission preview.
//
// @Summary Get a plugin review request
// @Description Returns the request plus readme_content extracted from the frozen submission. Applicants see their own requests; Space owners/admins see every request in the Space. A request in another Space is reported as not found.
// @Tags plugin
// @ID plugin.review_request.get
// @Accept json
// @Produce json
// @Security Bearer
// @Param review_id path string true "Review request ID"
// @Success 200 {object} apiresponse.Data[reviewRequestResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 429 {object} apiresponse.Error "RATE_LIMITED"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/review_requests/{review_id} [get]
func (h *Handler) GetReview(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	reviewID := strings.TrimSpace(c.Param("review_id"))
	if reviewID == "" {
		validation(c, "review_id")
		return
	}
	out, err := h.svc.GetReview(c.Request.Context(), caller, reviewID)
	if err != nil {
		writeServiceError(c, err, "plugin_review_request.get")
		return
	}
	apiresponse.OK(c, reviewDTO(out))
}

// ApproveReview approves a pending request and applies the frozen submission.
//
// @Summary Approve a plugin review request
// @Description Requires the Space owner/admin role. Applies the frozen documents and relation graph to the plugin, records a release version carrying the applicant's label, and for a first listing flips visibility from private to space. Losing a concurrent decision race returns CONFLICT.
// @Tags plugin
// @ID plugin.review_request.approve
// @Accept json
// @Produce json
// @Security Bearer
// @Param review_id path string true "Review request ID"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN: Space owner/admin role required"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT: already decided"
// @Failure 429 {object} apiresponse.Error "RATE_LIMITED"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/review_requests/{review_id}/approve [post]
func (h *Handler) ApproveReview(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	reviewID := strings.TrimSpace(c.Param("review_id"))
	if reviewID == "" {
		validation(c, "review_id")
		return
	}
	plug, err := h.svc.ApproveReview(c.Request.Context(), caller, reviewID)
	if err != nil {
		writeServiceError(c, err, "plugin_review_request.approve")
		return
	}
	apiresponse.OK(c, detailResponse{Plugin: pluginDTO(plug), Relations: []relationResponse{}})
}

// RejectReview rejects a pending request with a required reason.
//
// @Summary Reject a plugin review request
// @Description Requires the Space owner/admin role and a non-empty reason of at most 1000 characters. The plugin is left untouched: a private draft stays private, and an already-listed version stays live. The applicant may edit and resubmit, reusing the same version label.
// @Tags plugin
// @ID plugin.review_request.reject
// @Accept json
// @Produce json
// @Security Bearer
// @Param review_id path string true "Review request ID"
// @Param payload body reviewRejectRequest true "Rejection reason"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR: reason is required"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN: Space owner/admin role required"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT: already decided"
// @Failure 429 {object} apiresponse.Error "RATE_LIMITED"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/review_requests/{review_id}/reject [post]
func (h *Handler) RejectReview(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	reviewID := strings.TrimSpace(c.Param("review_id"))
	if reviewID == "" {
		validation(c, "review_id")
		return
	}
	var body reviewRejectRequest
	if !decodeReviewBody(c, &body) {
		return
	}
	if err := h.svc.RejectReview(c.Request.Context(), caller, reviewID, body.Reason); err != nil {
		writeServiceError(c, err, "plugin_review_request.reject")
		return
	}
	apiresponse.OK(c, apiresponse.EmptyResp{})
}

// CancelReview withdraws the caller's own pending request.
//
// @Summary Cancel a plugin review request
// @Description Withdraws a pending request. Only the applicant may cancel; reviewers reject instead. A request that has already been decided returns CONFLICT, not NOT_FOUND.
// @Tags plugin
// @ID plugin.review_request.cancel
// @Accept json
// @Produce json
// @Security Bearer
// @Param review_id path string true "Review request ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND: no such pending request for this applicant"
// @Failure 409 {object} apiresponse.Error "CONFLICT: already decided"
// @Failure 429 {object} apiresponse.Error "RATE_LIMITED"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/review_requests/{review_id}/cancel [post]
func (h *Handler) CancelReview(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	reviewID := strings.TrimSpace(c.Param("review_id"))
	if reviewID == "" {
		validation(c, "review_id")
		return
	}
	if err := h.svc.CancelReview(c.Request.Context(), caller, reviewID); err != nil {
		writeServiceError(c, err, "plugin_review_request.cancel")
		return
	}
	apiresponse.OK(c, apiresponse.EmptyResp{})
}
