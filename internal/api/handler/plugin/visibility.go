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
	ExpectedRevision           string `json:"expected_revision,omitempty"`
	PlanRevision               string `json:"plan_revision,omitempty"`
	Confirmed                  bool   `json:"is_confirmed,omitempty"`
	Visibility                 string `json:"visibility" enums:"private,space" binding:"required"`
	ExpectedPublishedVersionID string `json:"expected_published_version_id,omitempty"`
}
type visibilityResponse struct {
	GroupID            string                    `json:"review_group_id,omitempty"`
	PlanRevision       string                    `json:"plan_revision,omitempty"`
	PluginID           string                    `json:"plugin_id"`
	Visibility         model.PluginVisibility    `json:"visibility"`
	PublishedVersionID string                    `json:"published_version_id"`
	DisplayStatus      model.PluginDisplayStatus `json:"display_status"`
	ReviewID           *string                   `json:"review_id,omitempty"`
}

// ChangeVisibility godoc
// @Summary Change a published plugin audience without a content release
// @Description Owner-only for published tenant Skills, Connectors, Experts and Expert Teams. Keeps immutable content, version labels, release count and editable drafts unchanged. Widening to Space follows its review policy and keeps the previous effective audience until approval. Preview impacts first. Confirmed narrowing suspends affected published dependents. Widening batches owned private dependencies in one atomic review decision. Connectors use expected_revision; versioned types use expected_published_version_id. A changed or missing impact confirmation is refused. Concurrent content/review changes are checked under a resource lock. Non-owners and other Spaces receive an opaque 404.
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
	result, err := svc.ChangeVisibility(c.Request.Context(), who, pluginsvc.VisibilityParams{ExpectedRevision: req.ExpectedRevision, PlanRevision: req.PlanRevision, Confirm: req.Confirmed, PluginID: c.Param("plugin_id"), ExpectedPublishedVersionID: req.ExpectedPublishedVersionID, Visibility: model.PluginVisibility(req.Visibility)})
	if err != nil {
		writeServiceError(c, err, "plugin.visibility")
		return
	}
	out := visibilityResponse{GroupID: result.GroupID, PlanRevision: result.PlanRevision, PluginID: result.Plugin.ID, Visibility: result.Plugin.Visibility, PublishedVersionID: result.PublishedVersionID, DisplayStatus: model.PluginDisplayStatusPublished}
	if result.ReviewID != "" {
		out.ReviewID = &result.ReviewID
	}
	if result.Pending {
		out.DisplayStatus = model.PluginDisplayStatusPendingReview
	}
	apiresponse.OK(c, out)
}

// PreviewVisibility godoc
// @Summary Preview a resource audience change and dependency impacts
// @Description Owner-only scoped preview over released and pinned dependency graphs. Returns owned private dependencies to widen and readable affected resources to suspend. No content, files, versions or audience is changed. Submit plan_revision with is_confirmed=true when the plan includes dependencies or impacts. A refreshed plan is required after intervening resource changes.
// @Tags plugin
// @ID plugin.visibility.preview
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param visibility query string true "Target resource audience" Enums(private,space)
// @Success 200 {object} apiresponse.Data[pluginrepo.VisibilityPlan]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/visibility_preview [get]
func (h *Handler) PreviewVisibility(c *gin.Context) {
	who, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	svc, ok := h.svc.(interface {
		PreviewVisibility(context.Context, pluginsvc.Caller, string, model.PluginVisibility) (*pluginrepo.VisibilityPlan, error)
	})
	if !ok {
		writeServiceError(c, pluginsvc.ErrNotFound, "plugin.visibility_preview")
		return
	}
	out, err := svc.PreviewVisibility(c.Request.Context(), who, c.Param("plugin_id"), model.PluginVisibility(c.Query("visibility")))
	if err != nil {
		writeServiceError(c, err, "plugin.visibility_preview")
		return
	}
	apiresponse.OK(c, out)
}
