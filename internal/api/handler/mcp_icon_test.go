// Unit coverage for the two probe-related pure helpers used by the system-MCP
// wizard's "试连 / 获取工具列表" button (see FormModal.tsx#handleProbe).
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	"github.com/gin-gonic/gin"
)

// stubIconSvc lets us drive InitMcpIconUpload's error branches without
// spinning up the real parse.Service (which needs a *sql.DB behind it).
type stubIconSvc struct {
	call int
	res  *parse.IconUploadResult
	err  error
}

func (s *stubIconSvc) InitMcpIconUpload(_ context.Context, name string, size int64) (*parse.IconUploadResult, error) {
	s.call++
	if s.err != nil {
		return nil, s.err
	}
	if s.res != nil {
		return s.res, nil
	}
	return &parse.IconUploadResult{
		ObjectKey:    "mcp-icons/xxx/" + name,
		PresignedURL: "https://put.example/" + name,
		DownloadURL:  "https://cdn.example/" + name,
		Method:       "PUT",
		ExpiresIn:    3600,
		Headers:      map[string]string{"Content-Type": "image/png"},
	}, nil
}

func serveMcpIcon(h *McpIcon, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	h.Init(c)
	return rec
}

func TestMcpIcon_Init_HappyPath(t *testing.T) {
	svc := &stubIconSvc{}
	h := NewMcpIcon(svc)

	body := strings.NewReader(`{"file_name":"logo.png","file_size":4096}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/mcps/upload/icon", body)
	req.Header.Set("Content-Type", "application/json")
	rec := serveMcpIcon(h, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data parse.IconUploadResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	out := envelope.Data
	if out.PresignedURL == "" || out.DownloadURL == "" || out.Method != "PUT" {
		t.Fatalf("unexpected payload: %+v", out)
	}
}

func TestMcpIcon_Init_RejectsEmptyFilename(t *testing.T) {
	svc := &stubIconSvc{}
	h := NewMcpIcon(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/upload/icon",
		strings.NewReader(`{"file_size":1024}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serveMcpIcon(h, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.call != 0 {
		t.Fatalf("service should not be called when validation fails")
	}
}

func TestMcpIcon_Init_RejectsZeroSize(t *testing.T) {
	svc := &stubIconSvc{}
	h := NewMcpIcon(svc)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"file_name":"x.png","file_size":0}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serveMcpIcon(h, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestMcpIcon_Init_FileTooLargeMapsTo400(t *testing.T) {
	svc := &stubIconSvc{err: parse.ErrFileTooLarge}
	h := NewMcpIcon(svc)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"file_name":"big.png","file_size":9999999}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serveMcpIcon(h, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "exceeds 2 MiB") {
		t.Fatalf("expected human message about size limit, got %s", rec.Body.String())
	}
}

func TestMcpIcon_Init_ServiceErrorPropagates(t *testing.T) {
	svc := &stubIconSvc{err: fmt.Errorf("must be an image")}
	h := NewMcpIcon(svc)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"file_name":"x.txt","file_size":100}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serveMcpIcon(h, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "must be an image") {
		t.Fatalf("wire response must not expose service errors, got %s", rec.Body.String())
	}
}
