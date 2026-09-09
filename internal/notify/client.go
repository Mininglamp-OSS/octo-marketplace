// Package notify is octo-marketplace's whole side of the plugin-review
// conversation with octo-server.
//
// It contains three things:
//
//   - Client: the outbound client for the two octo-server internal endpoints
//     the Space review flow needs — a single-subject member-role lookup and
//     role-targeted approval-card delivery.
//   - The HMAC-SHA256 primitives (signature.go) the inbound card-action
//     callback handler uses to authenticate octo-server's decision callback.
//   - BestEffort (best_effort.go) + the bounded card text builders (card.go).
//
// # Recipients are resolved by octo-server, never by us
//
// The card is addressed with `target_role: "space_admin"`; octo-server resolves
// the Space's active human owners/admins itself and reports back only the uids
// it actually delivered to. An earlier design fetched the admin roster and
// passed explicit `targets`; that endpoint was DELETED because the roster
// leaked members' verified legal names across tenants and turned a single
// shared token into a Space-existence oracle. This package therefore has no
// roster call, and NotifyRequest has no Targets field at all — sending one is
// structurally impossible, not merely discouraged.
//
// # Shape
//
// The HTTP client follows internal/fleet: bounded timeout, redirects refused,
// bounded response reads, a typed error carrying the upstream status, and no
// retries (the caller decides). Token values are never logged.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"go.uber.org/zap"
)

// maxRespBytes bounds any response we read from octo-server. Both the role
// lookup and the notify response are small JSON objects.
const maxRespBytes = 1 << 20

// defaultTimeout is used when New is given a non-positive timeout. Deliberately
// tight: every call site is post-commit best-effort work on the response path.
const defaultTimeout = 3 * time.Second

// internalTokenHeader authenticates marketplace to octo-server's /v1/internal
// routes. It carries OCTO_MARKETPLACE_INTERNAL_TOKEN and is never logged.
const internalTokenHeader = "X-Internal-Token"

// targetRoleSpaceAdmin is the only target_role octo-server accepts: the
// Space's active owners and admins (space_member.status=1 AND role>=1) of an
// active Space, robots excluded. An unknown value is a 400 upstream rather than
// a silent fallback, so this is a constant and not a parameter.
const targetRoleSpaceAdmin = "space_admin"

// Space member roles, using octo-server's native space_member.role encoding.
// Note this is the OPPOSITE of octo-web's display encoding; do not convert.
//
// These are the same numeric values as model.SpaceRole*; duplicated here so
// that callers of notify.Client.MemberRole do not have to import model just to
// compare the returned *int. Clamping of out-of-range wire values uses
// model.ClampSpaceRole so the canonical range lives in one place.
const (
	RoleMember = 0
	RoleAdmin  = 1
	RoleOwner  = 2
)

// APIError is a non-2xx response from octo-server. Status lets the caller
// distinguish a client-fault 4xx (a bug in our request, or a rotated token)
// from a 5xx/transport failure (upstream unavailable). Code is octo-server's
// `error.code` when the body used the standard error envelope.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("notify: status %d: %s: %s", e.Status, e.Code, e.Message)
	case e.Code != "":
		return fmt.Sprintf("notify: status %d: %s", e.Status, e.Code)
	case e.Message != "":
		return fmt.Sprintf("notify: status %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("notify: status %d", e.Status)
}

// ApprovalCard is the bounded business payload for octo-server's generic
// approval card. octo-server owns the AdaptiveCard template (pkg/cardtmpl);
// marketplace supplies only these fields and never constructs card JSON.
//
// Actions is deliberately absent: omitting it makes octo-server render its
// localized approve/deny buttons with the correct positive/destructive styles,
// which custom actions drop.
type ApprovalCard struct {
	ActionType  string
	Title       string
	Description string
	Data        map[string]string
}

// NotifyRequest is the caller-facing input to NotifySpaceAdmins.
//
// There is no Targets field by design (see the package doc): the recipient set
// is always "this Space's admins", resolved server-side.
type NotifyRequest struct {
	SpaceID  string
	Service  string
	ActorUID string

	ApprovalCard *ApprovalCard
}

// NotifyResponse is octo-server's delivery report. With role targeting the
// caller never learns the target set up front, so Delivered is its ONLY record
// of who received the card.
//
// An empty Delivered with an empty Filtered means the Space has no active human
// admin. That is a legitimate state of the world and a success, not an error —
// returning an error would make producers retry forever. It is distinguishable
// from "everyone was filtered", which populates Filtered.
type NotifyResponse struct {
	Delivered []string
	Filtered  map[string]string
}

// Client talks to one octo-server base URL (no trailing slash, no /v1 suffix —
// paths are appended as "/v1/...", matching internal/auth.HTTPResolver).
type Client struct {
	baseURL       string
	internalToken string
	http          *http.Client

	// roleDriftOnce bounds the log flood when octo-server returns an out-of-
	// range member role (e.g. it drifts onto the inverted octo-web encoding).
	// See the note on HTTPResolver.driftLogOnce in internal/auth: same shape,
	// same reason — the bad value arrives on every MemberRole call during a
	// drift, so one log per distinct bad value per process life is the bound.
	roleDriftOnce map[int]*sync.Once
	driftMu       sync.Mutex
}

// New returns a Client. An empty baseURL or internalToken yields a disabled
// client: Enabled reports false and both calls fail fast rather than dialing an
// empty host. A non-positive timeout falls back to defaultTimeout.
func New(baseURL, internalToken string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		baseURL:       strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		internalToken: strings.TrimSpace(internalToken),
		http: &http.Client{
			Timeout: timeout,
			// Never follow redirects. We send a long-lived shared service
			// credential in a custom header, and Go only strips the well-known
			// auth headers on a cross-origin redirect — a custom
			// "X-Internal-Token" would be copied verbatim to whatever host a 3xx
			// names, leaking the credential (and turning a notification into an
			// SSRF primitive). Surface the 3xx as the response instead.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		roleDriftOnce: make(map[int]*sync.Once),
	}
}

// Enabled reports whether the client is configured to reach octo-server. A nil
// receiver is treated as disabled so call sites can hold an unwired *Client.
func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != "" && c.internalToken != ""
}

// errDisabled is returned by both calls on an unconfigured client.
var errDisabled = errors.New("notify: octo-server client not configured (missing base URL or internal token)")

// memberRoleEnvelope decodes GET .../members/{uid}/role.
//
// Data is a pointer so an absent `data` object is a decode fault rather than
// being silently indistinguishable from `{"data":{"role":null}}`; Role is a
// pointer because 0 is a REAL role (plain member) and must be distinguishable
// from "not a member". octo-server deliberately omits `omitempty` on its side
// for the same reason.
type memberRoleEnvelope struct {
	Data *struct {
		Role *int `json:"role"`
	} `json:"data"`
}

// MemberRole reports uid's role in spaceID, using octo-server's native
// space_member encoding (RoleMember/RoleAdmin/RoleOwner).
//
// A nil role with a nil error means "not an active member" and is BYTE-
// IDENTICAL upstream for a non-member, a removed member, an unknown space_id,
// and a disbanded Space. That collapse is intentional: any distinguishable
// answer would make a shared service token a cross-tenant Space-existence
// oracle. Do not try to recover the distinction.
//
// Out-of-range role values (negatives, values > RoleOwner, or a drift onto the
// inverted octo-web encoding where 3 means "member") are CLAMPED to RoleMember
// at this boundary, matching the clamp on the HTTP-resolver identity path.
// Review-authorization checks (`*role >= RoleAdmin`) therefore fail closed
// instead of silently promoting every plain member. A clamped value is logged
// once per distinct bad integer per process life so an upstream drift is loud
// without flooding logs.
//
// The card-action callback carries only an asserted operator_uid and no user
// token, so this is how the callback handler independently confirms that the
// operator is STILL an admin at decision time.
func (c *Client) MemberRole(ctx context.Context, spaceID, uid string) (*int, error) {
	if !c.Enabled() {
		return nil, errDisabled
	}
	spaceID = strings.TrimSpace(spaceID)
	uid = strings.TrimSpace(uid)
	if spaceID == "" || uid == "" {
		return nil, errors.New("notify: member role lookup requires space_id and uid")
	}
	path := "/v1/internal/spaces/" + url.PathEscape(spaceID) + "/members/" + url.PathEscape(uid) + "/role"

	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var env memberRoleEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("notify: decode member role response: %w", err)
	}
	if env.Data == nil {
		return nil, errors.New("notify: member role response missing data")
	}
	if env.Data.Role == nil {
		// "not an active member" — keep nil distinct from RoleMember.
		return nil, nil
	}
	clamped, bad := model.ClampSpaceRole(*env.Data.Role)
	if bad {
		c.onceForBadRole(*env.Data.Role).Do(func() {
			logging.Warn("notify_member_role_drift",
				zap.String("operation", "notify.member_role"),
				zap.Int("received_role", *env.Data.Role),
				zap.Int("clamped_to", clamped),
				zap.String("expected_range", "0..2 (0=member, 1=admin, 2=owner)"),
				zap.String("hint", "octo-server returned an out-of-range role on the internal member-role endpoint — likely the inverted octo-web encoding (1=owner,2=admin,3=member). Treating as plain member; IM card-action approvals from that value fail closed."),
			)
		})
		v := clamped
		return &v, nil
	}
	return env.Data.Role, nil
}

// onceForBadRole returns (and lazily creates) the sync.Once for a given bad
// role value, so each distinct bad integer is logged at most once per Client
// (one per process life in practice).
func (c *Client) onceForBadRole(v int) *sync.Once {
	c.driftMu.Lock()
	defer c.driftMu.Unlock()
	o, ok := c.roleDriftOnce[v]
	if !ok {
		o = &sync.Once{}
		c.roleDriftOnce[v] = o
	}
	return o
}

// notifyWire is the exact JSON body sent to POST /v1/internal/notify.
//
// TargetRole is always set and `targets` is not a field at all: octo-server
// rejects a request carrying both, and it must never carry an explicit roster.
type notifyWire struct {
	SpaceID      string            `json:"space_id"`
	Service      string            `json:"service"`
	TargetRole   string            `json:"target_role"`
	ActorUID     string            `json:"actor_uid,omitempty"`
	ApprovalCard *approvalCardWire `json:"approval_card"`
}

type approvalCardWire struct {
	ActionType  string            `json:"action_type"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Data        map[string]string `json:"data"`
}

type notifyEnvelope struct {
	Data struct {
		Delivered []string          `json:"delivered"`
		Filtered  map[string]string `json:"filtered"`
	} `json:"data"`
}

// NotifySpaceAdmins asks octo-server to deliver an approval card to spaceID's
// active owners/admins and returns its delivery report.
//
// Never returns a partial result with an error: on any failure the report is
// nil. An empty Delivered on success means the Space has no active human admin
// (see NotifyResponse).
func (c *Client) NotifySpaceAdmins(ctx context.Context, req NotifyRequest) (*NotifyResponse, error) {
	if !c.Enabled() {
		return nil, errDisabled
	}
	spaceID := strings.TrimSpace(req.SpaceID)
	if spaceID == "" {
		return nil, errors.New("notify: approval card requires space_id")
	}
	if req.ApprovalCard == nil {
		return nil, errors.New("notify: approval card is required")
	}
	if strings.TrimSpace(req.ApprovalCard.ActionType) == "" {
		return nil, errors.New("notify: approval card requires action_type")
	}
	service := strings.TrimSpace(req.Service)
	if service == "" {
		service = "marketplace"
	}
	wire := notifyWire{
		SpaceID:    spaceID,
		Service:    service,
		TargetRole: targetRoleSpaceAdmin,
		ActorUID:   strings.TrimSpace(req.ActorUID),
		ApprovalCard: &approvalCardWire{
			ActionType:  req.ApprovalCard.ActionType,
			Title:       req.ApprovalCard.Title,
			Description: req.ApprovalCard.Description,
			Data:        req.ApprovalCard.Data,
		},
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("notify: encode notify request: %w", err)
	}

	body, err := c.do(ctx, http.MethodPost, "/v1/internal/notify", encoded)
	if err != nil {
		return nil, err
	}
	var env notifyEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("notify: decode notify response: %w", err)
	}
	out := NotifyResponse{
		Delivered: env.Data.Delivered,
		Filtered:  env.Data.Filtered,
	}
	if out.Delivered == nil {
		out.Delivered = []string{}
	}
	if out.Filtered == nil {
		out.Filtered = map[string]string{}
	}
	return &out, nil
}

// do issues one request with the internal token, returning the bounded response
// body on 2xx or an *APIError otherwise. A refused redirect surfaces here as a
// 3xx *APIError rather than a followed request.
func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("notify: build %s %s request: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(internalTokenHeader, c.internalToken)

	resp, err := c.http.Do(req)
	if err != nil {
		// http.Client errors embed the request URL but never header values, so
		// the shared token cannot leak into a log through here.
		return nil, fmt.Errorf("notify: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if err != nil {
		return nil, fmt.Errorf("notify: read %s %s response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code, msg := parseErrorEnvelope(data)
		return nil, &APIError{Status: resp.StatusCode, Code: code, Message: msg}
	}
	return data, nil
}

// parseErrorEnvelope extracts octo-server's `{"error":{"code","message"}}`,
// falling back to the truncated raw body when it isn't that shape.
func parseErrorEnvelope(data []byte) (code, message string) {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err == nil && (env.Error.Code != "" || env.Error.Message != "") {
		return env.Error.Code, truncateRunes(env.Error.Message, 200)
	}
	return "", truncateRunes(strings.TrimSpace(string(data)), 200)
}

// truncateRunes bounds an upstream message by RUNE so a giant or multi-byte
// (Chinese) upstream error cannot balloon a log line or be cut mid-character.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}
