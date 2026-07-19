package upload

import (
	"errors"
	"log"
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/gin-gonic/gin"
)

// Handler handles HTTP requests for upload, parse, and download.
type Handler struct {
	parseSvc     *parse.Service
	skillSvc     *skillsvc.Service
	localStorage *storage.LocalStorage // nil when not using local storage
	maxUploadMB  int
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

// Register registers upload/parse/download routes.
func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.POST("/skill_uploads", h.InitUpload)
	rg.POST("/skill_icon_uploads", h.InitIconUpload)
	rg.POST("/skill_uploads/:skill_upload_id/parse", h.TriggerParse)
	rg.GET("/skill_parse_tasks/:skill_parse_task_id", h.PollParse)
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

// initRequest is the JSON body for POST /api/v1/skill/upload/init.
type InitUploadRequest struct {
	FileName string `json:"file_name" binding:"required"`
	FileSize int64  `json:"file_size" binding:"required"`
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
	FileSize int64  `json:"file_size" binding:"required"`
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
	FileSize int64  `json:"file_size" binding:"required"`
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

	if c.Query("format") == "json" {
		apiresponse.OK(c, DownloadResponse{DownloadURL: info.DownloadURL, FileSHA256: info.FileSHA256})
		return
	}

	c.Redirect(http.StatusFound, info.DownloadURL)
}
