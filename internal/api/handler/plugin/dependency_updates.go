package plugin

import (
	"context"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type dismissChoices struct {
	Choices []pluginrepo.DependencyChoice `json:"choices" binding:"required,min=1,max=100,dive"`
}

// DependencyUpdates godoc
// @Summary DependencyUpdates for a published expert or team
// @Description Owner-scoped direct dependencies only. Includes dismissed candidates. Use the returned draft revision, published ID and selected choices on publish.
// @Tags plugin
// @ID plugin.dependency_updates.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Resource ID"
// @Success 200 {object} apiresponse.Data[pluginrepo.DependencyPreview]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/dependency_updates [get]
func (h *Handler) DependencyUpdates(c *gin.Context) { h.readUpdates(c, nil) }

// DismissUpdates godoc
// @Summary DismissUpdates for a published expert or team
// @Description Owner-scoped direct dependencies only. Includes dismissed candidates. Use the returned draft revision, published ID and selected choices on publish.
// @Tags plugin
// @ID plugin.dependency_updates.dismiss
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Resource ID"
// @Param body body dismissChoices true "Exact reminder candidates to dismiss"
// @Success 200 {object} apiresponse.Data[pluginrepo.DependencyPreview]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/dependency_updates/dismiss [post]
func (h *Handler) DismissUpdates(c *gin.Context) {
	var req dismissChoices
	if !decode(c, &req) {
		return
	}
	h.readUpdates(c, req.Choices)
}
func (h *Handler) readUpdates(c *gin.Context, choices []pluginrepo.DependencyChoice) {
	who, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	svc, ok := h.svc.(interface {
		DependencyUpdates(context.Context, pluginsvc.Caller, string, []pluginrepo.DependencyChoice) (*pluginrepo.DependencyPreview, error)
	})
	if !ok {
		writeServiceError(c, pluginsvc.ErrNotFound, "plugin.dependency_updates")
		return
	}
	plan, err := svc.DependencyUpdates(c.Request.Context(), who, c.Param("plugin_id"), choices)
	if err != nil {
		writeServiceError(c, err, "plugin.dependency_updates")
		return
	}
	apiresponse.OK(c, plan)
}
