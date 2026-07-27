package category

import (
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	categorysvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/category"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/gin-gonic/gin"
)

// Handler handles HTTP requests for categories.
type Handler struct {
	svc   *categorysvc.Service
	idGen func() string
}

// New creates a new category handler.
func New(svc *categorysvc.Service) *Handler {
	return &Handler{svc: svc}
}

// Register registers category routes on the given router group.
func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.GET("/skill_categories", h.List)
	rg.GET("/skill/categories", legacyCategoryEndpoint, h.List)
}

// List godoc
// @Summary List Skill categories
// @Description Return all Skill categories and their visible Skill counts.
// @Tags skill_category
// @ID skill_category.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param q query string false "Fuzzy search keyword"
// @Param tags query string false "Comma-separated tag names"
// @Param tag query []string false "Repeated tag names"
// @Success 200 {object} apiresponse.Data[[]categorysvc.CategoryItem]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /skill_categories [get]
func (h *Handler) List(c *gin.Context) {
	identity, ok := middleware.Identity(c)
	if !ok {
		apiresponse.Fail(c, http.StatusUnauthorized, errcode.Unauthorized, "unauthorized", nil, "")
		return
	}
	spaceID := middleware.SpaceID(c)

	tags := append([]string{}, c.QueryArray("tags")...)
	tags = append(tags, c.QueryArray("tag")...)
	items, err := h.svc.List(c.Request.Context(), categorysvc.ListParams{
		SpaceID: spaceID,
		UserID:  identity.UID,
		Query:   c.Query("q"),
		Tags:    skillsvc.ParseTagFilters(tags...),
	})
	if err != nil {
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}

	apiresponse.OK(c, items)
}

func legacyCategoryEndpoint(c *gin.Context) {
	c.Header("Deprecation", "true")
	c.Header("Sunset", "Thu, 01 Oct 2026 00:00:00 GMT")
	c.Header("Link", "</api/v1/skill_categories>; rel=\"successor-version\"")
	c.Next()
}
