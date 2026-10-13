package plugin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type AdminSceneService interface {
	AdminListScenes(context.Context) ([]model.PluginScene, error)
	AdminCreateScene(context.Context, pluginsvc.SceneCreateParams) (*model.PluginScene, error)
	AdminUpdateScene(context.Context, string, pluginsvc.SceneUpdateParams) (*model.PluginScene, error)
	AdminDeleteScene(context.Context, string) error
	AdminListPlacements(context.Context, pluginsvc.PlacementListParams) ([]model.AdminPluginPlacement, int64, error)
	AdminCreatePlacement(context.Context, pluginsvc.PlacementCreateParams) (*model.AdminPluginPlacement, error)
	AdminUpdatePlacement(context.Context, string, pluginsvc.PlacementUpdateParams) (*model.AdminPluginPlacement, error)
	AdminDeletePlacement(context.Context, string) error
	AdminBatchSetPlacements(context.Context, pluginsvc.PlacementBatchParams) error
}

type SceneAdminHandler struct{ svc AdminSceneService }

func NewSceneAdmin(svc AdminSceneService) *SceneAdminHandler { return &SceneAdminHandler{svc: svc} }

func (h *SceneAdminHandler) RegisterAdmin(r *gin.Engine, adminAuth *marketmiddleware.AdminAuthenticator) {
	admin := r.Group("/api/v1/admin", adminAuth.Handler(marketmiddleware.RoleMarketAdmin))
	admin.GET("/plugin_scenes", h.ListScenes)
	admin.POST("/plugin_scenes", h.CreateScene)
	admin.PATCH("/plugin_scenes/:plugin_scene_id", h.UpdateScene)
	admin.DELETE("/plugin_scenes/:plugin_scene_id", h.DeleteScene)
	admin.GET("/plugin_placements", h.ListPlacements)
	admin.POST("/plugin_placements", h.CreatePlacement)
	admin.POST("/plugin_placements/_batch", h.BatchSetPlacements)
	admin.PATCH("/plugin_placements/:plugin_placement_id", h.UpdatePlacement)
	admin.DELETE("/plugin_placements/:plugin_placement_id", h.DeletePlacement)
}

type pluginSceneResponse struct {
	SceneID       string    `json:"scene_id"`
	SceneCode     string    `json:"scene_code"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	SortOrder     int       `json:"sort_order"`
	PluginCount   int       `json:"plugin_count"`
	CategoryCount int       `json:"category_count"`
	CreatedAt     time.Time `json:"created_at" swaggertype:"string,date-time"`
	UpdatedAt     time.Time `json:"updated_at" swaggertype:"string,date-time"`
}

type pluginSceneCreateRequest struct {
	SceneCode   string `json:"scene_code"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SortOrder   int    `json:"sort_order"`
}

type pluginSceneUpdateRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	SortOrder   *int    `json:"sort_order,omitempty"`
}

type pluginPlacementResponse struct {
	PlacementID  string           `json:"placement_id"`
	SceneID      string           `json:"scene_id"`
	SceneCode    string           `json:"scene_code"`
	SceneName    string           `json:"scene_name"`
	PluginID     string           `json:"plugin_id"`
	PluginName   string           `json:"plugin_name"`
	PluginType   model.PluginType `json:"plugin_type"`
	CategoryID   *string          `json:"category_id,omitempty"`
	CategoryName *string          `json:"category_name,omitempty"`
	IsVisible    bool             `json:"is_visible"`
	SortOrder    int              `json:"sort_order"`
	CreatedAt    time.Time        `json:"created_at" swaggertype:"string,date-time"`
	UpdatedAt    time.Time        `json:"updated_at" swaggertype:"string,date-time"`
}

type pluginPlacementCreateRequest struct {
	SceneCode string `json:"scene_code"`
	PluginID  string `json:"plugin_id"`
	IsVisible *bool  `json:"is_visible,omitempty"`
	SortOrder int    `json:"sort_order"`
}

type pluginPlacementUpdateRequest struct {
	IsVisible *bool `json:"is_visible,omitempty"`
	SortOrder *int  `json:"sort_order,omitempty"`
}

type pluginPlacementBatchRequest struct {
	SceneCode string   `json:"scene_code"`
	PluginIDs []string `json:"plugin_ids"`
	IsPlaced  *bool    `json:"is_placed"`
	IsVisible *bool    `json:"is_visible,omitempty"`
	SortOrder int      `json:"sort_order"`
}

type pluginPlacementBatchItemResponse struct {
	SceneCode string `json:"scene_code"`
	PluginID  string `json:"plugin_id"`
	IsPlaced  bool   `json:"is_placed"`
}

type pluginPlacementBatchResponse struct {
	Items []pluginPlacementBatchItemResponse `json:"items"`
}

// ListScenes godoc
// @Summary List plugin scenes
// @Description List configured Marketplace scenes with Plugin and category placement counts. Admin only.
// @Tags admin_plugin_scene
// @ID admin_plugin_scene.list
// @Accept json
// @Produce json
// @Security Bearer
// @Success 200 {object} apiresponse.Data[[]pluginSceneResponse]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_scenes [get]
func (h *SceneAdminHandler) ListScenes(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	items, err := h.svc.AdminListScenes(c.Request.Context())
	if err != nil {
		writeSceneError(c, err, "plugin.admin.scene.list", "plugin_scene")
		return
	}
	out := make([]pluginSceneResponse, len(items))
	for i := range items {
		out[i] = sceneDTO(&items[i])
	}
	apiresponse.OK(c, out)
}

// CreateScene godoc
// @Summary Create plugin scene
// @Description Create a stable Marketplace scene code and its display metadata. Admin only.
// @Tags admin_plugin_scene
// @ID admin_plugin_scene.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body pluginSceneCreateRequest true "Plugin scene"
// @Success 201 {object} apiresponse.Data[pluginSceneResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_scenes [post]
func (h *SceneAdminHandler) CreateScene(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	var req pluginSceneCreateRequest
	if !decode(c, &req) {
		return
	}
	scene, err := h.svc.AdminCreateScene(c.Request.Context(), pluginsvc.SceneCreateParams{Code: req.SceneCode, Name: req.Name, Description: req.Description, SortOrder: req.SortOrder})
	if err != nil {
		writeSceneError(c, err, "plugin.admin.scene.create", "plugin_scene")
		return
	}
	apiresponse.Created(c, sceneDTO(scene))
}

// UpdateScene godoc
// @Summary Update plugin scene
// @Description Update scene display metadata; scene_code is immutable. Admin only.
// @Tags admin_plugin_scene
// @ID admin_plugin_scene.update
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_scene_id path string true "Plugin scene ID"
// @Param body body pluginSceneUpdateRequest true "Plugin scene fields"
// @Success 200 {object} apiresponse.Data[pluginSceneResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_scenes/{plugin_scene_id} [patch]
func (h *SceneAdminHandler) UpdateScene(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	var req pluginSceneUpdateRequest
	if !decode(c, &req) {
		return
	}
	scene, err := h.svc.AdminUpdateScene(c.Request.Context(), c.Param("plugin_scene_id"), pluginsvc.SceneUpdateParams{Name: req.Name, Description: req.Description, SortOrder: req.SortOrder})
	if err != nil {
		writeSceneError(c, err, "plugin.admin.scene.update", "plugin_scene")
		return
	}
	apiresponse.OK(c, sceneDTO(scene))
}

// DeleteScene godoc
// @Summary Delete plugin scene
// @Description Delete an unused non-default Marketplace scene. Admin only.
// @Tags admin_plugin_scene
// @ID admin_plugin_scene.delete
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_scene_id path string true "Plugin scene ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_scenes/{plugin_scene_id} [delete]
func (h *SceneAdminHandler) DeleteScene(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	if err := h.svc.AdminDeleteScene(c.Request.Context(), c.Param("plugin_scene_id")); err != nil {
		writeSceneError(c, err, "plugin.admin.scene.delete", "plugin_scene")
		return
	}
	apiresponse.Empty(c)
}

// ListPlacements godoc
// @Summary List plugin placements
// @Description List Plugin-to-scene relationships across all Spaces. Admin only.
// @Tags admin_plugin_scene
// @ID admin_plugin_placement.list
// @Accept json
// @Produce json
// @Security Bearer
// @Param scene_code query string false "Scene code"
// @Param plugin_type query string false "Plugin type" Enums(expert,expert_team,skill,connector)
// @Param q query string false "Plugin name search query"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[pluginPlacementResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_placements [get]
func (h *SceneAdminHandler) ListPlacements(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	page, pageSize, ok := pagination(c)
	if !ok {
		validation(c, "pagination")
		return
	}
	items, total, err := h.svc.AdminListPlacements(c.Request.Context(), pluginsvc.PlacementListParams{SceneCode: c.Query("scene_code"), Type: model.PluginType(c.Query("plugin_type")), Keyword: c.Query("q"), Limit: pageSize, Offset: (page - 1) * pageSize})
	if err != nil {
		writeSceneError(c, err, "plugin.admin.placement.list", "plugin_placement")
		return
	}
	out := make([]pluginPlacementResponse, len(items))
	for i := range items {
		out[i] = placementDTO(&items[i])
	}
	apiresponse.Offset(c, out, int(total), page, pageSize)
}

// CreatePlacement godoc
// @Summary Create plugin placement
// @Description Place one live top-level Plugin in a configured Marketplace scene. Admin only.
// @Tags admin_plugin_scene
// @ID admin_plugin_placement.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body pluginPlacementCreateRequest true "Plugin placement"
// @Success 201 {object} apiresponse.Data[pluginPlacementResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_placements [post]
func (h *SceneAdminHandler) CreatePlacement(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	var req pluginPlacementCreateRequest
	if !decode(c, &req) {
		return
	}
	visible := true
	if req.IsVisible != nil {
		visible = *req.IsVisible
	}
	item, err := h.svc.AdminCreatePlacement(c.Request.Context(), pluginsvc.PlacementCreateParams{SceneCode: req.SceneCode, PluginID: req.PluginID, IsVisible: visible, SortOrder: req.SortOrder})
	if err != nil {
		writeSceneError(c, err, "plugin.admin.placement.create", "plugin_placement")
		return
	}
	apiresponse.Created(c, placementDTO(item))
}

// BatchSetPlacements godoc
// @Summary Batch update plugin placements
// @Description Atomically add, update, or remove one scene placement for up to 100 Plugins. Removing or hiding default placements is rejected.
// @Tags admin_plugin_scene
// @ID admin_plugin_placement.batch_update
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body pluginPlacementBatchRequest true "Batch placement update"
// @Success 200 {object} apiresponse.Data[pluginPlacementBatchResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_placements/_batch [post]
func (h *SceneAdminHandler) BatchSetPlacements(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	var req pluginPlacementBatchRequest
	if !decode(c, &req) {
		return
	}
	if req.IsPlaced == nil {
		validation(c, "is_placed")
		return
	}
	req.SceneCode = strings.TrimSpace(req.SceneCode)
	for i := range req.PluginIDs {
		req.PluginIDs[i] = strings.TrimSpace(req.PluginIDs[i])
	}
	visible := true
	if req.IsVisible != nil {
		visible = *req.IsVisible
	}
	if err := h.svc.AdminBatchSetPlacements(c.Request.Context(), pluginsvc.PlacementBatchParams{
		SceneCode: req.SceneCode, PluginIDs: req.PluginIDs, IsPlaced: *req.IsPlaced, IsVisible: visible, SortOrder: req.SortOrder,
	}); err != nil {
		writeSceneError(c, err, "plugin.admin.placement.batch_update", "plugin_placement")
		return
	}
	items := make([]pluginPlacementBatchItemResponse, len(req.PluginIDs))
	for i, pluginID := range req.PluginIDs {
		items[i] = pluginPlacementBatchItemResponse{SceneCode: req.SceneCode, PluginID: pluginID, IsPlaced: *req.IsPlaced}
	}
	apiresponse.OK(c, pluginPlacementBatchResponse{Items: items})
}

// UpdatePlacement godoc
// @Summary Update plugin placement
// @Description Update visibility or ordering for a Plugin placement. The default placement must remain visible. Admin only.
// @Tags admin_plugin_scene
// @ID admin_plugin_placement.update
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_placement_id path string true "Plugin placement ID"
// @Param body body pluginPlacementUpdateRequest true "Plugin placement fields"
// @Success 200 {object} apiresponse.Data[pluginPlacementResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_placements/{plugin_placement_id} [patch]
func (h *SceneAdminHandler) UpdatePlacement(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	var req pluginPlacementUpdateRequest
	if !decode(c, &req) {
		return
	}
	item, err := h.svc.AdminUpdatePlacement(c.Request.Context(), c.Param("plugin_placement_id"), pluginsvc.PlacementUpdateParams{IsVisible: req.IsVisible, SortOrder: req.SortOrder})
	if err != nil {
		writeSceneError(c, err, "plugin.admin.placement.update", "plugin_placement")
		return
	}
	apiresponse.OK(c, placementDTO(item))
}

// DeletePlacement godoc
// @Summary Delete plugin placement
// @Description Remove a Plugin from a non-default Marketplace scene. Admin only.
// @Tags admin_plugin_scene
// @ID admin_plugin_placement.delete
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_placement_id path string true "Plugin placement ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_placements/{plugin_placement_id} [delete]
func (h *SceneAdminHandler) DeletePlacement(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	if err := h.svc.AdminDeletePlacement(c.Request.Context(), c.Param("plugin_placement_id")); err != nil {
		writeSceneError(c, err, "plugin.admin.placement.delete", "plugin_placement")
		return
	}
	apiresponse.Empty(c)
}

func sceneDTO(scene *model.PluginScene) pluginSceneResponse {
	if scene == nil {
		return pluginSceneResponse{}
	}
	return pluginSceneResponse{SceneID: scene.ID, SceneCode: scene.Code, Name: scene.Name, Description: scene.Description, SortOrder: scene.SortOrder, PluginCount: scene.PluginCount, CategoryCount: scene.CategoryCount, CreatedAt: scene.CreatedAt, UpdatedAt: scene.UpdatedAt}
}

func placementDTO(item *model.AdminPluginPlacement) pluginPlacementResponse {
	if item == nil {
		return pluginPlacementResponse{}
	}
	return pluginPlacementResponse{PlacementID: item.ID, SceneID: item.SceneID, SceneCode: item.SceneCode, SceneName: item.SceneName, PluginID: item.PluginID, PluginName: item.PluginName, PluginType: item.PluginType, CategoryID: item.CategoryID, CategoryName: item.CategoryName, IsVisible: item.IsVisible, SortOrder: item.SortOrder, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
}

func writeSceneError(c *gin.Context, err error, operation, resource string) {
	var batchItemErr *pluginsvc.BatchItemError
	errors.As(err, &batchItemErr)
	switch {
	case errors.Is(err, pluginsvc.ErrInvalidRequest):
		if batchItemErr != nil {
			apiresponse.Fail(c, http.StatusBadRequest, errcode.BadRequest, "request validation failed", map[string]any{"field": "body", "reason": "invalid", "failed_index": batchItemErr.Index}, "Correct the request and try again.")
			return
		}
		validation(c, "body")
	case errors.Is(err, pluginsvc.ErrNotFound):
		details := map[string]any{"resource": resource}
		if batchItemErr != nil {
			details["failed_index"] = batchItemErr.Index
		}
		apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "marketplace configuration not found", details, "Refresh and try again.")
	case errors.Is(err, pluginsvc.ErrConflict):
		details := map[string]any{"conflict_reason": "in_use_or_duplicate"}
		if batchItemErr != nil {
			details["failed_index"] = batchItemErr.Index
		}
		apiresponse.Fail(c, http.StatusConflict, errcode.Conflict, "marketplace configuration conflicts with existing data", details, "Remove dependent placements or choose a different value.")
	default:
		apiresponse.Internal(c, err, operation)
	}
}
