package handler

import (
	"log"
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/gin-gonic/gin"
)

// Session handles current-user session routes.
type Session struct{}

// NewSession creates a session handler.
func NewSession() *Session {
	return &Session{}
}

// Register registers session routes on the authenticated v1 group.
func (h *Session) Register(rg *gin.RouterGroup) {
	rg.GET("/session", h.Get)
}

// SessionResponse is the current authenticated user context.
type SessionResponse struct {
	UID     string `json:"uid"`
	Name    string `json:"name"`
	SpaceID string `json:"space_id"`
}

// Get godoc
// @Summary Get current session
// @Description Return the authenticated user identity and active Space context.
// @Tags session
// @ID session.get
// @Accept json
// @Produce json
// @Security Bearer
// @Success 200 {object} apiresponse.Data[SessionResponse]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /session [get]
func (h *Session) Get(c *gin.Context) {
	identity, ok := marketmiddleware.Identity(c)
	if !ok {
		log.Printf("[session] get failed: identity not found in context")
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}
	apiresponse.OK(c, SessionResponse{
		UID:     identity.UID,
		Name:    identity.Name,
		SpaceID: marketmiddleware.SpaceID(c),
	})
}
