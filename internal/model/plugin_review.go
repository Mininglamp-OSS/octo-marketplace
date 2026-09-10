package model

import (
	"encoding/json"
	"time"
)

// ReviewStatus is the lifecycle state of a plugin review request.
//
// NOTE: this is OUR vocabulary. The octo-server card-action protocol uses a
// different one (denied/cancelled); never send these strings over that wire —
// route them through the single translation point in service/plugin/review.go.
type ReviewStatus string

const (
	ReviewStatusPending  ReviewStatus = "pending"
	ReviewStatusApproved ReviewStatus = "approved"
	ReviewStatusRejected ReviewStatus = "rejected"
	ReviewStatusCanceled ReviewStatus = "canceled"
)

// ReviewKind distinguishes the initial Space listing from a version upgrade.
type ReviewKind string

const (
	ReviewKindFirst   ReviewKind = "first"
	ReviewKindUpgrade ReviewKind = "upgrade"
)

// ReviewDecisionSource records where a decision originated.
type ReviewDecisionSource string

const (
	ReviewDecisionSourceWeb    ReviewDecisionSource = "web"
	ReviewDecisionSourceIM     ReviewDecisionSource = "im"
	ReviewDecisionSourcePolicy ReviewDecisionSource = "policy"
)

// PluginReviewPolicy is the effective policy for the authenticated Space.
// A missing persistence row is represented as the default Enabled=true value.
type PluginReviewPolicy struct {
	IsAutoApproveEnabled bool       `json:"is_auto_approve_enabled"`
	UpdatedAt            *time.Time `json:"updated_at,omitempty"`
}

// DefaultIMDenyReason is the reason persisted when an admin clicks the deny
// button on the notification card. The generic approval-card template only
// emits Action.Submit (no Input.Text), so an IM reject cannot carry a
// per-request reason; the web reject modal requires an explicit one.
const DefaultIMDenyReason = "管理员在消息卡片中拒绝,未填写原因"

// PluginReviewRequest is a frozen-snapshot submission waiting for Space
// owner/admin approval (or its terminal outcome). Status lives on this entity,
// not on the plugin, so a listed v1 can coexist with an in-review v2.
type PluginReviewRequest struct {
	ID          string       `json:"review_id"`
	PluginID    string       `json:"plugin_id"`
	SpaceID     string       `json:"space_id"`
	TargetScope string       `json:"target_scope"` // "space" now; "system" reserved
	Status      ReviewStatus `json:"status"`
	Kind        ReviewKind   `json:"kind"`
	Version     string       `json:"version"`
	Changelog   *string      `json:"changelog,omitempty"`
	// Frozen snapshot bytes. Loaded only by the detail read (LoadReviewSnapshot);
	// the list query deliberately does not select these columns.
	ManifestJSON  json.RawMessage `json:"-"`
	PluginJSON    json.RawMessage `json:"-"`
	RelationsJSON json.RawMessage `json:"-"`
	// AttachmentKeys is the frozen storage-attachment sidecar (path -> managed
	// object key) for the snapshot. It is NULL for submissions that carry no
	// spilled files (all-inline packages, declared-JSON connector/expert
	// submissions). Mirrors plugins.attachment_keys_json exactly so a
	// zip-submitted skill upgrade can carry its complete snapshot through to
	// approval without relying on the live row's keys still mapping correctly.
	AttachmentKeys json.RawMessage       `json:"-"`
	ManifestHash   string                `json:"manifest_hash"`
	PluginHash     string                `json:"plugin_hash"`
	ApplicantUID   string                `json:"applicant_id"`
	ApplicantName  string                `json:"applicant_name"`
	ReviewerUID    *string               `json:"reviewer_id,omitempty"`
	ReviewerName   *string               `json:"reviewer_name,omitempty"`
	Reason         *string               `json:"reason,omitempty"`
	DecisionSource *ReviewDecisionSource `json:"decision_source,omitempty"`
	SubmittedAt    time.Time             `json:"submitted_at"`
	ReviewedAt     *time.Time            `json:"reviewed_at,omitempty"`
	CreatedAt      time.Time             `json:"-"`
	UpdatedAt      time.Time             `json:"-"`

	// Joined from the plugin row for display; not stored on the request.
	PluginName     string     `json:"plugin_name,omitempty"`
	PluginType     PluginType `json:"plugin_type,omitempty"`
	PluginIcon     string     `json:"plugin_icon,omitempty"`
	CurrentVersion *string    `json:"current_version,omitempty"`
	// PluginListingState is the plugin's CURRENT listing state, which can have
	// moved on since this request was decided: an approved plugin a Space admin
	// later took down is `delisted`. The review queue needs it to show the right
	// status and to stop offering 下架 on something already down.
	PluginListingState PluginListingState `json:"plugin_listing_state,omitempty"`
	// ReadmeContent is the primary document extracted from the FROZEN package
	// snapshot (SKILL.md and friends) — the text a reviewer is actually deciding
	// on. Populated on the detail read only.
	ReadmeContent string `json:"readme_content,omitempty"`
}

// CardActionReceipt records the response already returned for an octo-server
// card-action event, giving IM decisions at-least-once idempotency. EventID is a
// decimal string (int64 exceeds the JS safe-integer range, so it is never a
// number on the wire).
type CardActionReceipt struct {
	EventID        string    `json:"event_id"`
	StoredResponse string    `json:"stored_response"`
	ReviewID       string    `json:"review_id,omitempty"`
	Decision       string    `json:"decision,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}
