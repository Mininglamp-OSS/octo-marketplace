package metrics

import (
	"errors"
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	metricssvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/metrics"
	"github.com/gin-gonic/gin"
)

// Handler handles HTTP requests for metrics tracking.
type Handler struct {
	svc *metricssvc.Service
}

// New creates a new metrics handler.
func New(svc *metricssvc.Service) *Handler {
	return &Handler{svc: svc}
}

// Register registers metrics routes on the given router group.
func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.POST("/metrics/track", h.Track)
}

type trackRequest struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	EventType    string `json:"event_type"`
}

// Track godoc
// @Summary Track metric event
// @Description Record a view event for a visible marketplace resource.
// @Tags metrics
// @ID metrics.track
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body trackRequest true "Metric event"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /metrics/track [post]
func (h *Handler) Track(c *gin.Context) {
	var req trackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "Invalid request body.", nil, "")
		return
	}

	// v1 only accepts event_type=view
	if req.EventType != "view" {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.MetricsUnsupportedEvent, "Unsupported event_type; only \"view\" is accepted.", map[string]any{"field": "event_type", "reason": "unsupported"}, "")
		return
	}

	if req.ResourceType != "skill" && req.ResourceType != "mcp" {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.MetricsUnsupportedResource, "Unsupported resource_type; only \"skill\" and \"mcp\" are accepted.", map[string]any{"field": "resource_type", "reason": "unsupported"}, "")
		return
	}

	if req.ResourceID == "" {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "resource_id is required.", nil, "")
		return
	}

	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "Authentication is required.", nil, "")
		return
	}

	caller := metricssvc.Caller{
		UID:     identity.UID,
		SpaceID: middleware.SpaceID(c),
	}

	err := h.svc.TrackView(c.Request.Context(), req.ResourceType, req.ResourceID, caller)
	if err != nil {
		switch {
		case errors.Is(err, metricssvc.ErrInvalidParam):
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "Invalid parameters.", nil, "")
		case errors.Is(err, metricssvc.ErrUnsupportedType):
			apiresponse.Fail(c, http.StatusBadRequest, errcode.MetricsUnsupportedResource, "Unsupported resource_type.", map[string]any{"field": "resource_type", "reason": "unsupported"}, "")
		case errors.Is(err, metricssvc.ErrResourceNotVisible):
			apiresponse.Fail(c, http.StatusNotFound, errcode.MetricsResourceNotVisible, "Resource not found or not visible.", map[string]any{"resource": req.ResourceType}, "")
		default:
			apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "Internal error.", nil, "")
		}
		return
	}

	apiresponse.Empty(c)
}
