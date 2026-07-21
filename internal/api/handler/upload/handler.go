package upload

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	metricssvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/metrics"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/gin-gonic/gin"
)

// Handler handles HTTP requests for upload, parse, and download.
type Handler struct {
	parseSvc     *parse.Service
	skillSvc     *skillsvc.Service
	metricsSvc   *metricssvc.Service
	localStorage *storage.LocalStorage // nil when not using local storage
	maxUploadMB  int
	devBotMode   bool
}

// New creates an upload handler.
func New(parseSvc *parse.Service, skillSvc *skillsvc.Service, localStorage *storage.LocalStorage, maxUploadMB ...int) *Handler {
	maxMB := 20
	if len(maxUploadMB) > 0 && maxUploadMB[0] > 0 {
		maxMB = maxUploadMB[0]
	}
	return &Handler{
		parseSvc:     parseSvc,
		skillSvc:     skillSvc,
		localStorage: localStorage,
		maxUploadMB:  maxMB,
	}
}

// SetMetricsService sets the metrics service for download tracking.
func (h *Handler) SetMetricsService(svc *metricssvc.Service) {
	h.metricsSvc = svc
}

// Register registers upload/parse/download routes.
func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.POST("/skill_uploads", h.InitUpload)
	rg.POST("/skill_icon_uploads", h.InitIconUpload)
	rg.POST("/skill_uploads/:skill_upload_id/parse", h.TriggerParse)
	rg.GET("/skill_parse_tasks/:skill_parse_task_id", h.PollParse)
	rg.POST("/bot/skills/publish", h.BotPublishSkill)
	rg.POST("/skills/:skill_id/reuploads", h.InitReupload)
	rg.GET("/skills/:skill_id/download", h.Download)

	legacy := rg.Group("/skill", legacyUploadEndpoint("/api/v1/skills"))
	legacy.POST("/upload/init", h.InitUpload)
	legacy.POST("/upload/icon", h.InitIconUpload)
	legacy.POST("/upload/:skill_upload_id/parse", h.TriggerParse)
	legacy.GET("/parse/:skill_parse_task_id", h.PollParse)
	legacy.POST("/:skill_id/reupload/init", h.InitReupload)
	legacy.GET("/:skill_id/download", h.Download)
}

// SetDevBotMode enables local-only BotIdentity fallback when AUTH_ENABLED=false.
func (h *Handler) SetDevBotMode(enabled bool) {
	h.devBotMode = enabled
}

// RegisterAdmin registers admin upload/parse/download routes on the admin group.
// These reuse the same handlers since the admin auth middleware injects identity
// in the same way, and the admin is treated as the ownerID of the upload.
func (h *Handler) RegisterAdmin(r *gin.Engine, adminAuth *middleware.AdminAuthenticator) {
	admin := r.Group("/api/v1/admin", adminAuth.Handler())
	admin.POST("/skill_uploads", h.InitUpload)
	admin.POST("/skill_uploads/:skill_upload_id/parse", h.TriggerParse)
	admin.GET("/skill_parse_tasks/:skill_parse_task_id", h.PollParse)
	admin.GET("/skills/:skill_id/download", h.AdminDownload)
}

func legacyUploadEndpoint(successor string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Deprecation", "true")
		c.Header("Sunset", "Thu, 01 Oct 2026 00:00:00 GMT")
		c.Header("Link", "<"+successor+">; rel=\"successor-version\"")
		c.Next()
	}
}

// RegisterLocalProxy registers local storage proxy routes (only for STORAGE_DRIVER=local).
func (h *Handler) RegisterLocalProxy(r *gin.Engine, authEnabled ...bool) {
	if h.localStorage == nil || (len(authEnabled) > 0 && authEnabled[0]) {
		return
	}
	r.PUT("/api/v1/_storage/upload/*key", localProxyLoopbackOnly, h.localUploadProxy)
	r.GET("/api/v1/_storage/download/*key", localProxyLoopbackOnly, h.localDownloadProxy)
}

// BotPublishSkill godoc
// @Summary Publish Skill as bot
// @Description Parse an uploaded Skill archive synchronously through the bounded worker pool, then create a Skill owned by the bot owner.
// @Tags skill_upload
// @ID bot_skill.publish
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body BotPublishSkillRequest true "Bot publish request"
// @Success 201 {object} apiresponse.Data[skillsvc.SkillItem]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /bot/skills/publish [post]
func (h *Handler) BotPublishSkill(c *gin.Context) {
	if h.parseSvc == nil || h.skillSvc == nil {
		apiresponse.Fail(c, http.StatusServiceUnavailable, errcode.InternalError, "skill publishing is unavailable", nil, "")
		return
	}
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	bot, ok := h.botIdentity(c, identity)
	if !ok {
		apiresponse.Fail(c, http.StatusForbidden, errcode.PermissionDenied, "bot token is required", nil, "")
		return
	}

	var req BotPublishSkillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "invalid request body", nil, "")
		return
	}

	mode := strings.TrimSpace(req.PublishMode)
	if mode == "" {
		mode = "create"
	}
	if mode != "create" {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "only publish_mode=create is supported", nil, "")
		return
	}

	uploadID := strings.TrimSpace(req.SkillUploadID)
	if uploadID == "" {
		uploadID = uploadIDFromLink(req.UploadURL)
	}
	if uploadID == "" {
		uploadID = uploadIDFromLink(req.PresignedURL)
	}
	if uploadID == "" {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "skill_upload_id or upload_url is required", nil, "")
		return
	}

	tags := marshalPublishTags(req.Tags)
	result, err := h.parseSvc.ParseUploadSync(c.Request.Context(), uploadID, identity.UID)
	if err != nil {
		if errors.Is(err, parse.ErrTaskNotFound) || errors.Is(err, parse.ErrForbidden) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "upload not found", nil, "")
			return
		}
		if errors.Is(err, parse.ErrTaskNotPending) {
			apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "upload cannot be published from its current parse status", nil, "")
			return
		}
		log.Printf("[BotPublishSkill] parse upload failed: %v", err)
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}
	if result.Status != "success" {
		code := ""
		message := "parse failed"
		if result.Error != nil {
			code = result.Error.Code
			message = result.Error.Message
		}
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, message, map[string]any{"parse_error_code": code}, "")
		return
	}

	item, err := h.skillSvc.Create(c.Request.Context(), skillsvc.CreateParams{
		ParseTaskID: result.TaskID,
		Name:        req.Name,
		DisplayName: req.DisplayName,
		IconURL:     req.IconURL,
		Description: req.Description,
		CategoryID:  req.CategoryID,
		Tags:        tags,
		Visibility:  req.Visibility,
		Version:     req.Version,
		Changelog:   req.Changelog,
		UserID:      identity.UID,
		UserName:    identity.Name,
		SpaceID:     bot.SpaceID,
		CreatorID:   bot.BotUID,
		CreatorName: bot.BotName,
	})
	if err != nil {
		if errors.Is(err, skillsvc.ErrInvalidParseTask) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "invalid or unavailable parse task", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrParseTaskConsumed) {
			apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "parse task already consumed", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrCategoryNotFound) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "category not found", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrNameTaken) {
			apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "skill name already exists", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrInvalidTags) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "tags must be a JSON string array", nil, "")
			return
		}
		log.Printf("[BotPublishSkill] create skill failed: %v", err)
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	apiresponse.Created(c, item)
}

type BotPublishSkillRequest struct {
	SkillUploadID string   `json:"skill_upload_id"`
	UploadURL     string   `json:"upload_url"`
	PresignedURL  string   `json:"presigned_url"`
	PublishMode   string   `json:"publish_mode"`
	Name          string   `json:"name"`
	DisplayName   string   `json:"display_name"`
	IconURL       string   `json:"icon_url"`
	Description   string   `json:"description"`
	CategoryID    string   `json:"category_id"`
	Tags          []string `json:"tags"`
	Visibility    string   `json:"visibility"`
	Version       string   `json:"version"`
	Changelog     string   `json:"changelog"`
}

func (h *Handler) botIdentity(c *gin.Context, identity model.Identity) (model.BotIdentity, bool) {
	if bot, ok := middleware.BotIdentity(c); ok {
		return bot, true
	}
	if !h.devBotMode || !strings.HasPrefix(requestToken(c), "bf_") {
		return model.BotIdentity{}, false
	}
	botUID := strings.TrimSpace(c.GetHeader("X-Dev-Bot-Uid"))
	if botUID == "" {
		botUID = "dev-bot"
	}
	botName := strings.TrimSpace(c.GetHeader("X-Dev-Bot-Name"))
	if botName == "" {
		botName = "Dev Bot"
	}
	return model.BotIdentity{
		BotUID:    botUID,
		BotName:   botName,
		OwnerUID:  identity.UID,
		OwnerName: identity.Name,
		SpaceID:   middleware.SpaceID(c),
	}, true
}

func requestToken(c *gin.Context) string {
	if token := strings.TrimSpace(c.GetHeader("Token")); token != "" {
		return token
	}
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if len(authorization) > 7 && strings.EqualFold(authorization[:7], "Bearer ") {
		return strings.TrimSpace(authorization[7:])
	}
	return ""
}

func normalizePublishTags(input json.RawMessage) (json.RawMessage, error) {
	if len(input) == 0 {
		return nil, nil
	}
	var tags []string
	if err := json.Unmarshal(input, &tags); err != nil {
		return nil, err
	}
	out, _ := json.Marshal(tags)
	return out, nil
}

func marshalPublishTags(tags []string) json.RawMessage {
	if tags == nil {
		return nil
	}
	out, _ := json.Marshal(tags)
	return out
}

func uploadIDFromLink(link string) string {
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	const marker = "skill-uploads/"
	idx := strings.Index(link, marker)
	if idx < 0 {
		return ""
	}
	rest := link[idx+len(marker):]
	if slash := strings.Index(rest, "/"); slash >= 0 {
		rest = rest[:slash]
	}
	if q := strings.IndexAny(rest, "?#"); q >= 0 {
		rest = rest[:q]
	}
	return strings.TrimSpace(rest)
}

// initRequest is the JSON body for POST /api/v1/skill/upload/init.
type InitUploadRequest struct {
	FileName string `json:"file_name" binding:"required"`
	FileSize int64  `json:"file_size" binding:"required,gt=0"`
}

// InitUpload godoc
// @Summary Initialize Skill upload
// @Description Create a bounded upload target for a Skill archive without publishing it.
// @Tags skill_upload
// @ID skill_upload.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body InitUploadRequest true "Archive metadata"
// @Success 200 {object} apiresponse.Data[parse.InitResult]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skill_uploads [post]
func (h *Handler) InitUpload(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)

	var req InitUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "file_name and file_size are required", nil, "")
		return
	}

	result, err := h.parseSvc.InitUpload(c.Request.Context(), req.FileName, req.FileSize, identity.UID, spaceID)
	if err != nil {
		if errors.Is(err, parse.ErrInvalidFileName) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "file_name must end with .zip", nil, "")
			return
		}
		if errors.Is(err, parse.ErrInvalidFileSize) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "file_size must be positive", nil, "")
			return
		}
		if errors.Is(err, parse.ErrFileTooLarge) {
			apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge, "file exceeds upload size limit", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	apiresponse.OK(c, result)
}

// TriggerParse godoc
// @Summary Parse Skill upload
// @Description Start parsing a previously initialized Skill archive upload.
// @Tags skill_upload
// @ID skill_upload.parse
// @Accept json
// @Produce json
// @Security Bearer
// @Param skill_upload_id path string true "Skill upload ID"
// @Success 200 {object} apiresponse.Data[map[string]string]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 429 {object} apiresponse.Error "RATE_LIMITED"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skill_uploads/{skill_upload_id}/parse [post]
func (h *Handler) TriggerParse(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}

	uploadID := c.Param("skill_upload_id")
	taskID, err := h.parseSvc.TriggerParse(c.Request.Context(), uploadID, identity.UID)
	if err != nil {
		if errors.Is(err, parse.ErrTaskNotFound) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "upload not found", nil, "")
			return
		}
		if errors.Is(err, parse.ErrForbidden) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "upload not found", nil, "")
			return
		}
		if errors.Is(err, parse.ErrTaskNotPending) {
			apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "parse already triggered", nil, "")
			return
		}
		if errors.Is(err, parse.ErrParseQueueFull) {
			apiresponse.Fail(c, http.StatusTooManyRequests, errcode.RateLimited, "parse queue is busy, retry later", nil, "")
			return
		}
		log.Printf("[TriggerParse] internal error for uploadID=%s: %v", uploadID, err)
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	apiresponse.OK(c, gin.H{"skill_parse_task_id": taskID})
}

// PollParse godoc
// @Summary Get Skill parse task
// @Description Return the current parse state for one Skill archive upload.
// @Tags skill_upload
// @ID skill_parse_task.get
// @Accept json
// @Produce json
// @Security Bearer
// @Param skill_parse_task_id path string true "Skill parse task ID"
// @Success 200 {object} apiresponse.Data[parse.PollResult]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skill_parse_tasks/{skill_parse_task_id} [get]
func (h *Handler) PollParse(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}

	taskID := c.Param("skill_parse_task_id")
	result, err := h.parseSvc.GetParseStatus(c.Request.Context(), taskID, identity.UID)
	if err != nil {
		if errors.Is(err, parse.ErrTaskNotFound) || errors.Is(err, parse.ErrForbidden) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "task not found", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	apiresponse.OK(c, result)
}

// iconUploadRequest is the JSON body for POST /api/v1/skill/upload/icon.
type IconUploadRequest struct {
	FileName string `json:"file_name" binding:"required"`
	FileSize int64  `json:"file_size" binding:"required,gt=0"`
}

// InitIconUpload godoc
// @Summary Initialize Skill icon upload
// @Description Create an upload target for a Skill icon without changing a Skill record.
// @Tags skill_upload
// @ID skill_icon_upload.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body IconUploadRequest true "Icon metadata"
// @Success 200 {object} apiresponse.Data[parse.IconUploadResult]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skill_icon_uploads [post]
func (h *Handler) InitIconUpload(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}

	var req IconUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "file_name and file_size are required", nil, "")
		return
	}

	result, err := h.parseSvc.InitIconUpload(c.Request.Context(), req.FileName, req.FileSize, identity.UID)
	if err != nil {
		if errors.Is(err, parse.ErrInvalidFileSize) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "file_size must be positive", nil, "")
			return
		}
		if errors.Is(err, parse.ErrFileTooLarge) {
			apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge, "icon exceeds 2MB limit", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "invalid icon upload request", nil, "")
		return
	}

	apiresponse.OK(c, result)
}

// reuploadRequest is the JSON body for POST /api/v1/skill/:id/reupload/init.
type ReuploadRequest struct {
	FileName string `json:"file_name" binding:"required"`
	FileSize int64  `json:"file_size" binding:"required,gt=0"`
}

// InitReupload godoc
// @Summary Initialize Skill reupload
// @Description Create an upload target for a new immutable version of an owned Skill.
// @Tags skill_upload
// @ID skill.reupload.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param skill_id path string true "Skill ID"
// @Param body body ReuploadRequest true "Archive metadata"
// @Success 200 {object} apiresponse.Data[parse.InitResult]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skills/{skill_id}/reuploads [post]
func (h *Handler) InitReupload(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)
	skillID := c.Param("skill_id")

	// Check ownership
	skill, err := h.skillSvc.Get(c.Request.Context(), skillID, spaceID, identity.UID)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "not found", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}
	if skill.OwnerID != identity.UID {
		apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "not found", nil, "")
		return
	}

	var req ReuploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "file_name and file_size are required", nil, "")
		return
	}

	result, err := h.parseSvc.InitReupload(c.Request.Context(), skillID, req.FileName, req.FileSize, identity.UID, spaceID)
	if err != nil {
		if errors.Is(err, parse.ErrInvalidFileName) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "file_name must end with .zip", nil, "")
			return
		}
		if errors.Is(err, parse.ErrInvalidFileSize) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "file_size must be positive", nil, "")
			return
		}
		if errors.Is(err, parse.ErrFileTooLarge) {
			apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge, "file exceeds upload size limit", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	apiresponse.OK(c, result)
}

// DownloadResponse contains the authorized artifact URL and integrity digest.
type DownloadResponse struct {
	DownloadURL string `json:"download_url"`
	FileSHA256  string `json:"file_sha256"`
}

// Download godoc
// @Summary Download Skill archive
// @Description Authorize access and redirect to the artifact URL, or return it when format=json.
// @Tags skill
// @ID skill.download
// @Accept json
// @Produce json
// @Security Bearer
// @Param skill_id path string true "Skill ID"
// @Param format query string false "Use json to return the download URL"
// @Success 200 {object} apiresponse.Data[DownloadResponse]
// @Success 302 "Redirect to artifact"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skills/{skill_id}/download [get]
func (h *Handler) Download(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)
	skillID := c.Param("skill_id")

	info, err := h.skillSvc.GetDownloadInfo(c.Request.Context(), skillID, spaceID, identity.UID)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "not found", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrNoFile) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "no file available", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	// Track download after successful URL generation — fire-and-forget.
	if h.metricsSvc != nil {
		if trackErr := h.metricsSvc.TrackDownload(c.Request.Context(), "skill", skillID); trackErr != nil {
			log.Printf("[download] WARN: TrackDownload failed for skill %s: %v", skillID, trackErr)
		}
	}

	if c.Query("format") == "json" {
		apiresponse.OK(c, DownloadResponse{DownloadURL: info.DownloadURL, FileSHA256: info.FileSHA256})
		return
	}

	c.Redirect(http.StatusFound, info.DownloadURL)
}

// AdminDownload godoc
// @Summary Download public Skill archive (admin)
// @Description Return a presigned artifact URL for a public Skill, or redirect to it unless format=json is requested.
// @Tags admin_skill
// @ID admin_skill.download
// @Accept json
// @Produce json
// @Security Bearer
// @Param skill_id path string true "Skill ID"
// @Param format query string false "Use json to return the download URL"
// @Success 200 {object} apiresponse.Data[DownloadResponse]
// @Success 302 "Redirect to artifact"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/skills/{skill_id}/download [get]
func (h *Handler) AdminDownload(c *gin.Context) {
	if _, ok := middleware.Identity(c); !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "authentication is required", nil, "")
		return
	}
	skillID := c.Param("skill_id")

	info, err := h.skillSvc.AdminGetDownloadInfo(c.Request.Context(), skillID)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "not found", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrNoFile) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "no file available", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	if c.Query("format") == "json" {
		apiresponse.OK(c, DownloadResponse{DownloadURL: info.DownloadURL, FileSHA256: info.FileSHA256})
		return
	}

	c.Redirect(http.StatusFound, info.DownloadURL)
}
