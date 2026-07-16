package category

import (
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	categorysvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/category"
	"github.com/gin-gonic/gin"
)

// Handler handles HTTP requests for categories.
type Handler struct {
	svc *categorysvc.Service
}

// New creates a new category handler.
func New(svc *categorysvc.Service) *Handler {
	return &Handler{svc: svc}
}

// Register registers category routes on the given router group.
func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.GET("/skill/categories", h.List)
}

// List returns all categories with skill counts.
func (h *Handler) List(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": errcode.Unauthorized, "message": "unauthorized"})
		return
	}
	spaceID := middleware.SpaceID(c)

	items, err := h.svc.List(c.Request.Context(), spaceID, identity.UID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": errcode.InternalError, "message": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": items,
	})
}
