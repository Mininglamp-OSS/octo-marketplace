package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"time"

	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type versionManagementService interface {
	CurrentDetail(context.Context, pluginsvc.Caller, string) (*pluginsvc.Detail, error)
	ManagedVersion(context.Context, pluginsvc.Caller, string, string, bool) (*model.PluginVersion, error)
	HistoricalVersions(context.Context, pluginsvc.Caller, string, int, int, bool) ([]model.PluginVersion, int64, error)
	VersionFile(context.Context, pluginsvc.Caller, string, string, string, bool) ([]byte, error)
	RestoreVersion(context.Context, pluginsvc.Caller, string, string, string, bool) (*pluginsvc.Detail, error)
	AdminPublishVersion(context.Context, pluginsvc.Caller, string) (*pluginsvc.Detail, error)
}

type restoreVersionRequest struct {
	Version string `json:"version" binding:"required"`
}

type versionReleaseResponse struct {
	VersionLabel string    `json:"version_label"`
	ReleasedBy   string    `json:"released_by"`
	ReleasedAt   time.Time `json:"released_at" swaggertype:"string,date-time"`
}

type historicalVersionResponse struct {
	Version      versionResponse `json:"version"`
	ManifestJSON json.RawMessage `json:"manifest_json" swaggertype:"object"`
	PluginJSON   json.RawMessage `json:"plugin_json" swaggertype:"object"`
}

func historicalVersionDTO(v model.PluginVersion) historicalVersionResponse {
	return historicalVersionResponse{Version: versionDTO(v), ManifestJSON: normalizedObjectRaw(v.Manifest), PluginJSON: normalizedObjectRaw(v.Package)}
}

func versionService(c *gin.Context, svc any) (versionManagementService, bool) {
	v, ok := svc.(versionManagementService)
	if !ok {
		writeServiceError(c, pluginsvc.ErrNotFound, "plugin.version")
		return nil, false
	}
	return v, true
}

func getVersion(c *gin.Context, svc any, admin bool) {
	who, ok := caller(c)
	if admin {
		who, ok = adminCaller(c)
	}
	if !ok {
		unauthorized(c)
		return
	}
	v, ok := versionService(c, svc)
	if !ok {
		return
	}
	result, err := v.ManagedVersion(c.Request.Context(), who, c.Param("plugin_id"), c.Param("version_id"), admin)
	if err != nil {
		writeServiceError(c, err, "plugin.version.get")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	apiresponse.OK(c, historicalVersionDTO(*result))
}
func downloadVersionFile(c *gin.Context, svc any, admin bool) {
	who, ok := caller(c)
	if admin {
		who, ok = adminCaller(c)
	}
	if !ok {
		unauthorized(c)
		return
	}
	v, ok := versionService(c, svc)
	if !ok {
		return
	}
	name := c.Query("path")
	data, err := v.VersionFile(c.Request.Context(), who, c.Param("plugin_id"), c.Param("version_id"), name, admin)
	if err != nil {
		writeServiceError(c, err, "plugin.version.download")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Disposition", contentDisposition(path.Base(name)))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "application/octet-stream", data)
}
func restoreVersion(c *gin.Context, svc any, admin bool) {
	who, ok := caller(c)
	if admin {
		who, ok = adminCaller(c)
	}
	if !ok {
		unauthorized(c)
		return
	}
	v, ok := versionService(c, svc)
	if !ok {
		return
	}
	var body restoreVersionRequest
	if !decode(c, &body) {
		return
	}
	result, err := v.RestoreVersion(c.Request.Context(), who, c.Param("plugin_id"), c.Param("version_id"), body.Version, admin)
	if err != nil {
		writeServiceError(c, err, "plugin.version.restore")
		return
	}
	apiresponse.OK(c, detailDTO(result))
}

// GetVersion godoc
// @Summary Get a historical plugin version
// @Description Requires enabled version management and current access. Authors and marketplace admins may manage drafts; other readers can access formally released versions only. Original snapshots are immutable. Restoration creates a new draft and requires publication.
// @Tags plugin
// @ID plugin.version.get
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param version_id path string true "Version ID belonging to this plugin"
// @Success 200 {object} apiresponse.Data[historicalVersionResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/versions/{version_id} [get]
func (h *Handler) GetVersion(c *gin.Context) { getVersion(c, h.svc, false) }

// DownloadVersionFile godoc
// @Summary Download an original historical file
// @Description Requires enabled version management and current access. Authors and marketplace admins may manage drafts; other readers can access formally released versions only. Original snapshots are immutable. Restoration creates a new draft and requires publication.
// @Tags plugin
// @ID plugin.version.file.download
// @Accept json
// @Produce octet-stream
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param version_id path string true "Version ID belonging to this plugin"
// @Param path query string true "Relative file path in this historical version"
// @Success 200 {file} binary
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/versions/{version_id}/file [get]
func (h *Handler) DownloadVersionFile(c *gin.Context) { downloadVersionFile(c, h.svc, false) }

// RestoreVersion godoc
// @Summary Restore a historical version to a new draft
// @Description Requires enabled version management and current access. Authors and marketplace admins may manage drafts; other readers can access formally released versions only. Original snapshots are immutable. Restoration creates a new draft and requires publication.
// @Tags plugin
// @ID plugin.version.restore
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param version_id path string true "Version ID belonging to this plugin"
// @Param body body restoreVersionRequest true "New forward version label for the restored draft"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/versions/{version_id}/restore [post]
func (h *Handler) RestoreVersion(c *gin.Context) { restoreVersion(c, h.svc, false) }

// GetVersion godoc
// @Summary Get a historical plugin version (admin)
// @Description Requires enabled version management and current access. Authors and marketplace admins may manage drafts; other readers can access formally released versions only. Original snapshots are immutable. Restoration creates a new draft and requires publication.
// @Tags admin_plugin
// @ID admin_plugin.version.get
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param version_id path string true "Version ID belonging to this plugin"
// @Success 200 {object} apiresponse.Data[historicalVersionResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id}/versions/{version_id} [get]
func (h *AdminHandler) GetVersion(c *gin.Context) { getVersion(c, h.svc, true) }

// DownloadVersionFile godoc
// @Summary Download a historical file (admin)
// @Description Requires enabled version management and current access. Authors and marketplace admins may manage drafts; other readers can access formally released versions only. Original snapshots are immutable. Restoration creates a new draft and requires publication.
// @Tags admin_plugin
// @ID admin_plugin.version.file.download
// @Accept json
// @Produce octet-stream
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param version_id path string true "Version ID belonging to this plugin"
// @Param path query string true "Relative file path in this historical version"
// @Success 200 {file} binary
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id}/versions/{version_id}/file [get]
func (h *AdminHandler) DownloadVersionFile(c *gin.Context) { downloadVersionFile(c, h.svc, true) }

// RestoreVersion godoc
// @Summary Restore a historical version to a new draft (admin)
// @Description Requires enabled version management and current access. Authors and marketplace admins may manage drafts; other readers can access formally released versions only. Original snapshots are immutable. Restoration creates a new draft and requires publication.
// @Tags admin_plugin
// @ID admin_plugin.version.restore
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param version_id path string true "Version ID belonging to this plugin"
// @Param body body restoreVersionRequest true "New forward version label for the restored draft"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id}/versions/{version_id}/restore [post]
func (h *AdminHandler) RestoreVersion(c *gin.Context) { restoreVersion(c, h.svc, true) }

// GetDraft godoc
// @Summary Get the current editable draft
// @Description Requires enabled version management. Draft access is owner-only; admin routes require the marketplace admin gate.
// @Tags plugin
// @ID plugin.draft.get
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/draft [get]
func (h *Handler) GetDraft(c *gin.Context) {
	who, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	v, ok := versionService(c, h.svc)
	if !ok {
		return
	}
	result, err := v.CurrentDetail(c.Request.Context(), who, c.Param("plugin_id"))
	if err != nil {
		writeServiceError(c, err, "plugin.draft.get")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	apiresponse.OK(c, detailDTO(result))
}

// ListVersions godoc
// @Summary List historical plugin versions (admin)
// @Description Requires enabled version management. Draft access is owner-only; admin routes require the marketplace admin gate.
// @Tags admin_plugin
// @ID admin_plugin.version.list
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param page query int false "Page number, default 1"
// @Param page_size query int false "Page size, default 20, max 100"
// @Success 200 {object} apiresponse.OffsetList[versionResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id}/versions [get]
func (h *AdminHandler) ListVersions(c *gin.Context) {
	who, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	v, ok := versionService(c, h.svc)
	if !ok {
		return
	}
	page, size, ok := pagination(c)
	if !ok {
		validation(c, "pagination")
		return
	}
	items, total, err := v.HistoricalVersions(c.Request.Context(), who, c.Param("plugin_id"), size, (page-1)*size, true)
	if err != nil {
		writeServiceError(c, err, "admin_plugin.version.list")
		return
	}
	out := make([]versionResponse, len(items))
	for i, item := range items {
		out[i] = versionDTO(item)
	}
	apiresponse.Offset(c, out, int(total), page, size)
}

// PublishVersion godoc
// @Summary Publish the current saved version (admin)
// @Description Requires enabled version management. Draft access is owner-only; admin routes require the marketplace admin gate.
// @Tags admin_plugin
// @ID admin_plugin.version.publish
// @Produce json
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id}/publish [post]
func (h *AdminHandler) PublishVersion(c *gin.Context) {
	who, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	v, ok := versionService(c, h.svc)
	if !ok {
		return
	}
	result, err := v.AdminPublishVersion(c.Request.Context(), who, c.Param("plugin_id"))
	if err != nil {
		writeServiceError(c, err, "admin_plugin.version.publish")
		return
	}
	apiresponse.OK(c, detailDTO(result))
}
