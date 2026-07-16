package upload

import (
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
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
}

// New creates an upload handler.
func New(parseSvc *parse.Service, skillSvc *skillsvc.Service, localStorage *storage.LocalStorage) *Handler {
	return &Handler{
		parseSvc:     parseSvc,
		skillSvc:     skillSvc,
		localStorage: localStorage,
	}
}

// Register registers upload/parse/download routes.
func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.POST("/skill/upload/init", h.InitUpload)
	rg.POST("/skill/upload/icon", h.InitIconUpload)
	rg.POST("/skill/upload/:uploadId/parse", h.TriggerParse)
	rg.GET("/skill/parse/:taskId", h.PollParse)
	rg.POST("/skill/:id/reupload/init", h.InitReupload)
	rg.GET("/skill/:id/download", h.Download)
}

// RegisterLocalProxy registers local storage proxy routes (only for STORAGE_DRIVER=local).
func (h *Handler) RegisterLocalProxy(r *gin.Engine) {
	if h.localStorage == nil {
		return
	}
	r.PUT("/api/v1/_storage/upload/*key", h.localUploadProxy)
	r.GET("/api/v1/_storage/download/*key", h.localDownloadProxy)
}

// initRequest is the JSON body for POST /api/v1/skill/upload/init.
type initRequest struct {
	FileName string `json:"file_name" binding:"required"`
	FileSize int64  `json:"file_size" binding:"required"`
}

// InitUpload handles POST /api/v1/skill/upload/init.
func (h *Handler) InitUpload(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}
	spaceID := middleware.SpaceID(c)

	var req initRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": "file_name and file_size are required"})
		return
	}

	result, err := h.parseSvc.InitUpload(c.Request.Context(), req.FileName, req.FileSize, identity.UID, spaceID)
	if err != nil {
		if errors.Is(err, parse.ErrInvalidFileName) {
			c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": "file_name must end with .zip"})
			return
		}
		if errors.Is(err, parse.ErrFileTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"code": errcode.FileTooLarge, "message": "file exceeds upload size limit"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": result,
	})
}

// TriggerParse handles POST /api/v1/skill/upload/:uploadId/parse.
func (h *Handler) TriggerParse(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}

	uploadID := c.Param("uploadId")
	taskID, err := h.parseSvc.TriggerParse(c.Request.Context(), uploadID, identity.UID)
	if err != nil {
		if errors.Is(err, parse.ErrTaskNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"code": errcode.NotFound, "message": "upload not found"})
			return
		}
		if errors.Is(err, parse.ErrForbidden) {
			c.JSON(http.StatusNotFound, gin.H{"code": errcode.NotFound, "message": "upload not found"})
			return
		}
		if errors.Is(err, parse.ErrTaskNotPending) {
			c.JSON(http.StatusConflict, gin.H{"code": errcode.Conflict, "message": "parse already triggered"})
			return
		}
		log.Printf("[TriggerParse] internal error for uploadID=%s: %v", uploadID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{"task_id": taskID},
	})
}

// PollParse handles GET /api/v1/skill/parse/:taskId.
func (h *Handler) PollParse(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}

	taskID := c.Param("taskId")
	result, err := h.parseSvc.GetParseStatus(c.Request.Context(), taskID, identity.UID)
	if err != nil {
		if errors.Is(err, parse.ErrTaskNotFound) || errors.Is(err, parse.ErrForbidden) {
			c.JSON(http.StatusNotFound, gin.H{"code": errcode.NotFound, "message": "task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": result,
	})
}

// iconUploadRequest is the JSON body for POST /api/v1/skill/upload/icon.
type iconUploadRequest struct {
	FileName string `json:"file_name" binding:"required"`
	FileSize int64  `json:"file_size" binding:"required"`
}

// InitIconUpload handles POST /api/v1/skill/upload/icon.
func (h *Handler) InitIconUpload(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}

	var req iconUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": "file_name and file_size are required"})
		return
	}

	result, err := h.parseSvc.InitIconUpload(c.Request.Context(), req.FileName, req.FileSize, identity.UID)
	if err != nil {
		if errors.Is(err, parse.ErrFileTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"code": errcode.FileTooLarge, "message": "icon exceeds 2MB limit"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": result,
	})
}

// reuploadRequest is the JSON body for POST /api/v1/skill/:id/reupload/init.
type reuploadRequest struct {
	FileName string `json:"file_name" binding:"required"`
	FileSize int64  `json:"file_size" binding:"required"`
}

// InitReupload handles POST /api/v1/skill/:id/reupload/init.
func (h *Handler) InitReupload(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}
	spaceID := middleware.SpaceID(c)
	skillID := c.Param("id")

	// Check ownership
	skill, err := h.skillSvc.Get(c.Request.Context(), skillID, spaceID, identity.UID)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"code": errcode.NotFound, "message": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}
	if skill.OwnerID != identity.UID {
		c.JSON(http.StatusNotFound, gin.H{"code": errcode.NotFound, "message": "not found"})
		return
	}

	var req reuploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": "file_name and file_size are required"})
		return
	}

	result, err := h.parseSvc.InitReupload(c.Request.Context(), skillID, req.FileName, req.FileSize, identity.UID, spaceID)
	if err != nil {
		if errors.Is(err, parse.ErrInvalidFileName) {
			c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": "file_name must end with .zip"})
			return
		}
		if errors.Is(err, parse.ErrFileTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"code": errcode.FileTooLarge, "message": "file exceeds upload size limit"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": result,
	})
}

// Download handles GET /api/v1/skill/:id/download.
func (h *Handler) Download(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}
	spaceID := middleware.SpaceID(c)
	skillID := c.Param("id")

	skill, err := h.skillSvc.Get(c.Request.Context(), skillID, spaceID, identity.UID)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"code": errcode.NotFound, "message": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	if skill.FileURL == "" {
		c.JSON(http.StatusNotFound, gin.H{"code": errcode.NotFound, "message": "no file available"})
		return
	}

	downloadURL, err := h.parseSvc.GetDownloadURL(c.Request.Context(), skill.FileURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}
	if c.Query("format") == "json" {
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"url": downloadURL}})
		return
	}

	c.Redirect(http.StatusFound, downloadURL)
}

// localUploadProxy handles PUT to local storage (development mode).
func (h *Handler) localUploadProxy(c *gin.Context) {
	key := c.Param("key")
	if key != "" && key[0] == '/' {
		key = key[1:]
	}

	if err := h.localStorage.WriteObject(key, c.Request.Body); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write failed"})
		return
	}
	c.Status(http.StatusOK)
}

// localDownloadProxy handles GET from local storage (development mode).
func (h *Handler) localDownloadProxy(c *gin.Context) {
	key := c.Param("key")
	if key != "" && key[0] == '/' {
		key = key[1:]
	}

	rc, err := h.localStorage.GetObject(c.Request.Context(), key)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	defer rc.Close()

	c.Header("Content-Type", "application/octet-stream")
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, rc)
}
