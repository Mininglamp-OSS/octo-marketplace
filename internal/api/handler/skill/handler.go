package skill

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/gin-gonic/gin"
)

// Handler handles HTTP requests for skills.
type Handler struct {
	svc *skillsvc.Service
}

// New creates a new skill handler.
func New(svc *skillsvc.Service) *Handler {
	return &Handler{svc: svc}
}

// Register registers skill routes on the given router group.
func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.GET("/skill/mine", h.ListMine)
	rg.GET("/skill", h.List)
	rg.GET("/skill/:id", h.Get)
	rg.GET("/skill/:id/versions", h.ListVersions)
	rg.POST("/skill", h.Create)
	rg.PUT("/skill/:id", h.Update)
	rg.DELETE("/skill/:id", h.Delete)
}

// List handles GET /api/v1/skill
func (h *Handler) List(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}
	spaceID := middleware.SpaceID(c)
	limit := parseLimit(c.Query("limit"))

	result, err := h.svc.List(c.Request.Context(), skillsvc.ListParams{
		SpaceID:    spaceID,
		UserID:     identity.UID,
		Query:      c.Query("q"),
		CategoryID: c.Query("category_id"),
		Cursor:     c.Query("cursor"),
		Limit:      limit,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"items":       result.Items,
			"next_cursor": result.NextCursor,
		},
	})
}

// ListMine handles GET /api/v1/skill/mine
func (h *Handler) ListMine(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}
	spaceID := middleware.SpaceID(c)
	limit := parseLimit(c.Query("limit"))

	result, err := h.svc.ListMine(c.Request.Context(), skillsvc.ListParams{
		SpaceID: spaceID,
		UserID:  identity.UID,
		Query:   c.Query("q"),
		Cursor:  c.Query("cursor"),
		Limit:   limit,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"items":       result.Items,
			"next_cursor": result.NextCursor,
		},
	})
}

// Get handles GET /api/v1/skill/:id
func (h *Handler) Get(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}
	spaceID := middleware.SpaceID(c)
	id := c.Param("id")

	item, err := h.svc.Get(c.Request.Context(), id, spaceID, identity.UID)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"code": errcode.NotFound, "message": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": item,
	})
}

// createRequest is the JSON body for POST /api/v1/skill.
type createRequest struct {
	ParseTaskID string          `json:"parse_task_id" binding:"required"`
	Name        string          `json:"name"`
	DisplayName string          `json:"display_name"`
	IconURL     string          `json:"icon_url"`
	Description string          `json:"description"`
	CategoryID  string          `json:"category_id"`
	Tags        json.RawMessage `json:"tags"`
	Visibility  string          `json:"visibility"`
	Version     string          `json:"version"`
}

// Create handles POST /api/v1/skill
func (h *Handler) Create(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}
	spaceID := middleware.SpaceID(c)

	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": "parse_task_id is required"})
		return
	}

	item, err := h.svc.Create(c.Request.Context(), skillsvc.CreateParams{
		ParseTaskID: req.ParseTaskID,
		Name:        req.Name,
		DisplayName: req.DisplayName,
		IconURL:     req.IconURL,
		Description: req.Description,
		CategoryID:  req.CategoryID,
		Tags:        req.Tags,
		Visibility:  req.Visibility,
		Version:     req.Version,
		UserID:      identity.UID,
		UserName:    identity.Name,
		SpaceID:     spaceID,
	})
	if err != nil {
		if errors.Is(err, skillsvc.ErrInvalidParseTask) {
			c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": "invalid or unavailable parse task"})
			return
		}
		if errors.Is(err, skillsvc.ErrParseTaskConsumed) {
			c.JSON(http.StatusConflict, gin.H{"code": errcode.Conflict, "message": "parse task already consumed"})
			return
		}
		if errors.Is(err, skillsvc.ErrCategoryNotFound) {
			c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": "category not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"code": 0,
		"data": item,
	})
}

// updateRequest is the JSON body for PUT /api/v1/skill/:id.
type updateRequest struct {
	Name        *string         `json:"name"`
	DisplayName *string         `json:"display_name"`
	IconURL     *string         `json:"icon_url"`
	Description *string         `json:"description"`
	CategoryID  *string         `json:"category_id"`
	Tags        json.RawMessage `json:"tags"`
	Visibility  *string         `json:"visibility"`
	Version     *string         `json:"version"`
	ParseTaskID string          `json:"parse_task_id"`
	Changelog   string          `json:"changelog"`
}

// Update handles PUT /api/v1/skill/:id
func (h *Handler) Update(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}
	spaceID := middleware.SpaceID(c)
	id := c.Param("id")

	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": "invalid request body"})
		return
	}

	item, err := h.svc.Update(c.Request.Context(), id, identity.UID, spaceID, skillsvc.UpdateParams{
		Name:        req.Name,
		DisplayName: req.DisplayName,
		IconURL:     req.IconURL,
		Description: req.Description,
		CategoryID:  req.CategoryID,
		Tags:        req.Tags,
		Visibility:  req.Visibility,
		Version:     req.Version,
		ParseTaskID: req.ParseTaskID,
		Changelog:   req.Changelog,
	})
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"code": errcode.NotFound, "message": "not found"})
			return
		}
		if errors.Is(err, skillsvc.ErrCategoryNotFound) {
			c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": "category not found"})
			return
		}
		if errors.Is(err, skillsvc.ErrInvalidParseTask) {
			c.JSON(http.StatusBadRequest, gin.H{"code": errcode.BadRequest, "message": "invalid or unavailable parse task"})
			return
		}
		if errors.Is(err, skillsvc.ErrParseTaskConsumed) {
			c.JSON(http.StatusConflict, gin.H{"code": errcode.Conflict, "message": "parse task already consumed"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": item,
	})
}

// Delete handles DELETE /api/v1/skill/:id
func (h *Handler) Delete(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}
	spaceID := middleware.SpaceID(c)
	id := c.Param("id")

	err := h.svc.Delete(c.Request.Context(), id, identity.UID, spaceID)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"code": errcode.NotFound, "message": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.Status(http.StatusNoContent)
}

// ListVersions handles GET /api/v1/skill/:id/versions
func (h *Handler) ListVersions(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}
	spaceID := middleware.SpaceID(c)
	id := c.Param("id")

	items, err := h.svc.ListVersions(c.Request.Context(), id, spaceID, identity.UID)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"code": errcode.NotFound, "message": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{"items": items},
	})
}

func parseLimit(s string) int {
	if s == "" {
		return 20
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 20
	}
	if n > 50 {
		return 50
	}
	return n
}
