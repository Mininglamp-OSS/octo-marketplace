package upload

import (
	"errors"
	"io"
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/gin-gonic/gin"
)

// Local storage proxy endpoints are development-only transport plumbing and
// intentionally excluded from the public Marketplace OpenAPI contract.
func (h *Handler) localUploadProxy(c *gin.Context) {
	key := localObjectKey(c.Param("key"))
	maxBytes := int64(h.maxUploadMB) * 1024 * 1024
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	if err := h.localStorage.WriteObject(key, c.Request.Body); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apiresponse.Fail(c, http.StatusRequestEntityTooLarge, errcode.FileTooLarge, "file exceeds upload size limit", map[string]any{"max_bytes": maxBytes}, "")
			return
		}
		apiresponse.Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
		return
	}
	c.Status(http.StatusOK)
}

func (h *Handler) localDownloadProxy(c *gin.Context) {
	key := localObjectKey(c.Param("key"))
	rc, err := h.localStorage.GetObject(c.Request.Context(), key)
	if err != nil {
		apiresponse.Fail(c, http.StatusNotFound, errcode.NotFound, "file not found", nil, "")
		return
	}
	defer rc.Close()
	c.Header("Content-Type", "application/octet-stream")
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, rc)
}

func localObjectKey(key string) string {
	if len(key) > 0 && key[0] == '/' {
		return key[1:]
	}
	return key
}
