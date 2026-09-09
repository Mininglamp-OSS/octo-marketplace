package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/notify"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// maxCardActionBody bounds the callback body before hashing. octo-server's
// payload is a small fixed shape; anything larger is not a legitimate callback
// and must not be read into memory just to compute a digest over it.
const maxCardActionBody = 64 << 10

// CardActionPath is both the mount point and the path covered by the signature.
// It must match the `url` path in octo-server's OCTO_CARD_ACTION_ROUTES entry:
// the signature covers the path, so this is part of the wire contract and cannot
// be renamed independently of octo-server's configuration.
const CardActionPath = "/v1/card-actions/decide"

// CardActionService is the decision entry point used by the callback. Narrower
// than the tenant Service interface because this route is not a tenant surface:
// it has no authenticated caller, only a signed payload.
type CardActionService interface {
	DecideReviewFromCard(ctx context.Context, eventID, operatorUID, decision, reviewID string) (*pluginsvc.CardActionResult, error)
}

// CardActionHandler serves POST /v1/card-actions/decide, octo-server's
// card-action callback for IM approve/deny clicks.
//
// This route is mounted OUTSIDE the tenant Authenticator: octo-server has no user
// token to present (the dispatch is an asynchronous retrying queue, not a
// user-facing request). Authentication is the HMAC signature over the exact
// request bytes; authorization is re-derived from operator_uid inside the
// service, because operator_uid is an identity assertion, not a grant.
type CardActionHandler struct {
	svc     CardActionService
	secret  string
	maxSkew time.Duration
	// path is the value octo-server signs. It comes from the configured route
	// constant rather than the observed request path, so a proxy rewrite cannot
	// silently change what gets verified.
	path string
	now  func() time.Time
}

// NewCardAction builds the callback handler. An empty secret leaves the endpoint
// permanently closed (every request 401s) rather than accepting unsigned input.
func NewCardAction(svc CardActionService, secret string, maxSkew time.Duration) *CardActionHandler {
	if maxSkew <= 0 {
		maxSkew = 5 * time.Minute
	}
	return &CardActionHandler{
		svc:     svc,
		secret:  strings.TrimSpace(secret),
		maxSkew: maxSkew,
		path:    CardActionPath,
		now:     time.Now,
	}
}

// Register mounts the callback on the root engine, deliberately not under
// /api/v1 and deliberately with no authenticator in the chain.
func (h *CardActionHandler) Register(r gin.IRouter) {
	r.POST(CardActionPath, h.Decide)
}

// decisionRequest mirrors octo-server's DecisionRequest. Only the fields this
// consumer needs are declared; event_id is a decimal string because its int64
// range exceeds JavaScript's safe-integer range.
type decisionRequest struct {
	EventID     string         `json:"event_id"`
	ActionID    string         `json:"action_id"`
	Decision    string         `json:"decision"`
	OperatorUID string         `json:"operator_uid"`
	Data        map[string]any `json:"data"`
	SpaceID     string         `json:"space_id"`
}

// cardActionUnauthorized answers 401 and records WHY. These refusals were
// previously silent, which made a rotated shared secret, a clock skew, and "no IM
// traffic at all" indistinguishable in the logs — and the callback has no other
// operator-visible surface, because a 401 is the one 4xx octo-server does not
// route to the DLQ. `reason` is a fixed label: nothing from the request body, the
// signature, or the attacker-controlled headers is logged, since none of it is
// trustworthy at this point.
func cardActionUnauthorized(c *gin.Context, reason string) {
	logging.Warn("card_action_unauthorized",
		zap.String("operation", "plugin_review.card_action"),
		zap.String("reason", reason))
	c.Status(http.StatusUnauthorized)
}

// Decide handles an IM approve/deny click.
//
// Status semantics follow the octo-server consumer contract: every HANDLED
// outcome — including a business refusal — is HTTP 200 with a typed body, so the
// event is acknowledged and not redelivered. Only transient faults return 5xx
// (retried with backoff). A 4xx other than 401 sends the event to the DLQ, so it
// is reserved for permanently malformed input.
func (h *CardActionHandler) Decide(c *gin.Context) {
	// Verify first, parse second: the signature covers the exact bytes on the
	// wire, so the body must be read raw and hashed before any decoder sees it.
	// Re-serializing the JSON would change the digest.
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxCardActionBody+1))
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "read body"})
		return
	}
	if len(raw) > maxCardActionBody {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body too large"})
		return
	}

	signature := c.GetHeader(notify.HeaderSignature)
	timestamp := c.GetHeader(notify.HeaderTimestamp)
	eventID := strings.TrimSpace(c.GetHeader(notify.HeaderEventID))

	if h.secret == "" {
		// Unconfigured means closed, not open.
		logging.Warn("card_action_secret_not_configured", zap.String("operation", "plugin_review.card_action"))
		c.Status(http.StatusUnauthorized)
		return
	}
	if !notify.VerifyTimestampAt(timestamp, h.now(), h.maxSkew) {
		cardActionUnauthorized(c, "stale_timestamp")
		return
	}
	if !notify.Verify(h.secret, signature, http.MethodPost, h.path, timestamp, eventID, raw) {
		cardActionUnauthorized(c, "bad_signature")
		return
	}

	var body decisionRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		// The signature was valid, so this came from octo-server but is
		// unparseable: permanently broken, DLQ it rather than retry forever.
		c.JSON(http.StatusBadRequest, gin.H{"error": "malformed payload"})
		return
	}
	// The body's event_id must match the signed header, otherwise a valid
	// signature for one event could be made to carry another event's payload.
	if strings.TrimSpace(body.EventID) != eventID {
		cardActionUnauthorized(c, "event_id_mismatch")
		return
	}
	// event_id is the idempotency key and is stored as a decimal string; reject
	// anything that is not one rather than letting it into the receipt table.
	if _, err := strconv.ParseInt(eventID, 10, 64); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "malformed event_id"})
		return
	}

	reviewID, _ := body.Data["review_id"].(string)
	result, err := h.svc.DecideReviewFromCard(c.Request.Context(), eventID,
		body.OperatorUID, body.Decision, strings.TrimSpace(reviewID))
	if err != nil {
		if errors.Is(err, pluginsvc.ErrCardBadDecision) {
			// A malformed payload (unrecognized decision, missing review_id,
			// empty operator_uid): permanently broken. Return 400 so octo-server
			// routes the event to the DLQ instead of acking it as 无权限, which
			// would silently discard a real admin's click every time the
			// vocabulary drifts between this service and octo-server.
			c.JSON(http.StatusBadRequest, gin.H{"error": "malformed decision payload"})
			return
		}
		if errors.Is(err, pluginsvc.ErrReviewInvalid) {
			// A decision that failed validation for a handled reason (e.g. the
			// operator is not a reviewer). This is a HANDLED refusal: 200 with
			// a typed body so the card renders "no permission" and stops
			// redelivery.
			c.JSON(http.StatusOK, pluginsvc.CardActionResult{Disposition: "forbidden", State: "pending"})
			return
		}
		// Genuine fault: database unavailable, or the role lookup could not be
		// performed. 503 asks octo-server to retry with backoff instead of
		// silently discarding a real admin's decision.
		logging.Error("card_action_decide_failed",
			zap.String("operation", "plugin_review.card_action"),
			zap.String("event_id", eventID),
			zap.String("review_id", reviewID),
			logging.ErrorField(err))
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "decision unavailable"})
		return
	}
	logging.Info("card_action_decided",
		zap.String("operation", "plugin_review.card_action"),
		zap.String("event_id", eventID),
		zap.String("review_id", reviewID),
		zap.String("disposition", result.Disposition),
		zap.String("state", result.State))
	c.JSON(http.StatusOK, result)
}
