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
	ExpectedPublishedVersionID string `json:"expected_published_version_id" binding:"required"`
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
	result, err := svc.ChangeVisibility(c.Request.Context(), who, pluginsvc.VisibilityParams{PluginID: c.Param("plugin_id"), ExpectedPublishedVersionID: req.ExpectedPublishedVersionID, Visibility: model.PluginVisibility(req.Visibility)})
	if err != nil {
		writeServiceError(c, err, "plugin.visibility")
		return
	}
	out := visibilityResponse{PluginID: result.Plugin.ID, Visibility: result.Plugin.Visibility, PublishedVersionID: result.PublishedVersionID, DisplayStatus: model.PluginDisplayStatusPublished}
	if result.ReviewID != "" {
		out.ReviewID = &result.ReviewID
	}
	if result.Pending {
		out.DisplayStatus = model.PluginDisplayStatusPendingReview
	}
	apiresponse.OK(c, out)
}
