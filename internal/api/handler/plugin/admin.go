package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

// AdminService is the handler-facing boundary over the unified Plugin service's
// admin (cross-Space, no ownership check) operations.
type AdminService interface {
	AdminList(context.Context, pluginsvc.Caller, model.PluginType, model.PluginVisibility, pluginsvc.ListParams) ([]model.Plugin, int64, error)
	AdminDetail(context.Context, pluginsvc.Caller, string, bool) (*pluginsvc.Detail, error)
	AdminCreate(context.Context, pluginsvc.Caller, pluginsvc.WriteRequest) (*pluginsvc.Detail, error)
	AdminImportContainer(context.Context, pluginsvc.Caller, pluginsvc.ContainerImportParams) (*pluginsvc.Detail, error)
	AdminReuploadContainer(context.Context, pluginsvc.Caller, string, pluginsvc.ContainerImportParams) (*pluginsvc.Detail, error)
	AdminImport(context.Context, pluginsvc.Caller, pluginsvc.ImportParams) (*pluginsvc.Detail, error)
	AdminUpdate(context.Context, pluginsvc.Caller, string, pluginsvc.WriteRequest) (*pluginsvc.Detail, error)
	AdminUpdateRating(context.Context, pluginsvc.Caller, string, *int) (*model.Plugin, error)
	AdminDelete(context.Context, pluginsvc.Caller, string) error
	AdminSkillMarkdown(context.Context, pluginsvc.Caller, string) (string, error)
	AdminOpenSkillPackage(context.Context, pluginsvc.Caller, string) (*pluginsvc.SkillPackageStream, error)
	// MaxArchiveBytes is the hard ceiling the service enforces on an uploaded
	// container archive; the handler caps the multipart body at the same value so
	// the transport and service limits are one number (MAX_UPLOAD_MB-driven).
	MaxArchiveBytes() int64
}

// AdminCategoryService is the handler-facing boundary over the admin taxonomy
// surface: the usage-counted read plus create/update/delete over the unified
// plugin_categories table.
type AdminCategoryService interface {
	AdminListCategories(context.Context, model.PluginType) ([]model.PluginCategory, error)
	AdminCreateCategory(ctx context.Context, name, iconKey string, pluginTypes []model.PluginType, sortOrder int) (*model.PluginCategory, error)
	AdminUpdateCategory(ctx context.Context, id, name, iconKey string, pluginTypes []model.PluginType, sortOrder int) (*model.PluginCategory, error)
	AdminDeleteCategory(ctx context.Context, id string) error
}

// AdminHandler serves the marketplace-admin plugin surface (/api/v1/admin/plugins*).
// It mirrors the legacy per-type admin surfaces (system MCPs, global skills,
// skill categories) over the unified plugin service, cross-Space and without an
// ownership check. The route group is gated by the admin authenticator, so the
// route gate is the authorization; the handlers build an admin caller with no
// Space (system rows live outside the Space model).
type AdminHandler struct {
	svc  AdminService
	cats AdminCategoryService
}

func NewAdmin(svc AdminService, cats AdminCategoryService) *AdminHandler {
	return &AdminHandler{svc: svc, cats: cats}
}

// RegisterAdmin mounts the admin plugin routes behind the admin gate. Static
// sibling paths (/admin/plugin_categories) go in a separate group so they never
// collide with the /admin/plugins/:plugin_id wildcard.
func (h *AdminHandler) RegisterAdmin(r *gin.Engine, adminAuth *marketmiddleware.AdminAuthenticator) {
	plugins := r.Group("/api/v1/admin/plugins", adminAuth.Handler(marketmiddleware.RoleMarketAdmin))
	plugins.GET("", h.List)
	plugins.POST("", h.Create)
	plugins.POST("/import", h.ImportContainer)
	plugins.POST("/container_reupload/:plugin_id", h.ReuploadContainer)
	plugins.POST("/skill_import", h.SkillImport)
	plugins.POST("/skill_reupload/:plugin_id", h.SkillReupload)
	plugins.GET("/:plugin_id", h.Get)
	plugins.GET("/:plugin_id/skill_md", h.SkillMarkdown)
	plugins.GET("/:plugin_id/download", h.DownloadSkillPackage)
	plugins.PATCH("/:plugin_id", h.Update)
	plugins.PATCH("/:plugin_id/rating", h.UpdateRating)
	plugins.DELETE("/:plugin_id", h.Delete)

	admin := r.Group("/api/v1/admin", adminAuth.Handler(marketmiddleware.RoleMarketAdmin))
	admin.GET("/plugin_categories", h.ListCategories)
	admin.POST("/plugin_categories", h.CreateCategory)
	admin.PATCH("/plugin_categories/:category_id", h.UpdateCategory)
	admin.DELETE("/plugin_categories/:category_id", h.DeleteCategory)
}

// adminCaller builds a caller from the admin identity. Unlike caller(c) it does
// not require a Space (admin routes carry none); the role gate already admitted
// the request, so any identity here is an admin.
func adminCaller(c *gin.Context) (pluginsvc.Caller, bool) {
	identity, ok := marketmiddleware.Identity(c)
	if !ok || identity.UID == "" {
		return pluginsvc.Caller{}, false
	}
	return pluginsvc.Caller{
		UID:           identity.UID,
		Name:          identity.Name,
		RequestID:     logging.RequestIDFromGin(c),
		IsSystemAdmin: true, // admitted through the admin gate
	}, true
}

// List godoc
// @Summary List plugins (admin)
// @Description List plugins of one type across all Spaces, optionally narrowed to a visibility class (e.g. system connectors). Admin only.
// @Tags admin_plugin
// @ID admin_plugin.list
// @Produce json
// @Security Bearer
// @Param plugin_type query string true "Plugin type" Enums(expert,expert_team,skill,connector)
// @Param visibility query string false "Filter by visibility" Enums(space,private,system)
// @Param q query string false "Search keyword"
// @Param category_id query string false "Category id"
// @Param sort query string false "Sort mode"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[listItemResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins [get]
func (h *AdminHandler) List(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	page, pageSize, ok := pagination(c)
	if !ok {
		validation(c, "pagination")
		return
	}
	pluginType := model.PluginType(c.Query("plugin_type"))
	if pluginType == "" {
		validation(c, "plugin_type")
		return
	}
	items, total, err := h.svc.AdminList(c.Request.Context(), caller, pluginType, model.PluginVisibility(c.Query("visibility")), pluginsvc.ListParams{
		CategoryID: c.Query("category_id"), Tags: splitQuery(c.QueryArray("tag")), Keyword: c.Query("q"), Sort: c.Query("sort"), Limit: pageSize, Offset: (page - 1) * pageSize,
	})
	if err != nil {
		writeServiceError(c, err, "plugin.admin.list")
		return
	}
	out := make([]listItemResponse, len(items))
	for i := range items {
		out[i] = listItemDTO(&items[i])
	}
	apiresponse.Offset(c, out, int(total), page, pageSize)
}

// Get godoc
// @Summary Get plugin (admin)
// @Description Return any plugin's full detail by id, ignoring Space scope. Admin only.
// @Tags admin_plugin
// @ID admin_plugin.get
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param include_relations query bool false "Include one-level relations (default true)"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id} [get]
func (h *AdminHandler) Get(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	includeRelations := true
	if raw, present := c.GetQuery("include_relations"); present {
		var err error
		includeRelations, err = strconv.ParseBool(raw)
		if err != nil {
			validation(c, "include_relations")
			return
		}
	}
	v, err := h.svc.AdminDetail(c.Request.Context(), caller, c.Param("plugin_id"), includeRelations)
	if err != nil {
		writeServiceError(c, err, "plugin.admin.get")
		return
	}
	apiresponse.OK(c, detailDTO(v))
}

// SkillMarkdown godoc
// @Summary Read skill plugin SKILL.md (admin)
// @Description Return the SKILL.md text of any skill plugin by id, read cross-Space from the unified plugins table. Serves embedded (bundled) skills via their id too. Admin only.
// @Tags admin_plugin
// @ID admin_plugin.skill_md
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Success 200 {object} apiresponse.Data[skillMarkdownResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id}/skill_md [get]
func (h *AdminHandler) SkillMarkdown(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	content, err := h.svc.AdminSkillMarkdown(c.Request.Context(), caller, c.Param("plugin_id"))
	if err != nil {
		writeServiceError(c, err, "plugin.admin.skill_md")
		return
	}
	apiresponse.OK(c, skillMarkdownResponse{Content: content})
}

// DownloadSkillPackage godoc
// @Summary Download skill plugin package (admin)
// @Description Stream the reconstructed package zip of any skill plugin by id, read cross-Space from the unified plugins table (no presigned URL). Serves embedded (bundled) skills via their id too. Admin only.
// @Tags admin_plugin
// @ID admin_plugin.download
// @Accept json
// @Produce application/zip
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Success 200 {file} binary "Skill package zip"
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id}/download [get]
func (h *AdminHandler) DownloadSkillPackage(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	result, err := h.svc.AdminOpenSkillPackage(c.Request.Context(), caller, c.Param("plugin_id"))
	if err != nil {
		writeServiceError(c, err, "plugin.admin.download")
		return
	}
	// Reconstruct/copy the zip into a bounded buffer FIRST: writeSkillZip caps the
	// aggregate size and fails closed on an integrity mismatch, so a mid-stream
	// error must surface as a proper error code rather than a truncated archive
	// already committed under a 200.
	var buf bytes.Buffer
	if err := result.Write(&buf); err != nil {
		writeServiceError(c, err, "plugin.admin.download")
		return
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", contentDisposition(result.FileName))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Length", strconv.Itoa(buf.Len()))
	c.Status(http.StatusOK)
	if _, err := c.Writer.Write(buf.Bytes()); err != nil {
		logging.Error("plugin_admin_skill_package_stream_failed", logging.ErrorField(err))
	}
}

// Create godoc
// @Summary Create plugin (admin)
// @Description Mint a system connector or system-visible skill/expert. Visibility and Space are set by convention (connector=system/NULL Space; others=system/global). Admin only.
// @Tags admin_plugin
// @ID admin_plugin.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body upsertRequest true "Plugin document"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins [post]
func (h *AdminHandler) Create(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req upsertRequest
	if !decode(c, &req) {
		return
	}
	v, err := h.svc.AdminCreate(c.Request.Context(), caller, req.serviceRequest())
	if err != nil {
		writeServiceError(c, err, "plugin.admin.create")
		return
	}
	apiresponse.OK(c, detailDTO(v))
}

// ImportContainer godoc
// @Summary Import expert/expert_team container (admin)
// @Description Ingest an uploaded expert or expert_team container archive (multipart file field "file") and store it as the unified plugin graph in one transaction: the expert/team plugin, its bundled skills as separate skill plugins, its squad members as separate expert plugins, and the relations wiring them. Admin only.
// @Tags admin_plugin
// @ID admin_plugin.import
// @Accept multipart/form-data
// @Produce json
// @Security Bearer
// @Param file formData string true "Expert/expert_team container zip (binary)"
// @Param category_id formData string false "Optional plugin category for the top-level expert/team"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/import [post]
func (h *AdminHandler) ImportContainer(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	params, ok := readContainerParams(c, h.svc.MaxArchiveBytes())
	if !ok {
		return
	}
	v, err := h.svc.AdminImportContainer(c.Request.Context(), caller, params)
	if err != nil {
		writeServiceError(c, err, "plugin.admin.import")
		return
	}
	apiresponse.OK(c, detailDTO(v))
}

// readContainerParams reads the shared expert/expert_team container upload:
// the multipart "file" field (the container zip) and the optional "category_id"
// form field. It caps the body at maxBytes (the service's MaxArchiveBytes, so the
// transport and service limits are one number) and reports PAYLOAD_TOO_LARGE /
// VALIDATION_ERROR itself, returning ok=false once a response has been written.
// The import and container-reupload routes share it so both enforce the same
// limits and shape.
func readContainerParams(c *gin.Context, maxBytes int64) (pluginsvc.ContainerImportParams, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge, "container archive is too large", map[string]any{"max_bytes": maxBytes}, "Reduce the archive size and try again.")
			return pluginsvc.ContainerImportParams{}, false
		}
		validation(c, "file")
		return pluginsvc.ContainerImportParams{}, false
	}
	if fileHeader.Size > maxBytes {
		apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge, "container archive is too large", map[string]any{"max_bytes": maxBytes}, "Reduce the archive size and try again.")
		return pluginsvc.ContainerImportParams{}, false
	}
	f, err := fileHeader.Open()
	if err != nil {
		validation(c, "file")
		return pluginsvc.ContainerImportParams{}, false
	}
	defer f.Close()
	archive, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(archive)) > maxBytes {
		apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge, "container archive is too large", map[string]any{"max_bytes": maxBytes}, "Reduce the archive size and try again.")
		return pluginsvc.ContainerImportParams{}, false
	}
	params := pluginsvc.ContainerImportParams{Archive: archive}
	if categoryID := strings.TrimSpace(c.PostForm("category_id")); categoryID != "" {
		params.CategoryID = &categoryID
	}
	return params, true
}

// ReuploadContainer godoc
// @Summary Reupload expert/expert_team container (admin)
// @Description Re-upload an expert or expert_team container archive (multipart file field "file") to rebuild an EXISTING plugin in place, preserving its plugin_id, visibility, Space, owner, and market placement while replacing its package/manifest/tags and swapping its embedded children (bundled skills for an expert; member experts and their skills for a squad). The container kind must match the existing plugin's type. Admin only.
// @Tags admin_plugin
// @ID admin_plugin.container_reupload
// @Accept multipart/form-data
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param file formData string true "Expert/expert_team container zip (binary)"
// @Param category_id formData string false "Optional plugin category for the top-level expert/team"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/container_reupload/{plugin_id} [post]
func (h *AdminHandler) ReuploadContainer(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	params, ok := readContainerParams(c, h.svc.MaxArchiveBytes())
	if !ok {
		return
	}
	v, err := h.svc.AdminReuploadContainer(c.Request.Context(), caller, c.Param("plugin_id"), params)
	if err != nil {
		writeServiceError(c, err, "plugin.admin.container_reupload")
		return
	}
	apiresponse.OK(c, detailDTO(v))
}

// adminSkillImportRequest is the JSON body for the admin skill import/reupload
// routes. The parse task carries the uploaded package; these fields override or
// supply the plugin metadata. category_id is a unified plugin_categories id.
type adminSkillImportRequest struct {
	ParseTaskID string   `json:"parse_task_id"`
	Name        string   `json:"name"`
	CategoryID  string   `json:"category_id"`
	Tags        []string `json:"tags"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Changelog   string   `json:"changelog"`
}

func (r adminSkillImportRequest) params(pluginID string) pluginsvc.ImportParams {
	p := pluginsvc.ImportParams{
		ParseTaskID: r.ParseTaskID,
		PluginID:    pluginID,
		Name:        r.Name,
		Description: r.Description,
		Tags:        r.Tags,
		Version:     r.Version,
	}
	if strings.TrimSpace(r.CategoryID) != "" {
		p.CategoryID = &r.CategoryID
	}
	if strings.TrimSpace(r.Changelog) != "" {
		p.Changelog = &r.Changelog
	}
	return p
}

// SkillImport godoc
// @Summary Import skill plugin (admin)
// @Description Create a system-visible skill plugin from a completed admin upload-parse task, under the admin conventions (system visibility, global Space, a default visible market placement). The category_id is a unified plugin_categories id. Admin only.
// @Tags admin_plugin
// @ID admin_plugin.skill_import
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body adminSkillImportRequest true "Skill import request"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/skill_import [post]
func (h *AdminHandler) SkillImport(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req adminSkillImportRequest
	if !decode(c, &req) {
		return
	}
	v, err := h.svc.AdminImport(c.Request.Context(), caller, req.params(""))
	if err != nil {
		writeServiceError(c, err, "plugin.admin.skill_import")
		return
	}
	apiresponse.OK(c, detailDTO(v))
}

// SkillReupload godoc
// @Summary Reupload skill plugin (admin)
// @Description Replace an existing skill plugin's package from a completed admin upload-parse task, preserving its visibility, Space, and owner. The category_id is a unified plugin_categories id. Admin only.
// @Tags admin_plugin
// @ID admin_plugin.skill_reupload
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param body body adminSkillImportRequest true "Skill reupload request"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/skill_reupload/{plugin_id} [post]
func (h *AdminHandler) SkillReupload(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req adminSkillImportRequest
	if !decode(c, &req) {
		return
	}
	v, err := h.svc.AdminImport(c.Request.Context(), caller, req.params(c.Param("plugin_id")))
	if err != nil {
		writeServiceError(c, err, "plugin.admin.skill_reupload")
		return
	}
	apiresponse.OK(c, detailDTO(v))
}

// Update godoc
// @Summary Update plugin (admin)
// @Description Update any plugin by id, ignoring owner/Space. Admin only.
// @Tags admin_plugin
// @ID admin_plugin.update
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param body body upsertRequest true "Plugin document"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id} [patch]
func (h *AdminHandler) Update(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req upsertRequest
	if !decode(c, &req) {
		return
	}
	v, err := h.svc.AdminUpdate(c.Request.Context(), caller, c.Param("plugin_id"), req.serviceRequest())
	if err != nil {
		writeServiceError(c, err, "plugin.admin.update")
		return
	}
	apiresponse.OK(c, detailDTO(v))
}

type ratingUpdateRequest struct {
	Rating *int `json:"rating" binding:"required" minimum:"1" maximum:"5" extensions:"x-nullable"`
}

type ratingUpdateResponse struct {
	PluginID string `json:"plugin_id"`
	Rating   *int   `json:"rating" minimum:"1" maximum:"5" extensions:"x-nullable"`
}

// UpdateRating godoc
// @Summary Update plugin rating (admin)
// @Description Set or clear any plugin's administrator rating without changing plugin content or versions. Admin only.
// @Tags admin_plugin
// @ID admin_plugin.rating.update
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param body body ratingUpdateRequest true "Nullable administrator rating (1-5)"
// @Success 200 {object} apiresponse.Data[ratingUpdateResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id}/rating [patch]
func (h *AdminHandler) UpdateRating(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var raw map[string]json.RawMessage
	if !decode(c, &raw) {
		return
	}
	value, present := raw["rating"]
	if !present || len(raw) != 1 {
		validation(c, "rating")
		return
	}
	var rating *int
	if string(value) != "null" {
		var parsed int
		if err := json.Unmarshal(value, &parsed); err != nil {
			validation(c, "rating")
			return
		}
		rating = &parsed
	}
	p, err := h.svc.AdminUpdateRating(c.Request.Context(), caller, c.Param("plugin_id"), rating)
	if err != nil {
		writeServiceError(c, err, "plugin.admin.rating.update")
		return
	}
	apiresponse.OK(c, ratingUpdateResponse{PluginID: p.ID, Rating: p.Rating})
}

// Delete godoc
// @Summary Delete plugin (admin)
// @Description Soft-delete any plugin by id, ignoring owner/Space. Admin only.
// @Tags admin_plugin
// @ID admin_plugin.delete
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Success 200 {object} apiresponse.Data[deleteResponse]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id} [delete]
func (h *AdminHandler) Delete(c *gin.Context) {
	caller, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	id := c.Param("plugin_id")
	if err := h.svc.AdminDelete(c.Request.Context(), caller, id); err != nil {
		writeServiceError(c, err, "plugin.admin.delete")
		return
	}
	apiresponse.OK(c, deleteResponse{PluginID: id, Deleted: true})
}

// ListCategories godoc
// @Summary List plugin categories (admin)
// @Description List every category applicable to a plugin type with its live-plugin usage count, including empty ones. Admin only.
// @Tags admin_plugin
// @ID admin_plugin_category.list
// @Produce json
// @Security Bearer
// @Param plugin_type query string true "Plugin type"
// @Success 200 {object} apiresponse.Data[[]categoryResponse]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_categories [get]
func (h *AdminHandler) ListCategories(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	items, err := h.cats.AdminListCategories(c.Request.Context(), model.PluginType(c.Query("plugin_type")))
	if err != nil {
		writeServiceError(c, err, "plugin.admin.category.list")
		return
	}
	out := make([]categoryResponse, len(items))
	for i, x := range items {
		out[i] = categoryResponse{CategoryID: x.ID, Name: x.Name, IconKey: x.IconKey, PluginTypes: stringSlice(x.PluginTypes), SortOrder: x.SortOrder, PluginCount: x.PluginCount}
	}
	apiresponse.OK(c, out)
}

// categoryWriteRequest is the create/update body for the admin taxonomy surface.
type categoryWriteRequest struct {
	Name        string             `json:"name" binding:"required"`
	IconKey     string             `json:"icon_key"`
	PluginTypes []model.PluginType `json:"plugin_types"`
	SortOrder   int                `json:"sort_order"`
}

func categoryDTO(c *model.PluginCategory) categoryResponse {
	if c == nil {
		return categoryResponse{PluginTypes: []string{}}
	}
	return categoryResponse{CategoryID: c.ID, Name: c.Name, IconKey: c.IconKey, PluginTypes: stringSlice(c.PluginTypes), SortOrder: c.SortOrder, PluginCount: c.PluginCount}
}

// CreateCategory godoc
// @Summary Create plugin category (admin)
// @Description Create a taxonomy row applicable to one or more plugin types. Admin only.
// @Tags admin_plugin
// @ID admin_plugin_category.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body categoryWriteRequest true "Category document"
// @Success 200 {object} apiresponse.Data[categoryResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_categories [post]
func (h *AdminHandler) CreateCategory(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	var req categoryWriteRequest
	if !decode(c, &req) {
		return
	}
	cat, err := h.cats.AdminCreateCategory(c.Request.Context(), req.Name, req.IconKey, req.PluginTypes, req.SortOrder)
	if err != nil {
		writeServiceError(c, err, "plugin.admin.category.create")
		return
	}
	apiresponse.OK(c, categoryDTO(cat))
}

// UpdateCategory godoc
// @Summary Update plugin category (admin)
// @Description Update an existing taxonomy row's editable fields (name, icon, plugin types, sort order). Admin only.
// @Tags admin_plugin
// @ID admin_plugin_category.update
// @Accept json
// @Produce json
// @Security Bearer
// @Param category_id path string true "Category ID"
// @Param body body categoryWriteRequest true "Category document"
// @Success 200 {object} apiresponse.Data[categoryResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_categories/{category_id} [patch]
func (h *AdminHandler) UpdateCategory(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	var req categoryWriteRequest
	if !decode(c, &req) {
		return
	}
	cat, err := h.cats.AdminUpdateCategory(c.Request.Context(), c.Param("category_id"), req.Name, req.IconKey, req.PluginTypes, req.SortOrder)
	if err != nil {
		writeServiceError(c, err, "plugin.admin.category.update")
		return
	}
	apiresponse.OK(c, categoryDTO(cat))
}

// DeleteCategory godoc
// @Summary Delete plugin category (admin)
// @Description Soft-delete an unused taxonomy row. Categories still referenced by a live plugin are rejected with CONFLICT. Admin only.
// @Tags admin_plugin
// @ID admin_plugin_category.delete
// @Produce json
// @Security Bearer
// @Param category_id path string true "Category ID"
// @Success 200 {object} apiresponse.Data[apiresponse.EmptyResp]
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugin_categories/{category_id} [delete]
func (h *AdminHandler) DeleteCategory(c *gin.Context) {
	if _, ok := adminCaller(c); !ok {
		unauthorized(c)
		return
	}
	if err := h.cats.AdminDeleteCategory(c.Request.Context(), c.Param("category_id")); err != nil {
		writeServiceError(c, err, "plugin.admin.category.delete")
		return
	}
	apiresponse.Empty(c)
}
