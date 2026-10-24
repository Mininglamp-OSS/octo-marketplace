package plugin

import (
	"context"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
	"strconv"
)

type referenceReadService interface {
	ReferenceDependents(context.Context, pluginsvc.Caller, string, string, int, int, bool) ([]pluginrepo.ReferenceDependent, int, error)
	ReferenceCandidates(context.Context, pluginsvc.Caller, string, string, int, int) ([]pluginrepo.ReferenceCandidate, int, error)
}

func referencePage(c *gin.Context) (page, size int, ok bool) {
	page, size = 1, 20
	var err error
	if q := c.Query("page"); q != "" {
		page, err = strconv.Atoi(q)
		if err != nil || page < 1 || page > 1000000 {
			writeServiceError(c, pluginsvc.ErrInvalidRequest, "plugin.references.page")
			return 0, 0, false
		}
	}
	if q := c.Query("page_size"); q != "" {
		size, err = strconv.Atoi(q)
		if err != nil || size < 1 || size > 100 {
			writeServiceError(c, pluginsvc.ErrInvalidRequest, "plugin.references.page")
			return 0, 0, false
		}
	}
	return page, size, true
}
func readDependents(c *gin.Context, svc any, admin bool) {
	who, ok := caller(c)
	if admin {
		who, ok = adminCaller(c)
	}
	if !ok {
		unauthorized(c)
		return
	}
	v, ok := svc.(referenceReadService)
	if !ok {
		writeServiceError(c, pluginsvc.ErrNotFound, "plugin.dependents")
		return
	}
	page, size, ok := referencePage(c)
	if !ok {
		return
	}
	items, total, err := v.ReferenceDependents(c.Request.Context(), who, c.Param("plugin_id"), c.Query("version_id"), size, (page-1)*size, admin)
	if err != nil {
		writeServiceError(c, err, "plugin.dependents")
		return
	}
	apiresponse.Offset(c, items, total, page, size)
}

// ReferenceDependents godoc
// @Summary List historical plugin dependents
// @Description Returns authorized direct and indirect consumers from immutable reference indexes. Unverified historical index coverage returns a conflict instead of a partial result. Delisting remains available regardless of these diagnostic results.
// @Tags plugin
// @ID plugin.dependents.list
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Target plugin ID"
// @Param version_id query string false "Optional target version ID"
// @Param page query int false "Page number"
// @Param page_size query int false "Page size, at most 100"
// @Success 200 {object} apiresponse.OffsetList[pluginrepo.ReferenceDependent]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT: reference index coverage is incomplete"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/dependents [get]
func (h *Handler) ReferenceDependents(c *gin.Context) { readDependents(c, h.svc, false) }

// ReferenceDependents godoc
// @Summary List administrative plugin dependents
// @Description Requires marketplace administrator role and enabled references. Returns immutable historical consumers without artifact content or storage keys.
// @Tags admin_plugin
// @ID admin_plugin.dependents.list
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Target plugin ID"
// @Param version_id query string false "Optional target version ID"
// @Param page query int false "Page number"
// @Param page_size query int false "Page size, at most 100"
// @Success 200 {object} apiresponse.OffsetList[pluginrepo.ReferenceDependent]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT: reference index coverage is incomplete"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id}/dependents [get]
func (h *AdminHandler) ReferenceDependents(c *gin.Context) { readDependents(c, h.svc, true) }

// ReferenceCandidates godoc
// @Summary List authorized plugin reference candidates
// @Description Returns independently published resources compatible with the source audience. Already bound targets and missing release baselines are explicitly unselectable; placements never determine eligibility.
// @Tags plugin
// @ID plugin.references.list
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Source plugin ID"
// @Param relation_type query string true "Relation type" Enums(expert_skill,expert_connector,expert_team_expert)
// @Param page query int false "Page number"
// @Param page_size query int false "Page size, at most 100"
// @Success 200 {object} apiresponse.OffsetList[pluginrepo.ReferenceCandidate]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/reference_candidates [get]
func (h *Handler) ReferenceCandidates(c *gin.Context) {
	who, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	v, ok := h.svc.(referenceReadService)
	if !ok {
		writeServiceError(c, pluginsvc.ErrNotFound, "plugin.reference_candidates")
		return
	}
	page, size, ok := referencePage(c)
	if !ok {
		return
	}
	items, total, err := v.ReferenceCandidates(c.Request.Context(), who, c.Param("plugin_id"), c.Query("relation_type"), size, (page-1)*size)
	if err != nil {
		writeServiceError(c, err, "plugin.reference_candidates")
		return
	}
	apiresponse.Offset(c, items, total, page, size)
}
