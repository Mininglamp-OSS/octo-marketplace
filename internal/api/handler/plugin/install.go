package plugin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
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
// @Description Provision an expert or expert_team Plugin into the chosen Loop workspace/runtime, acting as the caller through the same atomic Fleet capability installation used by /plugins/{plugin_id}/installations. The original body and response are retained for older clients; Idempotency-Key is optional.
// @Tags plugin
// @ID plugin.install
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body installRequest true "Plugin ID and target workspace + runtime"
// @Param Idempotency-Key header string false "Optional stable installation key; omission starts a new operation"
// @Success 200 {object} apiresponse.Data[installResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 415 {object} apiresponse.Error "UNSUPPORTED_MEDIA_TYPE"
// @Failure 429 {object} apiresponse.Error "RATE_LIMITED"
// @Header 429 {string} Retry-After "Delay in seconds before retrying"
// @Failure 503 {object} apiresponse.Error "UPSTREAM_UNAVAILABLE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/install [post]
func (h *Handler) Install(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	if len(c.Request.Header.Values("Idempotency-Key")) > 1 {
		validation(c, "Idempotency-Key")
		return
	}
	var req installRequest
	if !decode(c, &req) {
		return
	}
	outcome, err := h.svc.Install(c.Request.Context(), caller, req.PluginID, pluginsvc.InstallParams{
		WorkspaceID:    strings.TrimSpace(req.WorkspaceID),
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RuntimeID:      strings.TrimSpace(req.RuntimeID),
		CustomEnv:      req.CustomEnv,
		// The token is forwarded to fleet; middleware discarded it, so re-read it.
		Token: marketmiddleware.Token(c),
	})
	if err != nil {
		var attempt *pluginsvc.InstallationAttemptError
		if c.GetHeader("Idempotency-Key") == "" && errors.As(err, &attempt) && attempt.Unknown {
			apiresponse.Fail(c, http.StatusServiceUnavailable, errcode.UpstreamUnavailable, "暂时无法确认安装结果。", map[string]any{"reason": "installation_result_unknown", "can_retry_same_key": false}, "本次旧接口请求没有幂等键。请先核对工作区已有资源，确认结果前不要再次发起安装。")
			return
		}
		writeInstallationError(c, err)
		return
	}
	apiresponse.OK(c, installResponse{AgentID: outcome.AgentID, SquadID: outcome.SquadID})
}
