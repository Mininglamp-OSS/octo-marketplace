package handler

import (
	"context"
	"errors"
	"net/http"

	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	"github.com/gin-gonic/gin"
)

type McpIconUploadService interface {
	InitMcpIconUpload(context.Context, string, int64) (*parse.IconUploadResult, error)
}

type McpIcon struct{ svc McpIconUploadService }

func NewMcpIcon(svc McpIconUploadService) *McpIcon { return &McpIcon{svc: svc} }

type MCPIconUploadRequest struct {
	FileName    string `json:"file_name" binding:"required"`
	FileSize    int64  `json:"file_size" binding:"required,gt=0"`
	ContentType string `json:"content_type,omitempty"`
}

// Init godoc
// @Summary Initialize MCP icon upload
// @Description Create a presigned upload target and persistent URL for an MCP icon.
// @Tags mcp
// @ID mcp_icon_upload.create
// @Accept json
// @Produce json
// @Security Bearer
// @Param body body MCPIconUploadRequest true "Icon metadata"
// @Success 200 {object} apiresponse.Data[parse.IconUploadResult]
// @Failure 400 {object} apiresponse.Error "VALIDATION_ERROR"
// @Failure 401 {object} apiresponse.Error "AUTH_REQUIRED"
// @Failure 403 {object} apiresponse.Error "FORBIDDEN"
// @Failure 404 {object} apiresponse.Error "NOT_FOUND"
// @Failure 413 {object} apiresponse.Error "PAYLOAD_TOO_LARGE"
// @Failure 500 {object} apiresponse.Error "INTERNAL_ERROR"
// @Router /mcp_icon_uploads [post]
func (h *McpIcon) Init(c *gin.Context) {
	var req MCPIconUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, apierr.CodeInvalidRequest, "file_name and positive file_size are required", nil, "")
		return
	}
	result, err := h.svc.InitMcpIconUpload(c.Request.Context(), req.FileName, req.FileSize)
	if errors.Is(err, parse.ErrFileTooLarge) {
		apiresponse.Fail(c, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "icon exceeds 2 MiB limit", map[string]any{"max_bytes": 2 << 20}, "")
		return
	}
	if err != nil {
		apiresponse.Fail(c, http.StatusBadRequest, apierr.CodeInvalidRequest, "invalid icon upload request", nil, "")
		return
	}
	apiresponse.OK(c, result)
}
