package plugin

import (
	"context"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type distributionService interface {
	Distribution(context.Context, pluginsvc.Caller, string) (*pluginsvc.DistributionBundle, error)
}

// GetDistribution godoc
// @Summary Get complete current expert or team content
// @Description Resolves all published dependencies and effective connectors in one snapshot, verifies original files, and returns the complete graph and content. Does not install resources or call Fleet.
// @Tags plugin
// @ID plugin.distribution.get
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Expert or team plugin ID"
// @Success 200 {object} apiresponse.Data[pluginsvc.DistributionBundle]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/distribution [get]
func (h *Handler) GetDistribution(c *gin.Context) {
	who, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	svc, ok := h.svc.(distributionService)
	if !ok {
		writeServiceError(c, pluginsvc.ErrNotFound, "plugin.distribution.get")
		return
	}
	out, err := svc.Distribution(c.Request.Context(), who, c.Param("plugin_id"))
	if err != nil {
		writeServiceError(c, err, "plugin.distribution.get")
		return
	}
	c.Header("Cache-Control", "no-store")
	apiresponse.OK(c, out)
}
