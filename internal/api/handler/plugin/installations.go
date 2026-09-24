package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type capabilityInstallationService interface {
	CreateInstallation(context.Context, pluginsvc.Caller, string, pluginsvc.InstallationParams) (*pluginsvc.InstallationOutcome, error)
}

type createInstallationRequest struct {
	ResourceName string                          `json:"resource_name,omitempty" maxLength:"128"`
	RuntimeID    string                          `json:"runtime_id" binding:"required" format:"uuid"`
	CustomEnv    map[string]installationEnvValue `json:"custom_env,omitempty" swaggertype:"object,string"`
}

// CreateInstallation godoc
// @Summary Install a plugin through Fleet capability installation
// @Description Resolves and installs the visible expert or expert_team graph atomically in Fleet; retry uncertain outcomes with the same Idempotency-Key and input within Fleet's 24-hour retention window. Disabled by default until rollout; the legacy /plugins/install remains available. Generated error text is Chinese; 409 preserves Fleet's conflict message.
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
// @Header 200 {string} Idempotency-Replayed "Only forwarded when Fleet explicitly marks a replay; absence does not prove a new installation"
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT or DUPLICATE"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 415 {object} apiresponse.Error "UNSUPPORTED_MEDIA_TYPE"
// @Failure 429 {object} apiresponse.Error "RATE_LIMITED"
// @Header 429 {string} Retry-After "Delay in seconds before retrying"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Failure 503 {object} apiresponse.Error "UPSTREAM_UNAVAILABLE"
// @Router /plugins/{plugin_id}/installations [post]
func (h *Handler) CreateInstallation(c *gin.Context) {
	user, ok := caller(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "登录状态无效或已过期，请重新登录。", nil, "请使用有效的用户身份重试。")
		return
	}
	if user.BotUID != "" {
		apiresponse.Fail(c, http.StatusForbidden, errcode.PermissionDenied, "安装专家需要使用用户身份。", nil, "请使用已登录的用户账号重试。")
		return
	}
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" || (c.GetHeader("Content-Encoding") != "" && c.GetHeader("Content-Encoding") != "identity") {
		apiresponse.Fail(c, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "安装请求必须使用未压缩的 JSON 格式。", nil, "请使用 application/json，并移除内容压缩。")
		return
	}
	// This route uses only the explicit Workspace ID. Reject the legacy slug
	// selector rather than silently ignoring a conflicting second target.
	if c.GetHeader("X-Workspace-Slug") != "" || len(c.Request.Header.Values("X-Workspace-ID")) != 1 || len(c.Request.Header.Values("Idempotency-Key")) != 1 {
		installationValidation(c, "headers")
		return
	}
	var req createInstallationRequest
	if !decodeInstallation(c, &req) {
		return
	}
	var customEnv map[string]string
	if req.CustomEnv != nil {
		customEnv = make(map[string]string, len(req.CustomEnv))
		for key, value := range req.CustomEnv {
			customEnv[key] = string(value)
		}
	}
	p, err := pluginsvc.NormalizeInstallationParams(pluginsvc.InstallationParams{WorkspaceID: c.GetHeader("X-Workspace-ID"), RuntimeID: req.RuntimeID, ResourceName: req.ResourceName, CustomEnv: customEnv, IdempotencyKey: c.GetHeader("Idempotency-Key"), Token: marketmiddleware.Token(c)})
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
		writeInstallationError(c, &pluginsvc.InstallationAttemptError{Err: errors.New("installation returned no result")})
		return
	}
	if out.Replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	apiresponse.OK(c, installResponse{AgentID: out.AgentID, SquadID: out.SquadID})
}

func installationValidation(c *gin.Context, field string) {
	apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "安装请求参数不正确。", map[string]any{"field": field, "reason": "invalid"}, "请检查安装参数后重试。")
}

// Keep this route's client-facing Chinese errors isolated from legacy callers.
func decodeInstallation(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(dst)
	if err == nil {
		err = decoder.Decode(&struct{}{})
		if errors.Is(err, io.EOF) {
			return true
		}
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge, "安装请求内容过大。", map[string]any{"max_bytes": maxBodyBytes}, "请减少请求内容后重试。")
	} else {
		installationValidation(c, "body")
	}
	return false
}

const installationRetryHint = "请在首次请求后24小时内使用相同的幂等键和安装参数重试，不要切换安装接口，以免重复安装；超过24小时请先核对工作区已有资源。"

func writeInstallationError(c *gin.Context, err error) {
	var field *pluginsvc.InstallationValidationError
	var upstream *fleet.CapabilityAPIError
	var attempt *pluginsvc.InstallationAttemptError
	switch {
	case errors.As(err, &field):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "安装参数不正确。", map[string]any{"field": field.Field, "reason": field.Reason}, "请检查工作区、运行环境、名称和环境变量后重试。")
	case errors.Is(err, fleet.ErrCapabilityInstallDisabled):
		apiresponse.Fail(c, http.StatusServiceUnavailable, errcode.UpstreamUnavailable, "新的安装接口暂未开放。", map[string]any{"upstream": "fleet", "reason": "not_enabled"}, "请等待接口开放；新安装任务仍可使用原安装入口，结果未确认的任务请勿切换入口。")
	case errors.Is(err, pluginsvc.ErrNotFound):
		apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "插件不存在或不可访问。", nil, "请刷新插件列表后重试。")
	case errors.Is(err, pluginsvc.ErrDependencyHidden):
		apiresponse.Fail(c, http.StatusForbidden, errcode.PermissionDenied, "没有权限访问插件所需的依赖。", nil, "请联系插件发布者检查依赖的访问权限。")
	case errors.Is(err, pluginsvc.ErrTooLarge):
		apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge, "安装内容超出限制。", nil, "请减少插件文件大小或依赖数量后重试。")
	case errors.Is(err, pluginsvc.ErrInvalidRequest):
		installationValidation(c, "body")
	case errors.As(err, &upstream):
		writeCapabilityUpstreamError(c, upstream)
	case errors.As(err, &attempt):
		// A transport failure may follow a commit. Do not label it a clean
		// failure or tell clients to call a different installer/new key.
		logInstallationFailure(c, "fleet", "outcome_unconfirmed", 0)
		apiresponse.Fail(c, http.StatusServiceUnavailable, errcode.UpstreamUnavailable, "暂时无法确认安装结果。", map[string]any{"upstream": "fleet", "phase": "fleet"}, installationRetryHint)
	default:
		reason, message := "preparation_failed", "安装准备失败，请稍后重试。"
		if errors.Is(err, pluginsvc.ErrIntegrity) {
			reason, message = "artifact_unavailable", "安装文件读取或校验失败，请稍后重试或联系插件发布者。"
		}
		logInstallationFailure(c, "preparation", reason, 0)
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, message, map[string]any{"phase": "preparation", "reason": reason}, installationRetryHint)
	}
}

func logInstallationFailure(c *gin.Context, phase, reason string, upstreamStatus int) {
	logging.Error("capability_installation_failed", append(logging.RequestFields(c),
		zap.String("operation", "plugin.installation.create"), zap.String("phase", phase),
		zap.String("reason", reason), zap.Int("upstream_status", upstreamStatus),
	)...)
}

func writeCapabilityUpstreamError(c *gin.Context, err *fleet.CapabilityAPIError) {
	status, code, message := err.Status, errcode.UpstreamUnavailable, "暂时无法确认安装结果。"
	details := map[string]any{"upstream": "fleet", "phase": "fleet"}
	switch status {
	case 400:
		code, message = errcode.BadRequest, "安装内容未通过校验，请检查插件配置。"
	case 401:
		code, message = errcode.Unauthorized, "登录状态无效或已过期，请重新登录。"
	case 403:
		code, message = errcode.PermissionDenied, "没有权限在所选工作区或运行环境中安装。"
	case 404:
		code, message = errcode.NotFound, "所选工作区或运行环境不存在或不可访问。"
	case 409:
		code, message = errcode.Conflict, "安装发生冲突，请检查已有资源或安装参数。"
		if strings.TrimSpace(err.Message) != "" {
			message = err.Message
		}
		if err.Code == "DUPLICATE" {
			code = errcode.Duplicate
		}
		if err.IdempotencyKeyReused || err.Code == "IDEMPOTENCY_KEY_REUSED" {
			details["conflict_reason"] = "idempotency_key_reused"
		}
	case 413:
		code, message = errcode.FileTooLarge, "安装内容超出服务允许的大小或数量限制。"
	case 415:
		code, message = "UNSUPPORTED_MEDIA_TYPE", "安装服务不支持当前请求格式。"
	case 429:
		code, message = errcode.RateLimited, "安装请求过于频繁，请稍后重试。"
		if seconds, parseErr := strconv.Atoi(strings.TrimSpace(err.RetryAfter)); parseErr == nil && seconds >= 0 {
			c.Header("Retry-After", strconv.Itoa(seconds))
		}
	default:
		status = http.StatusServiceUnavailable
		logInstallationFailure(c, "fleet", "upstream_error", err.Status)
	}
	apiresponse.Fail(c, status, code, message, details, installationRetryHint)
}
