package plugin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
	"unicode/utf8"
)

type installRequest struct {
	PluginID    string            `json:"plugin_id"`
	WorkspaceID string            `json:"workspace_id"`
	RuntimeID   string            `json:"runtime_id"`
	CustomEnv   map[string]string `json:"custom_env,omitempty"`
}

// installResponse carries exactly one created Loop resource id, matching the
// Plugin's type: agent_id for expert, squad_id for expert_team.
type installResponse struct {
	AgentID string `json:"agent_id,omitempty"`
	SquadID string `json:"squad_id,omitempty"`
}

// Install godoc
// @Summary Install plugin to a Loop workspace/runtime
// @Description Provision an expert or expert_team Plugin into the chosen Loop workspace/runtime, acting as the caller against octo-fleet with full rollback on partial failure.
// @Tags plugin
// @ID plugin.install
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body installRequest true "Plugin ID and target workspace + runtime"
// @Success 200 {object} apiresponse.Data[installResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 503 {object} apiresponse.Error "UPSTREAM_UNAVAILABLE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/install [post]
func (h *Handler) Install(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req installRequest
	if !decode(c, &req) {
		return
	}
	outcome, err := h.svc.Install(c.Request.Context(), caller, req.PluginID, pluginsvc.InstallParams{
		WorkspaceID: strings.TrimSpace(req.WorkspaceID),
		RuntimeID:   strings.TrimSpace(req.RuntimeID),
		CustomEnv:   req.CustomEnv,
		// The token is forwarded to fleet; middleware discarded it, so re-read it.
		Token: marketmiddleware.Token(c),
	})
	if err != nil {
		writeInstallError(c, err)
		return
	}
	apiresponse.OK(c, installResponse{AgentID: outcome.AgentID, SquadID: outcome.SquadID})
}

// writeInstallError maps install-specific failures on top of the shared
// catalog mapping: an unconfigured fleet → 503, fleet 4xx surfaced verbatim,
// fleet 5xx/transport collapsed to UPSTREAM_UNAVAILABLE so an upstream hiccup
// never masquerades as a client fault.
func writeInstallError(c *gin.Context, err error) {
	if errors.Is(err, expertsvc.ErrFleetNotConfigured) {
		apiresponse.Fail(c, http.StatusServiceUnavailable, errcode.UpstreamUnavailable, "loop service is not configured", nil, "")
		return
	}
	if errors.Is(err, expertsvc.ErrInstallTooLarge) {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "install exceeds resource limits", nil, "")
		return
	}
	var apiErr *fleet.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status >= 400 && apiErr.Status < 500 {
			apiresponse.Fail(c, apiErr.Status, installErrCode(apiErr.Status), fleetErrorMessage(apiErr), nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusServiceUnavailable, errcode.UpstreamUnavailable, "loop service is unavailable", nil, "")
		return
	}
	if errors.Is(err, expertsvc.ErrInvalidRequest) {
		validation(c, "body")
		return
	}
	// ErrTooLarge on the install path is a relation-topology cap
	// (maxInstallRelationTargets), not an upload: the shared catalog mapping's
	// "reduce the attachment size" hint would send the caller looking in the
	// wrong place, so surface the real cause here (P2-2).
	if errors.Is(err, pluginsvc.ErrTooLarge) {
		apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge,
			"install expands to too many skills or team members",
			map[string]any{"resource": "plugin"},
			"Reduce the number of skills or team members in this plugin and try again.")
		return
	}
	// ErrDependencyHidden: the plugin declares a relation target the caller
	// cannot see, so the full published topology cannot be reproduced. Refuse
	// loudly (P1-1) with an actionable, non-leaking message rather than
	// provisioning a partial expert/squad.
	if errors.Is(err, pluginsvc.ErrDependencyHidden) {
		apiresponse.Fail(c, http.StatusForbidden, errcode.PermissionDenied,
			"this plugin depends on resources you cannot access",
			map[string]any{"resource": "plugin"},
			"Ask the plugin owner to widen the visibility of its dependencies, then install again.")
		return
	}
	writeServiceError(c, err, "plugin.install")
}

func installErrCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return errcode.Unauthorized
	case http.StatusForbidden:
		return errcode.PermissionDenied
	case http.StatusNotFound:
		return errcode.NotFound
	case http.StatusConflict:
		return errcode.Conflict
	default:
		return errcode.BadRequest
	}
}

// fleetErrorMessage returns fleet's message capped on a rune boundary (fleet
// returns Chinese text) with a safe fallback.
func fleetErrorMessage(e *fleet.APIError) string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		return "loop service rejected the request"
	}
	const maxBytes = 200
	if len(msg) <= maxBytes {
		return msg
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(msg[end]) {
		end--
	}
	return msg[:end]
}
