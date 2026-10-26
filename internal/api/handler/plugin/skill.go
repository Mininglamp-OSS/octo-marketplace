package plugin

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"

	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type importRequest struct {
	ExpectedEditRevision *uint64  `json:"expected_edit_revision,omitempty"`
	ParseTaskID          string   `json:"parse_task_id"`
	PluginID             string   `json:"plugin_id,omitempty"`
	PluginName           string   `json:"plugin_name,omitempty"`
	Name                 string   `json:"name,omitempty"`
	Description          string   `json:"description,omitempty"`
	CategoryID           *string  `json:"category_id,omitempty"`
	Tags                 []string `json:"tags,omitempty"`
	Visibility           string   `json:"visibility,omitempty"`
	Icon                 string   `json:"icon,omitempty"`
	Version              string   `json:"version,omitempty"`
	Changelog            *string  `json:"changelog,omitempty"`
}

type skillMarkdownResponse struct {
	Content string `json:"content"`
	// VersionID is the immutable snapshot actually read (A23). It is omitted on
	// the public A06 path, which does not carry a version identity today.
	VersionID string `json:"version_id,omitempty"`
}

// Import godoc
// @Summary Import skill plugin from a parsed upload
// @Description Turn a completed upload-parse task into a skill Plugin (or update one), storing the rewritten package in managed storage and publishing the given version onto the default scene.
// @Tags plugin
// @ID plugin.import
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body importRequest true "Parse task and document fields"
// @Success 200 {object} apiresponse.Data[detailResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 409 {object} apiresponse.Error "CONFLICT"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/import [post]
func (h *Handler) Import(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	var req importRequest
	if !decode(c, &req) {
		return
	}
	v, err := h.svc.Import(c.Request.Context(), caller, pluginsvc.ImportParams{
		ParseTaskID:          req.ParseTaskID,
		PluginID:             req.PluginID,
		ExpectedEditRevision: req.ExpectedEditRevision,
		PluginName:           req.PluginName,
		Name:                 req.Name,
		Description:          req.Description,
		CategoryID:           req.CategoryID,
		Tags:                 req.Tags,
		Visibility:           model.PluginVisibility(req.Visibility),
		Icon:                 req.Icon,
		Version:              req.Version,
		Changelog:            req.Changelog,
	})
	if err != nil {
		if errors.Is(err, pluginsvc.ErrInvalidParseTask) {
			validation(c, "parse_task_id")
			return
		}
		writeServiceError(c, err, "plugin.import")
		return
	}
	apiresponse.OK(c, detailDTO(v))
}

// SkillMarkdown godoc
// @Summary Read skill plugin SKILL.md
// @Description Return the SKILL.md text of a visible skill Plugin, serving both inlined attachments and legacy backfilled object pointers.
// @Tags plugin
// @ID plugin.skill_md
// @Accept json
// @Produce json
// @Security Bearer
// @Param plugin_id query string true "Plugin ID"
// @Success 200 {object} apiresponse.Data[skillMarkdownResponse]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/skill_md [get]
func (h *Handler) SkillMarkdown(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	content, err := h.svc.SkillMarkdown(c.Request.Context(), caller, c.Query("plugin_id"))
	if err != nil {
		writeServiceError(c, err, "plugin.skill_md")
		return
	}
	apiresponse.OK(c, skillMarkdownResponse{Content: content})
}

// DownloadSkillPackage godoc
// @Summary Download skill plugin package
// @Description Stream the packaged zip of a visible skill Plugin behind authentication (no presigned URL), serving managed and legacy backfilled storage keys.
// @Tags plugin
// @ID plugin.skill_download
// @Accept json
// @Produce application/zip
// @Security Bearer
// @Param plugin_id query string true "Plugin ID"
// @Success 200 {file} binary "Skill package zip"
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /plugins/download [get]
func (h *Handler) DownloadSkillPackage(c *gin.Context) {
	caller, ok := caller(c)
	if !ok {
		unauthorized(c)
		return
	}
	result, err := h.svc.OpenSkillPackage(c.Request.Context(), caller, c.Query("plugin_id"))
	if err != nil {
		writeServiceError(c, err, "plugin.skill_download")
		return
	}
	// Reconstruct/copy the zip into a bounded buffer FIRST: writeSkillZip caps the
	// aggregate size at maxArchiveBytes and fails closed on an integrity mismatch,
	// so a mid-stream ErrTooLarge/ErrIntegrity must surface as a proper error code
	// rather than a truncated archive already committed under a 200 (B3).
	var buf bytes.Buffer
	if err := result.Write(&buf); err != nil {
		writeServiceError(c, err, "plugin.skill_download")
		return
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", contentDisposition(result.FileName))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Length", strconv.Itoa(buf.Len()))
	c.Status(http.StatusOK)
	if _, err := c.Writer.Write(buf.Bytes()); err != nil {
		logging.Error("plugin_skill_package_stream_failed", logging.ErrorField(err))
	}
}
