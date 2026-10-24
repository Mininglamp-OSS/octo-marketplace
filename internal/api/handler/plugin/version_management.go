package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path"
	"strings"
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
	return historicalVersionResponse{Version: versionDTO(v), ManifestJSON: normalizedObjectRaw(v.Manifest), PluginJSON: publicVersionPackage(v.Package)}
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
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/versions/{version_id} [get]
func (h *Handler) GetVersion(c *gin.Context) { getVersion(c, h.svc, false) }

// DownloadVersionFile godoc
// @Summary Download an original historical file
// @Description Requires enabled version management and current access. Authors and marketplace admins may manage drafts; other readers can access formally released versions only. Original snapshots are immutable. Restoration creates a new draft and requires publication.
// @Tags plugin
// @ID plugin.version.download
// @Accept json
// @Produce octet-stream
// @Security Bearer
// @Param plugin_id path string true "Plugin ID"
// @Param version_id path string true "Version ID belonging to this plugin"
// @Param path query string true "Relative file path in this historical version"
// @Success 200 {file} binary
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/{plugin_id}/versions/{version_id}/file [get]
func (h *Handler) DownloadVersionFile(c *gin.Context) { downloadVersionFile(c, h.svc, false) }

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
// @Success 200 {object} apiresponse.Data[adminHistoricalVersionResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /admin/plugins/{plugin_id}/versions/{version_id} [get]
func (h *AdminHandler) GetVersion(c *gin.Context) {
	who, ok := adminCaller(c)
	if !ok {
		unauthorized(c)
		return
	}
	v, ok := versionService(c, h.svc)
	if !ok {
		return
	}
	result, err := v.ManagedVersion(c.Request.Context(), who, c.Param("plugin_id"), c.Param("version_id"), true)
	if err != nil {
		writeServiceError(c, err, "admin_plugin.version.get")
		return
	}
	if result.PluginType != model.PluginTypeSkill && result.PluginType != model.PluginTypeExpert && result.PluginType != model.PluginTypeExpertTeam {
		writeServiceError(c, pluginsvc.ErrInvalidRequest, "admin_plugin.version.get")
		return
	}
	var pkg struct {
		Attachments []struct {
			Path string `json:"path"`
			MIME string `json:"mime_type"`
		} `json:"attachments"`
	}
	if json.Unmarshal(result.Package, &pkg) != nil {
		writeServiceError(c, pluginsvc.ErrIntegrity, "admin_plugin.version.get")
		return
	}
	files := []adminVersionFileResponse{}
	for _, a := range pkg.Attachments {
		if a.Path == "skill/ref.json" {
			continue
		}
		files = append(files, adminVersionFileResponse{Path: a.Path, MIMEType: a.MIME, IsText: strings.HasPrefix(a.MIME, "text/") || a.MIME == "application/json"})
	}
	rels := versionRelationSlice(result.PluginID, result.ID, result.Relations)
	canonical, err := json.Marshal(rels)
	if err != nil {
		writeServiceError(c, pluginsvc.ErrIntegrity, "admin_plugin.version.get")
		return
	}
	digest := sha256.Sum256(canonical)
	label := ""
	if result.DeclaredVersion != nil {
		label = *result.DeclaredVersion
	}
	c.Header("Cache-Control", "private, no-store")
	apiresponse.OK(c, adminHistoricalVersionResponse{PluginID: result.PluginID, VersionID: result.ID, RevisionNo: result.Version, VersionLabel: label, CreatedAt: result.CreatedAt, ManifestJSON: normalizedObjectRaw(result.Manifest), Files: files, Relations: rels, RelationDigest: hex.EncodeToString(digest[:])})
}

type adminVersionFileResponse struct {
	Path     string `json:"path"`
	MIMEType string `json:"mime_type"`
	IsText   bool   `json:"is_text"`
}
type adminHistoricalVersionResponse struct {
	PluginID       string                     `json:"plugin_id"`
	VersionID      string                     `json:"version_id"`
	RevisionNo     string                     `json:"revision_no"`
	VersionLabel   string                     `json:"version_label"`
	CreatedAt      time.Time                  `json:"created_at" swaggertype:"string,date-time"`
	ManifestJSON   json.RawMessage            `json:"manifest_json" swaggertype:"object"`
	Files          []adminVersionFileResponse `json:"files"`
	Relations      []map[string]any           `json:"relations"`
	RelationDigest string                     `json:"relation_digest"`
}

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
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
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

// The legacy skill/ref.json attachment is host metadata. Its object locations
// are private even when the historical snapshot is authorized for public read.
func publicVersionPackage(raw json.RawMessage) json.RawMessage {
	var pkg map[string]any
	if json.Unmarshal(raw, &pkg) != nil {
		return json.RawMessage(`{}`)
	}
	if attachments, ok := pkg["attachments"].([]any); ok {
		for _, item := range attachments {
			a, ok := item.(map[string]any)
			if !ok {
				continue
			}
			delete(a, "storage_uri")
			if a["path"] != "skill/ref.json" {
				continue
			}
			str, _ := a["raw_content"].(string)
			var ref map[string]any
			if json.Unmarshal([]byte(str), &ref) != nil {
				delete(a, "raw_content")
				continue
			}
			delete(ref, "object_key")
			delete(ref, "zip_object_key")
			delete(ref, "file_url")
			value, _ := json.Marshal(ref)
			a["raw_content"] = string(value)
		}
	}
	value, err := json.Marshal(pkg)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return value
}
