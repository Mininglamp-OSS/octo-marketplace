package plugin

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type capabilityInstallationService interface {
	CreateInstallation(context.Context, pluginsvc.Caller, string, pluginsvc.InstallationParams) (*pluginsvc.InstallationOutcome, error)
}

type createInstallationRequest struct {
	ResourceName string            `json:"resource_name,omitempty" maxLength:"200"`
	RuntimeID    string            `json:"runtime_id" binding:"required" format:"uuid"`
	CustomEnv    map[string]string `json:"custom_env,omitempty"`
}

// CreateInstallation godoc
// @Summary Install a plugin through Fleet capability installation
// @Description Resolves the visible expert or expert_team graph and installs it atomically in Fleet. Requires a stable Idempotency-Key; retry uncertain outcomes with the same key and input. Disabled by default pending the Fleet schema extensions; the legacy /plugins/install remains available.
// @Tags plugin
// @ID plugin.installation.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Marketplace plugin ID"
// @Param X-Workspace-ID header string true "Target Workspace ID"
// @Param Idempotency-Key header string true "Stable key for this installation; 1-200 visible ASCII characters"
// @Param body body createInstallationRequest true "Resource name, local runtime, and environment bindings"
// @Success 200 {object} apiresponse.Data[installResponse]
// @Header 200 {string} Idempotency-Replayed "true when Fleet replayed the installation"
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT or DUPLICATE"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 415 {object} apiresponse.Error "UNSUPPORTED_MEDIA_TYPE"
// @Failure 429 {object} apiresponse.Error "RATE_LIMITED"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Failure 503 {object} apiresponse.Error "UPSTREAM_UNAVAILABLE"
// @Router /plugins/{plugin_id}/installations [post]
func (h *Handler) CreateInstallation(c *gin.Context) {
	user, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	if user.BotUID != "" {
		apiresponse.Fail(c, http.StatusForbidden, errcode.PermissionDenied, "user identity is required", nil, "Use an authenticated user session.")
		return
	}
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" || (c.GetHeader("Content-Encoding") != "" && c.GetHeader("Content-Encoding") != "identity") {
		apiresponse.Fail(c, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "uncompressed JSON is required", nil, "Send application/json without content encoding.")
		return
	}
	// This route uses only the explicit Workspace ID. Reject the legacy slug
	// selector rather than silently ignoring a conflicting second target.
	if c.GetHeader("X-Workspace-Slug") != "" || len(c.Request.Header.Values("X-Workspace-ID")) != 1 || len(c.Request.Header.Values("Idempotency-Key")) != 1 {
		validation(c, "headers")
		return
	}
	var req createInstallationRequest
	if !decode(c, &req) {
		return
	}
	p, err := pluginsvc.NormalizeInstallationParams(pluginsvc.InstallationParams{WorkspaceID: c.GetHeader("X-Workspace-ID"), RuntimeID: req.RuntimeID, ResourceName: req.ResourceName, CustomEnv: req.CustomEnv, IdempotencyKey: c.GetHeader("Idempotency-Key"), Token: marketmiddleware.Token(c)})
	if err != nil {
		writeInstallationError(c, err)
		return
	}
	svc, ok := h.svc.(capabilityInstallationService)
	if !ok {
		writeInstallationError(c, fleet.ErrCapabilityInstallDisabled)
		return
	}
	out, err := svc.CreateInstallation(c.Request.Context(), user, c.Param("plugin_id"), p)
	if err != nil {
		writeInstallationError(c, err)
		return
	}
	if out == nil {
		writeInstallationError(c, errors.New("installation returned no result"))
		return
	}
	if out.Replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	apiresponse.OK(c, installResponse{AgentID: out.AgentID, SquadID: out.SquadID})
}

func writeInstallationError(c *gin.Context, err error) {
	var field *pluginsvc.InstallationValidationError
	var upstream *fleet.CapabilityAPIError
	switch {
	case errors.As(err, &field):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "invalid installation input", map[string]any{"field": field.Field, "reason": field.Reason}, "Correct the installation input.")
	case errors.Is(err, fleet.ErrCapabilityInstallDisabled):
		apiresponse.Fail(c, http.StatusServiceUnavailable, errcode.UpstreamUnavailable, "capability installation is not enabled", map[string]any{"upstream": "fleet", "reason": "contract_pending"}, "Use the legacy endpoint for new operations until migration is enabled.")
	case errors.Is(err, pluginsvc.ErrNotFound):
		apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "plugin not found", nil, "")
	case errors.Is(err, pluginsvc.ErrDependencyHidden):
		apiresponse.Fail(c, http.StatusForbidden, errcode.PermissionDenied, "plugin dependency is not accessible", nil, "Ask the publisher to check dependency access.")
	case errors.Is(err, pluginsvc.ErrTooLarge):
		apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge, "installation exceeds resource limits", nil, "Reduce the plugin size or dependency count.")
	case errors.Is(err, pluginsvc.ErrInvalidRequest):
		validation(c, "body")
	case errors.As(err, &upstream):
		writeCapabilityUpstreamError(c, upstream)
	default:
		// A transport failure may follow a commit. Do not label it a clean
		// failure or tell clients to call a different installer/new key.
		apiresponse.Fail(c, http.StatusServiceUnavailable, errcode.UpstreamUnavailable, "capability installation could not be confirmed", map[string]any{"upstream": "fleet"}, "Retry with the same Idempotency-Key and input; do not switch installation endpoints.")
	}
}

func writeCapabilityUpstreamError(c *gin.Context, err *fleet.CapabilityAPIError) {
	status, code, message := err.Status, errcode.UpstreamUnavailable, "capability installation could not be confirmed"
	details := map[string]any{"upstream": "fleet"}
	switch status {
	case 400:
		code, message = errcode.BadRequest, "Fleet rejected the capability definition"
	case 401:
		code, message = errcode.Unauthorized, "authentication required"
	case 403:
		code, message = errcode.PermissionDenied, "installation is forbidden"
	case 404:
		code, message = errcode.NotFound, "installation target is unavailable"
	case 409:
		code, message = errcode.Conflict, "installation conflict"
		if err.Code == "DUPLICATE" {
			code = errcode.Duplicate
		}
		if err.Code == "IDEMPOTENCY_KEY_REUSED" {
			details["conflict_reason"] = "idempotency_key_reused"
		}
	case 413:
		code, message = errcode.FileTooLarge, "installation exceeds Fleet limits"
	case 415:
		code, message = "UNSUPPORTED_MEDIA_TYPE", "Fleet rejected the media type"
	case 429:
		code, message = errcode.RateLimited, "installation rate limit exceeded"
		if seconds, parseErr := strconv.Atoi(strings.TrimSpace(err.RetryAfter)); parseErr == nil && seconds >= 0 {
			c.Header("Retry-After", strconv.Itoa(seconds))
		}
	default:
		status = http.StatusServiceUnavailable
	}
	apiresponse.Fail(c, status, code, message, details, "Retry uncertain outcomes with the same Idempotency-Key and input.")
}
