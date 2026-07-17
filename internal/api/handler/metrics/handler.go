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

// Track handles POST /api/v1/metrics/track.
func (h *Handler) Track(c *gin.Context) {
	var req trackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "Invalid request body.", nil, "")
		return
	}

	// v1 only accepts event_type=view
	if req.EventType != "view" {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.MetricsUnsupportedEvent, "Unsupported event_type; only \"view\" is accepted.", nil, "")
		return
	}

	// v1 only accepts resource_type=skill
	if req.ResourceType != "skill" {
		apiresponse.Fail(c, http.StatusBadRequest, errcode.MetricsUnsupportedResource, "Unsupported resource_type; only \"skill\" is accepted.", nil, "")
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
			apiresponse.Fail(c, http.StatusBadRequest, errcode.MetricsUnsupportedResource, "Unsupported resource_type.", nil, "")
		case errors.Is(err, metricssvc.ErrResourceNotVisible):
			apiresponse.Fail(c, http.StatusBadRequest, errcode.MetricsResourceNotVisible, "Resource not found or not visible.", nil, "")
		default:
			apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "Internal error.", nil, "")
		}
		return
	}

	c.Status(http.StatusNoContent)
}
