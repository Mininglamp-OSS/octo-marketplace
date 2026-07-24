package skill

import (
	"errors"
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/gin-gonic/gin"
)

// RegisterAdmin registers admin skill routes on the given engine.
func (h *Handler) RegisterAdmin(r *gin.Engine, adminAuth *middleware.AdminAuthenticator) {
	admin := r.Group("/api/v1/admin/skills", adminAuth.Handler())
	admin.GET("", h.AdminList)
	admin.GET("/:skill_id", h.AdminGet)
	admin.POST("", h.AdminCreate)
	admin.PATCH("/:skill_id", h.AdminUpdate)
	admin.DELETE("/:skill_id", h.AdminDelete)
	admin.GET("/:skill_id/skill_md", h.AdminGetSkillMD)
	admin.POST("/:skill_id/reupload", h.AdminReupload)
}

// AdminList godoc
// @Summary List public skills (admin)
// @Description List all visibility=public skills without Space restriction.
// @Tags admin_skill
// @ID admin_skill.list
// @Accept json
// @Produce json
// @Security AdminToken
// @Param q query string false "Search name/description"
// @Param category_id query string false "Category ID"
// @Param tags query string false "Comma-separated tags"
// @Param sort query string false "Sort: latest, downloads, views, comprehensive"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size"
// @Success 200 {object} apiresponse.OffsetList[skillsvc.SkillItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/skills [get]
func (h *Handler) AdminList(c *gin.Context) {
	if _, ok := middleware.Identity(c); !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "authentication is required", nil, "")
		return
	}
	limit := parseLimit(pageSizeQuery(c))
	sort := c.Query("sort")
	if sort == "" {
		sort = "latest"
	}
	if !validSortModes[sort] {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "invalid sort parameter", nil, "")
		return
	}
	page := parsePage(c.Query("page"))
	offset := (page - 1) * limit
	if rawOffset := c.Query("offset"); rawOffset != "" {
		offset = parseOffset(rawOffset)
		if limit > 0 {
			page = offset/limit + 1
		}
	}

	result, err := h.svc.AdminList(c.Request.Context(), skillsvc.AdminListParams{
		Query:      c.Query("q"),
		CategoryID: c.Query("category_id"),
		Tags:       tagFilters(c),
		Limit:      limit,
		Offset:     offset,
		Sort:       sort,
	})
	if err != nil {
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}
	apiresponse.Offset(c, result.Items, result.Total, page, limit)
}

// AdminGet godoc
// @Summary Get public skill detail (admin)
// @Description Return one public skill without Space restriction.
// @Tags admin_skill
// @ID admin_skill.get
// @Accept json
// @Produce json
// @Security AdminToken
// @Param skill_id path string true "Skill ID"
// @Success 200 {object} apiresponse.Data[skillsvc.SkillItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/skills/{skill_id} [get]
func (h *Handler) AdminGet(c *gin.Context) {
	if _, ok := middleware.Identity(c); !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "authentication is required", nil, "")
		return
	}
	id := c.Param("skill_id")
	item, err := h.svc.AdminGet(c.Request.Context(), id)
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

// AdminCreateRequest is the JSON body for POST /api/v1/admin/skills.
type AdminCreateRequest struct {
	ParseTaskID string   `json:"parse_task_id" binding:"required"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	IconURL     string   `json:"icon_url"`
	Description string   `json:"description"`
	CategoryID  string   `json:"category_id"`
	Tags        []string `json:"tags" binding:"omitempty,max=10,dive,max=10" maxLength:"10"`
	Version     string   `json:"version"`
}

// AdminCreate godoc
// @Summary Create public skill (admin)
// @Description Create a system skill with visibility=public.
// @Tags admin_skill
// @ID admin_skill.create
// @Accept json
// @Produce json
// @Security AdminToken
// @Param body body AdminCreateRequest true "Skill"
// @Success 201 {object} apiresponse.Data[skillsvc.SkillItem]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/skills [post]
func (h *Handler) AdminCreate(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "authentication is required", nil, "")
		return
	}
	var req AdminCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "invalid request body", nil, "")
		return
	}
	item, err := h.svc.AdminCreate(c.Request.Context(), skillsvc.AdminCreateParams{
		ParseTaskID: req.ParseTaskID,
		Name:        req.Name,
		DisplayName: req.DisplayName,
		IconURL:     req.IconURL,
		Description: req.Description,
		CategoryID:  req.CategoryID,
		Tags:        marshalTags(req.Tags),
		Version:     req.Version,
		AdminUID:    identity.UID,
		AdminName:   identity.Name,
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
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "tags must contain at most 10 strings of at most 10 characters each", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}
	apiresponse.Created(c, item)
}

// AdminUpdateRequest is the JSON body for PATCH /api/v1/admin/skills/:skill_id.
type AdminUpdateRequest struct {
	Name        *string   `json:"name"`
	DisplayName *string   `json:"display_name"`
	IconURL     *string   `json:"icon_url"`
	Description *string   `json:"description"`
	CategoryID  *string   `json:"category_id"`
	Tags        *[]string `json:"tags" binding:"omitempty,max=10,dive,max=10" maxLength:"10"`
}

// AdminUpdate godoc
// @Summary Update public skill (admin)
// @Description Update basic info of a public skill.
// @Tags admin_skill
// @ID admin_skill.update
// @Accept json
// @Produce json
// @Security AdminToken
// @Param skill_id path string true "Skill ID"
// @Param body body AdminUpdateRequest true "Skill changes"
// @Success 200 {object} apiresponse.Data[skillsvc.SkillItem]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/skills/{skill_id} [patch]
func (h *Handler) AdminUpdate(c *gin.Context) {
	if _, ok := middleware.Identity(c); !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "authentication is required", nil, "")
		return
	}
	id := c.Param("skill_id")
	var req AdminUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "invalid request body", nil, "")
		return
	}
	item, err := h.svc.AdminUpdate(c.Request.Context(), id, skillsvc.AdminUpdateParams{
		Name:        req.Name,
		DisplayName: req.DisplayName,
		IconURL:     req.IconURL,
		Description: req.Description,
		CategoryID:  req.CategoryID,
		Tags:        marshalOptionalTags(req.Tags),
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
		if errors.Is(err, skillsvc.ErrNameTaken) {
			apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "skill name already exists", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrInvalidTags) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "tags must contain at most 10 strings of at most 10 characters each", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}
	apiresponse.OK(c, item)
}

// AdminDelete godoc
// @Summary Delete public skill (admin)
// @Description Delete a public skill and its versions.
// @Tags admin_skill
// @ID admin_skill.delete
// @Accept json
// @Produce json
// @Security AdminToken
// @Param skill_id path string true "Skill ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/skills/{skill_id} [delete]
func (h *Handler) AdminDelete(c *gin.Context) {
	if _, ok := middleware.Identity(c); !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "authentication is required", nil, "")
		return
	}
	id := c.Param("skill_id")
	err := h.svc.AdminDelete(c.Request.Context(), id)
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

// AdminGetSkillMD godoc
// @Summary Get SKILL.md (admin)
// @Description Return SKILL.md for a public skill without Space restriction.
// @Tags admin_skill
// @ID admin_skill.skillmd.get
// @Accept json
// @Produce json
// @Security AdminToken
// @Param skill_id path string true "Skill ID"
// @Success 200 {object} apiresponse.Data[SkillMDResponse]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/skills/{skill_id}/skill_md [get]
func (h *Handler) AdminGetSkillMD(c *gin.Context) {
	if _, ok := middleware.Identity(c); !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "authentication is required", nil, "")
		return
	}
	id := c.Param("skill_id")
	data, err := h.svc.AdminGetSkillMD(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "not found", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrNoFile) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "skill-md not available", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}
	apiresponse.OK(c, SkillMDResponse{Content: string(data)})
}

// AdminReuploadRequest is the JSON body for POST /api/v1/admin/skills/:skill_id/reupload.
type AdminReuploadRequest struct {
	ParseTaskID string   `json:"parse_task_id" binding:"required"`
	Version     string   `json:"version"`
	Changelog   string   `json:"changelog"`
	Tags        []string `json:"tags" binding:"omitempty,max=10,dive,max=10" maxLength:"10"`
}

// AdminReupload godoc
// @Summary Reupload public skill version (admin)
// @Description Upload a new zip to update the skill version.
// @Tags admin_skill
// @ID admin_skill.reupload
// @Accept json
// @Produce json
// @Security AdminToken
// @Param skill_id path string true "Skill ID"
// @Param body body AdminReuploadRequest true "Reupload params"
// @Success 200 {object} apiresponse.Data[skillsvc.SkillItem]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/skills/{skill_id}/reupload [post]
func (h *Handler) AdminReupload(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "authentication is required", nil, "")
		return
	}
	id := c.Param("skill_id")
	var req AdminReuploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "invalid request body", nil, "")
		return
	}
	item, err := h.svc.AdminReupload(c.Request.Context(), id, skillsvc.AdminReuploadParams{
		ParseTaskID: req.ParseTaskID,
		Version:     req.Version,
		Changelog:   req.Changelog,
		Tags:        marshalTags(req.Tags),
		AdminUID:    identity.UID,
	})
	if err != nil {
		if errors.Is(err, skillsvc.ErrNotFound) {
			apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "not found", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrInvalidParseTask) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "invalid or unavailable parse task", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrIDMismatch) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "zip id does not match skill id", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrNameMismatch) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "skill name does not match SKILL.md name", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrParseTaskConsumed) {
			apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "parse task already consumed", nil, "")
			return
		}
		if errors.Is(err, skillsvc.ErrInvalidTags) {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "tags must contain at most 10 strings of at most 10 characters each", nil, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}
	apiresponse.OK(c, item)
}
