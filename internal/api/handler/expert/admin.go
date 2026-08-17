// This file exposes the administrator (SuperAdmin) HTTP surface for the Expert
// Marketplace, mounted under /api/v1/admin and gated by AdminAuthenticator. It
// mirrors the skill/category admin pattern (a RegisterAdmin method on the same
// Handler) rather than the two-struct MCP pattern, because the expert handler
// already shares callerFromContext + writeServiceError. Create stamps
// visibility=system in the service; list bypasses Space scoping; get/update/
// delete operate on system rows by id. The skill-package upload endpoint is the
// user-surface handler reused verbatim (it is Space/owner-agnostic).
package expert

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/expert"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
	"github.com/gin-gonic/gin"
)

// AdminCategoryRequest is the create/update body for an expert category.
type AdminCategoryRequest struct {
	Name      string `json:"name"`
	IconKey   string `json:"icon_key"`
	SortOrder int    `json:"sort_order"`
}

// RegisterAdmin mounts the expert/squad/category/tag/upload admin routes on the
// engine root under /api/v1/admin. Admitted: superAdmin everywhere, plus
// marketAdmin — that role runs the platform market as a whole, so the Expert
// Market is admitted alongside the MCP and Skill catalogs.
func (h *Handler) RegisterAdmin(r *gin.Engine, adminAuth *middleware.AdminAuthenticator) {
	experts := r.Group("/api/v1/admin/experts", adminAuth.Handler(middleware.RoleMarketAdmin))
	experts.POST("", h.AdminCreateExpert)
	experts.GET("", h.AdminListExperts)
	experts.GET("/:expert_id", h.AdminGetExpert)
	experts.GET("/:expert_id/skill_md", h.AdminGetExpertSkillMD)
	experts.PATCH("/:expert_id", h.AdminPatchExpert)
	experts.DELETE("/:expert_id", h.AdminDeleteExpert)

	squads := r.Group("/api/v1/admin/squads", adminAuth.Handler(middleware.RoleMarketAdmin))
	squads.POST("", h.AdminCreateSquad)
	squads.GET("", h.AdminListSquads)
	squads.GET("/:squad_id", h.AdminGetSquad)
	squads.GET("/:squad_id/skill_md", h.AdminGetSquadSkillMD)
	squads.PATCH("/:squad_id", h.AdminPatchSquad)
	squads.DELETE("/:squad_id", h.AdminDeleteSquad)

	// The taxonomy, tag, and upload endpoints live directly under /api/v1/admin
	// (a separate group) so their static paths never collide with the
	// /api/v1/admin/experts/:expert_id wildcard — same split registerAdminMCP uses.
	admin := r.Group("/api/v1/admin", adminAuth.Handler(middleware.RoleMarketAdmin))
	admin.GET("/expert_categories", h.AdminListCategories)
	admin.POST("/expert_categories", h.AdminCreateCategory)
	admin.PATCH("/expert_categories/:category_id", h.AdminUpdateCategory)
	admin.DELETE("/expert_categories/:category_id", h.AdminDeleteCategory)
	admin.GET("/expert_tags", h.AdminListTags)
	admin.POST("/expert_skill_uploads", h.InitSkillUpload)
}

// ─── Experts (admin) ─────────────────────────────────────────────────────────

// AdminCreateExpert godoc
// @Summary Create system expert (admin)
// @Description Create a platform-provided expert (visibility=system, cross-Space).
// @Tags admin_expert
// @ID admin_expert.create
// @Accept json
// @Produce json
// @Security AdminToken
// @Param request body model.ExpertCreateRequest true "Create payload"
// @Success 201 {object} apiresponse.Data[ExpertDetailResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 409 {object} apiresponse.Error "DUPLICATE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/experts [post]
func (h *Handler) AdminCreateExpert(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req model.ExpertCreateRequest
	if !decodeJSON(c, &req) {
		return
	}
	detail, err := h.svc.CreateSystemExpert(c.Request.Context(), caller, req)
	if err != nil {
		writeServiceError(c, err, "admin.expert.create")
		return
	}
	apiresponse.Created(c, detail)
}

// AdminListExperts godoc
// @Summary List system experts (admin)
// @Description List every visibility=system expert across Spaces.
// @Tags admin_expert
// @ID admin_expert.list
// @Produce json
// @Security AdminToken
// @Param keyword query string false "Search name/summary/creator"
// @Param category query string false "Category id filter; 'all' disables"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[model.ExpertAgentListItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/experts [get]
func (h *Handler) AdminListExperts(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	params, page, pageSize := listParams(c)
	result, err := h.svc.ListSystemExperts(c.Request.Context(), params)
	if err != nil {
		writeServiceError(c, err, "admin.expert.list")
		return
	}
	apiresponse.Offset(c, result.Items, result.Total, page, pageSize)
}

// AdminGetExpert godoc
// @Summary Get system expert detail (admin)
// @Description Full system expert record, including instruction, mcp_config and skills.
// @Tags admin_expert
// @ID admin_expert.get
// @Produce json
// @Security AdminToken
// @Param expert_id path string true "Expert ID"
// @Success 200 {object} apiresponse.Data[ExpertDetailResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/experts/{expert_id} [get]
func (h *Handler) AdminGetExpert(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	detail, err := h.svc.GetSystemExpert(c.Request.Context(), c.Param("expert_id"))
	if err != nil {
		writeServiceError(c, err, "admin.expert.get")
		return
	}
	apiresponse.OK(c, detail)
}

// AdminPatchExpert godoc
// @Summary Update system expert (admin)
// @Description Partial update; sending skills replaces the stored set.
// @Tags admin_expert
// @ID admin_expert.update
// @Accept json
// @Produce json
// @Security AdminToken
// @Param expert_id path string true "Expert ID"
// @Param request body model.ExpertPatchRequest true "Patch payload"
// @Success 200 {object} apiresponse.Data[ExpertDetailResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "DUPLICATE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/experts/{expert_id} [patch]
func (h *Handler) AdminPatchExpert(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	var req model.ExpertPatchRequest
	if !decodeJSON(c, &req) {
		return
	}
	detail, err := h.svc.UpdateSystemExpert(c.Request.Context(), c.Param("expert_id"), req)
	if err != nil {
		writeServiceError(c, err, "admin.expert.update")
		return
	}
	apiresponse.OK(c, detail)
}

// AdminDeleteExpert godoc
// @Summary Delete system expert (admin)
// @Description Soft delete; the name frees up for reuse.
// @Tags admin_expert
// @ID admin_expert.delete
// @Produce json
// @Security AdminToken
// @Param expert_id path string true "Expert ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/experts/{expert_id} [delete]
func (h *Handler) AdminDeleteExpert(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	if err := h.svc.DeleteSystemExpert(c.Request.Context(), c.Param("expert_id")); err != nil {
		writeServiceError(c, err, "admin.expert.delete")
		return
	}
	apiresponse.Empty(c)
}

// ─── Squads (admin) ──────────────────────────────────────────────────────────

// AdminCreateSquad godoc
// @Summary Create system squad (admin)
// @Description Create a platform-provided squad (visibility=system, cross-Space).
// @Tags admin_expert
// @ID admin_squad.create
// @Accept json
// @Produce json
// @Security AdminToken
// @Param request body model.SquadCreateRequest true "Create payload"
// @Success 201 {object} apiresponse.Data[SquadDetailResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 409 {object} apiresponse.Error "DUPLICATE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/squads [post]
func (h *Handler) AdminCreateSquad(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req model.SquadCreateRequest
	if !decodeJSON(c, &req) {
		return
	}
	detail, err := h.svc.CreateSystemSquad(c.Request.Context(), caller, req)
	if err != nil {
		writeServiceError(c, err, "admin.squad.create")
		return
	}
	apiresponse.Created(c, detail)
}

// AdminListSquads godoc
// @Summary List system squads (admin)
// @Description List every visibility=system squad across Spaces.
// @Tags admin_expert
// @ID admin_squad.list
// @Produce json
// @Security AdminToken
// @Param keyword query string false "Search name/summary/creator"
// @Param category query string false "Category id filter; 'all' disables"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[model.ExpertSquadListItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/squads [get]
func (h *Handler) AdminListSquads(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	params, page, pageSize := listParams(c)
	result, err := h.svc.ListSystemSquads(c.Request.Context(), params)
	if err != nil {
		writeServiceError(c, err, "admin.squad.list")
		return
	}
	apiresponse.Offset(c, result.Items, result.Total, page, pageSize)
}

// AdminGetSquad godoc
// @Summary Get system squad detail (admin)
// @Description Full system squad record, including strategies, dependencies and the member roster.
// @Tags admin_expert
// @ID admin_squad.get
// @Produce json
// @Security AdminToken
// @Param squad_id path string true "Squad ID"
// @Success 200 {object} apiresponse.Data[SquadDetailResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/squads/{squad_id} [get]
func (h *Handler) AdminGetSquad(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	detail, err := h.svc.GetSystemSquad(c.Request.Context(), c.Param("squad_id"))
	if err != nil {
		writeServiceError(c, err, "admin.squad.get")
		return
	}
	apiresponse.OK(c, detail)
}

// AdminPatchSquad godoc
// @Summary Update system squad (admin)
// @Description Partial update; sending members replaces the whole roster.
// @Tags admin_expert
// @ID admin_squad.update
// @Accept json
// @Produce json
// @Security AdminToken
// @Param squad_id path string true "Squad ID"
// @Param request body model.SquadPatchRequest true "Patch payload"
// @Success 200 {object} apiresponse.Data[SquadDetailResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "DUPLICATE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/squads/{squad_id} [patch]
func (h *Handler) AdminPatchSquad(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	var req model.SquadPatchRequest
	if !decodeJSON(c, &req) {
		return
	}
	detail, err := h.svc.UpdateSystemSquad(c.Request.Context(), c.Param("squad_id"), req)
	if err != nil {
		writeServiceError(c, err, "admin.squad.update")
		return
	}
	apiresponse.OK(c, detail)
}

// AdminDeleteSquad godoc
// @Summary Delete system squad (admin)
// @Description Soft delete; the name frees up for reuse.
// @Tags admin_expert
// @ID admin_squad.delete
// @Produce json
// @Security AdminToken
// @Param squad_id path string true "Squad ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/squads/{squad_id} [delete]
func (h *Handler) AdminDeleteSquad(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	if err := h.svc.DeleteSystemSquad(c.Request.Context(), c.Param("squad_id")); err != nil {
		writeServiceError(c, err, "admin.squad.delete")
		return
	}
	apiresponse.Empty(c)
}

// ─── Categories (admin) ──────────────────────────────────────────────────────

// AdminListCategories godoc
// @Summary List expert categories (admin)
// @Description List every category with usage counts, including empty ones.
// @Tags admin_expert
// @ID admin_expert_category.list
// @Produce json
// @Security AdminToken
// @Success 200 {object} apiresponse.Data[[]expertsvc.AdminCategory]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/expert_categories [get]
func (h *Handler) AdminListCategories(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	items, err := h.svc.ListAdminCategories(c.Request.Context())
	if err != nil {
		apiresponse.Internal(c, err, "admin.expert_category.list")
		return
	}
	apiresponse.OK(c, items)
}

// AdminCreateCategory godoc
// @Summary Create expert category (admin)
// @Description Create a category for the expert market taxonomy.
// @Tags admin_expert
// @ID admin_expert_category.create
// @Accept json
// @Produce json
// @Security AdminToken
// @Param request body AdminCategoryRequest true "Category payload"
// @Success 201 {object} apiresponse.Data[expertsvc.AdminCategory]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/expert_categories [post]
func (h *Handler) AdminCreateCategory(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	var req AdminCategoryRequest
	if !decodeJSON(c, &req) {
		return
	}
	item, err := h.svc.CreateCategory(c.Request.Context(), req.Name, req.IconKey, req.SortOrder)
	if err != nil {
		writeCategoryError(c, err, 0, "admin.expert_category.create")
		return
	}
	apiresponse.Created(c, item)
}

// AdminUpdateCategory godoc
// @Summary Update expert category (admin)
// @Description Update name, icon and sort order; all three columns are written.
// @Tags admin_expert
// @ID admin_expert_category.update
// @Accept json
// @Produce json
// @Security AdminToken
// @Param category_id path string true "Category ID"
// @Param request body AdminCategoryRequest true "Category payload"
// @Success 200 {object} apiresponse.Data[expertsvc.AdminCategory]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/expert_categories/{category_id} [patch]
func (h *Handler) AdminUpdateCategory(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	var req AdminCategoryRequest
	if !decodeJSON(c, &req) {
		return
	}
	item, err := h.svc.UpdateCategory(c.Request.Context(), c.Param("category_id"), req.Name, req.IconKey, req.SortOrder)
	if err != nil {
		writeCategoryError(c, err, 0, "admin.expert_category.update")
		return
	}
	apiresponse.OK(c, item)
}

// AdminDeleteCategory godoc
// @Summary Delete expert category (admin)
// @Description Rejected with 409 (and the reference count) while records still use it.
// @Tags admin_expert
// @ID admin_expert_category.delete
// @Produce json
// @Security AdminToken
// @Param category_id path string true "Category ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/expert_categories/{category_id} [delete]
func (h *Handler) AdminDeleteCategory(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	count, err := h.svc.DeleteCategory(c.Request.Context(), c.Param("category_id"))
	if err != nil {
		writeCategoryError(c, err, count, "admin.expert_category.delete")
		return
	}
	apiresponse.Empty(c)
}

// ─── Tags (admin) ────────────────────────────────────────────────────────────

// AdminListTags godoc
// @Summary List expert tags (admin)
// @Description Aggregate tags across system records of the given kind.
// @Tags admin_expert
// @ID admin_expert_tag.list
// @Produce json
// @Security AdminToken
// @Param kind query string false "agent (default) or squad"
// @Param limit query int false "Max tags, default 50, max 100"
// @Success 200 {object} apiresponse.Data[TagListResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/expert_tags [get]
func (h *Handler) AdminListTags(c *gin.Context) {
	caller, ok := callerFromContext(c)
	if !ok {
		unauthorized(c)
		return
	}
	entity := expertrepo.EntityExpert
	if c.Query("kind") == "squad" {
		entity = expertrepo.EntitySquad
	}
	// Admin caller has an empty SpaceID, so the shared visible-set rule reduces
	// to the system-row clause — i.e. this aggregates tags off system records.
	tags, err := h.svc.ListTags(c.Request.Context(), caller, entity, "", parseTagLimit(c.Query("limit")), false)
	if err != nil {
		writeServiceError(c, err, "admin.expert_tag.list")
		return
	}
	apiresponse.OK(c, tags)
}

// writeCategoryError maps the admin category sentinels onto wire codes: name
// collision → 409 CONFLICT, in-use → 409 CONFLICT (with the reference count),
// not-found → 404, invalid → 400, else a logged 500.
func writeCategoryError(c *gin.Context, err error, count int, operation string) {
	switch {
	case errors.Is(err, expertsvc.ErrNotFound):
		apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "category not found", nil, "")
	case errors.Is(err, expertsvc.ErrCategoryNameTaken):
		apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "category name already exists", nil, "")
	case errors.Is(err, expertsvc.ErrCategoryInUse):
		apiresponse.Fail(c, http.StatusConflict, errcode.CategoryInUse, "category is in use",
			map[string]any{"count": count}, "Move the experts before deleting this category.")
	case errors.Is(err, expertsvc.ErrInvalidRequest):
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "name is required", nil, "")
	default:
		apiresponse.Internal(c, err, operation)
	}
}

// ─── System skill content (admin) ────────────────────────────────────────────

// AdminGetExpertSkillMD godoc
// @Summary Get system expert skill content (admin)
// @Description Return the stored SKILL.md text for the expert's skill at index i.
// @Tags admin_expert
// @ID admin_expert.skill_md
// @Produce json
// @Security AdminToken
// @Param expert_id path string true "Expert ID"
// @Param i query int false "Skill index (default 0)"
// @Success 200 {object} apiresponse.Data[SkillContentResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/experts/{expert_id}/skill_md [get]
func (h *Handler) AdminGetExpertSkillMD(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	index, _ := strconv.Atoi(c.Query("i"))
	content, err := h.svc.GetSystemExpertSkillMD(c.Request.Context(), c.Param("expert_id"), index)
	if err != nil {
		writeServiceError(c, err, "admin.expert.skill_md")
		return
	}
	apiresponse.OK(c, SkillContentResp{Content: content})
}

// AdminGetSquadSkillMD godoc
// @Summary Get system squad member skill content (admin)
// @Description Return the stored SKILL.md text for a squad member's skill at index i.
// @Tags admin_expert
// @ID admin_squad.skill_md
// @Produce json
// @Security AdminToken
// @Param squad_id path string true "Squad ID"
// @Param member query string true "Member key"
// @Param i query int false "Skill index (default 0)"
// @Success 200 {object} apiresponse.Data[SkillContentResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/squads/{squad_id}/skill_md [get]
func (h *Handler) AdminGetSquadSkillMD(c *gin.Context) {
	if _, ok := callerFromContext(c); !ok {
		unauthorized(c)
		return
	}
	index, _ := strconv.Atoi(c.Query("i"))
	content, err := h.svc.GetSystemSquadSkillMD(c.Request.Context(), c.Param("squad_id"), c.Query("member"), index)
	if err != nil {
		writeServiceError(c, err, "admin.squad.skill_md")
		return
	}
	apiresponse.OK(c, SkillContentResp{Content: content})
}
