package skill

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/gin-gonic/gin"
)

// Handler handles HTTP requests for skills.
type Handler struct {
	svc *skillsvc.Service
}

type SkillResponse = model.Skill

type SkillVersionList struct {
	Items []model.SkillVersion `json:"items"`
}

type SkillTagList struct {
	Items []skillsvc.TagItem `json:"items"`
}

// New creates a new skill handler.
func New(svc *skillsvc.Service) *Handler {
	return &Handler{svc: svc}
}

// Register registers skill routes on the given router group.
func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.GET("/skills/mine", h.ListMine)
	rg.GET("/skills/tags", h.ListTags)
	rg.GET("/skills", h.List)
	rg.GET("/skills/:skill_id", h.Get)
	rg.GET("/skills/:skill_id/versions", h.ListVersions)
	rg.POST("/skills", h.Create)
	rg.PATCH("/skills/:skill_id", h.Update)
	rg.DELETE("/skills/:skill_id", h.Delete)

	legacy := rg.Group("/skill", legacyEndpoint("/api/v1/skills"))
	legacy.GET("/mine", h.ListMine)
	legacy.GET("/tags", h.ListTags)
	legacy.GET("", h.List)
	legacy.GET("/:skill_id", h.Get)
	legacy.GET("/:skill_id/versions", h.ListVersions)
	legacy.POST("", h.Create)
	legacy.PUT("/:skill_id", h.Update)
	legacy.DELETE("/:skill_id", h.Delete)
}

// List godoc
// @Summary List skills
// @Description List skills visible in the current Space using cursor pagination.
// @Tags skill
// @ID skill.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param q query string false "Search query"
// @Param category_id query string false "Category ID"
// @Param tags query string false "Comma-separated tag names; all tags must match"
// @Param tag query []string false "Repeated tag names; all tags must match"
// @Param cursor query string false "Cursor for next page"
// @Param page_size query int false "Page size, default 20, max 50"
// @Success 200 {object} apiresponse.CursorList[SkillResponse]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skills [get]
func (h *Handler) List(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)
	limit := parseLimit(pageSizeQuery(c))

	result, err := h.svc.List(c.Request.Context(), skillsvc.ListParams{
		SpaceID:    spaceID,
		UserID:     identity.UID,
		Query:      c.Query("q"),
		CategoryID: c.Query("category_id"),
		Tags:       tagFilters(c),
		Cursor:     c.Query("cursor"),
		Limit:      limit,
	})
	if err != nil {
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	nextCursor := cursorValue(result.NextCursor)
	apiresponse.Cursor(c, result.Items, nextCursor != "", nextCursor)
}

// ListMine godoc
// @Summary List owned skills
// @Description List skills owned by the authenticated user in the current Space.
// @Tags skill
// @ID skill.mine.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param q query string false "Search query"
// @Param tags query string false "Comma-separated tag names; all tags must match"
// @Param tag query []string false "Repeated tag names; all tags must match"
// @Param cursor query string false "Cursor for next page"
// @Param page_size query int false "Page size, default 20, max 50"
// @Success 200 {object} apiresponse.CursorList[SkillResponse]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skills/mine [get]
func (h *Handler) ListMine(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)
	limit := parseLimit(pageSizeQuery(c))

	result, err := h.svc.ListMine(c.Request.Context(), skillsvc.ListParams{
		SpaceID: spaceID,
		UserID:  identity.UID,
		Query:   c.Query("q"),
		Tags:    tagFilters(c),
		Cursor:  c.Query("cursor"),
		Limit:   limit,
	})
	if err != nil {
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	nextCursor := cursorValue(result.NextCursor)
	apiresponse.Cursor(c, result.Items, nextCursor != "", nextCursor)
}

// Get godoc
// @Summary Get skill
// @Description Return one skill visible to the authenticated caller.
// @Tags skill
// @ID skill.get
// @Accept json
// @Produce json
// @Security Bearer
// @Param skill_id path string true "Skill ID"
// @Success 200 {object} apiresponse.Data[SkillResponse]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skills/{skill_id} [get]
func (h *Handler) Get(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)
	id := c.Param("skill_id")

	item, err := h.svc.Get(c.Request.Context(), id, spaceID, identity.UID)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "not found", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	apiresponse.OK(c, item)
}

// createRequest is the JSON body for POST /api/v1/skill.
type CreateRequest struct {
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

// Create godoc
// @Summary Create skill
// @Description Publish a parsed Skill archive for the authenticated user and current Space.
// @Tags skill
// @ID skill.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body CreateRequest true "Skill"
// @Success 201 {object} apiresponse.Data[SkillResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skills [post]
func (h *Handler) Create(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)

	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "parse_task_id is required", nil, "")
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
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	apiresponse.Created(c, item)
}

// updateRequest is the JSON body for PUT /api/v1/skill/:id.
type UpdateRequest struct {
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

// Update godoc
// @Summary Update skill
// @Description Partially update a Skill owned by the authenticated user.
// @Tags skill
// @ID skill.update
// @Accept json
// @Produce json
// @Security Bearer
// @Param skill_id path string true "Skill ID"
// @Param body body UpdateRequest true "Skill changes"
// @Success 200 {object} apiresponse.Data[SkillResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skills/{skill_id} [patch]
func (h *Handler) Update(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)
	id := c.Param("skill_id")

	var req UpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "invalid request body", nil, "")
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
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "not found", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrCategoryNotFound) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "category not found", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrInvalidParseTask) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "invalid or unavailable parse task", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrParseTaskConsumed) {
			apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "parse task already consumed", nil, "")
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
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	apiresponse.OK(c, item)
}

// ListTags godoc
// @Summary List skill tags
// @Description List custom Skill tags created in the current Space. Supports fuzzy search by q.
// @Tags skill
// @ID skill.tag.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param q query string false "Fuzzy tag search"
// @Param limit query int false "Limit, default 50, max 100"
// @Success 200 {object} apiresponse.Data[SkillTagList]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skills/tags [get]
func (h *Handler) ListTags(c *gin.Context) {
	if _, ok := middleware.Identity(c); !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)
	items, err := h.svc.ListTags(c.Request.Context(), spaceID, c.Query("q"), parseTagLimit(c.Query("limit")))
	if err != nil {
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}
	apiresponse.OK(c, gin.H{"items": items})
}

// Delete godoc
// @Summary Delete skill
// @Description Delete a Skill owned by the authenticated user.
// @Tags skill
// @ID skill.delete
// @Accept json
// @Produce json
// @Security Bearer
// @Param skill_id path string true "Skill ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skills/{skill_id} [delete]
func (h *Handler) Delete(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)
	id := c.Param("skill_id")

	err := h.svc.Delete(c.Request.Context(), id, identity.UID, spaceID)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "not found", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	apiresponse.Empty(c)
}

// ListVersions godoc
// @Summary List skill versions
// @Description List immutable release history for one visible Skill.
// @Tags skill
// @ID skill.version.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param skill_id path string true "Skill ID"
// @Success 200 {object} apiresponse.Data[SkillVersionList]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skills/{skill_id}/versions [get]
func (h *Handler) ListVersions(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)
	id := c.Param("skill_id")

	items, err := h.svc.ListVersions(c.Request.Context(), id, spaceID, identity.UID)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "not found", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	apiresponse.OK(c, gin.H{"items": items})
}

func pageSizeQuery(c *gin.Context) string {
	if value := c.Query("page_size"); value != "" {
		return value
	}
	return c.Query("limit")
}

func cursorValue(cursor *string) string {
	if cursor == nil {
		return ""
	}
	return *cursor
}

func legacyEndpoint(successor string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Deprecation", "true")
		c.Header("Sunset", "Thu, 01 Oct 2026 00:00:00 GMT")
		c.Header("Link", "<"+successor+">; rel=\"successor-version\"")
		c.Next()
	}
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

func parseTagLimit(s string) int {
	if s == "" {
		return 50
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 50
	}
	if n > 100 {
		return 100
	}
	return n
}

func tagFilters(c *gin.Context) []string {
	values := append([]string{}, c.QueryArray("tags")...)
	values = append(values, c.QueryArray("tag")...)
	return skillsvc.ParseTagFilters(values...)
}
