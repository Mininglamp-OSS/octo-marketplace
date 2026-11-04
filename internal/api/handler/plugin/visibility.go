package plugin

import (
	"context"

	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type visibilityRequest struct {
	Visibility                 string `json:"visibility" enums:"private,space" binding:"required"`
	ExpectedPublishedVersionID string `json:"expected_published_version_id,omitempty"`
	ExpectedPlanRevision       string `json:"expected_plan_revision,omitempty"`
}
type visibilityResponse struct {
	PluginID           string                    `json:"plugin_id"`
	Visibility         model.PluginVisibility    `json:"visibility"`
	PublishedVersionID string                    `json:"published_version_id"`
	DisplayStatus      model.PluginDisplayStatus `json:"display_status"`
	ReviewID           *string                   `json:"review_id,omitempty"`
}

// ChangeVisibility godoc
// @Summary Change a published plugin audience without a content release
// @Description Owner-only for published tenant Skills, Experts and Expert Teams. Keeps immutable content, version labels, release count and editable drafts unchanged. Widening to Space follows its review policy and keeps the previous effective audience until approval. Narrowing validates published references. Concurrent content/review changes are checked under a resource lock. Non-owners and other Spaces receive an opaque 404.
// @Tags plugin
// @ID plugin.visibility.update
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param body body visibilityRequest true "Target audience and the published version the owner viewed"
// @Success 200 {object} apiresponse.Data[visibilityResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/visibility [patch]
func (h *Handler) ChangeVisibility(c *gin.Context) {
	who, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req visibilityRequest
	if !decode(c, &req) {
		return
	}
	svc, ok := h.svc.(interface {
		ChangeVisibility(context.Context, pluginsvc.Caller, pluginsvc.VisibilityParams) (*pluginrepo.VisibilityResult, error)
	})
	if !ok {
		writeServiceError(c, pluginsvc.ErrNotFound, "plugin.visibility")
		return
	}
	result, err := svc.ChangeVisibility(c.Request.Context(), who, pluginsvc.VisibilityParams{PluginID: c.Param("plugin_id"), ExpectedPublishedVersionID: req.ExpectedPublishedVersionID, ExpectedPlanRevision: req.ExpectedPlanRevision, Visibility: model.PluginVisibility(req.Visibility)})
	if err != nil {
		writeServiceError(c, err, "plugin.visibility")
		return
	}
	out := visibilityResponse{PluginID: result.Plugin.ID, Visibility: result.Plugin.Visibility, PublishedVersionID: result.PublishedVersionID, DisplayStatus: result.Plugin.DisplayStatus(result.Pending, "")}
	if result.ReviewID != "" {
		out.ReviewID = &result.ReviewID
	}
	if result.Pending {
		out.DisplayStatus = model.PluginDisplayStatusPendingReview
	}
	apiresponse.OK(c, out)
}

// VisibilityPlan previews the complete audience change without writing data.
// @Summary Preview a plugin audience change and dependency impact
// @Description Returns owner-scoped identities and an opaque revision for confirming all scope changes together. Private foreign dependency content is never exposed.
// @Tags plugin
// @ID plugin.visibility_plan.get
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param visibility query string true "Target audience" Enums(private,space)
// @Success 200 {object} apiresponse.Data[model.VisibilityPlan]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/visibility_plan [get]
func (h *Handler) VisibilityPlan(c *gin.Context) {
	who, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	svc, ok := h.svc.(interface {
		VisibilityPlan(context.Context, pluginsvc.Caller, string, model.PluginVisibility) (*model.VisibilityPlan, error)
	})
	if !ok {
		writeServiceError(c, pluginsvc.ErrNotFound, "plugin.visibility_plan")
		return
	}
	out, err := svc.VisibilityPlan(c.Request.Context(), who, c.Param("plugin_id"), model.PluginVisibility(c.Query("visibility")))
	if err != nil {
		writeServiceError(c, err, "plugin.visibility_plan")
		return
	}
	apiresponse.OK(c, out)
}
