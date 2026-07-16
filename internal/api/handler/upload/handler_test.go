package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func testRouter(localStorage *storage.LocalStorage) (*gin.Engine, *Handler) {
	r := gin.New()
	auth := middleware.NewAuthenticator(false, nil,
		model.Identity{UID: "test-user", Name: "Tester"}, "test-space")
	v1 := r.Group("/api/v1")
	v1.Use(auth.Handler())

	h := New(nil, nil, localStorage) // no services for proxy tests
	h.RegisterLocalProxy(r)
	return r, h
}

func TestLocalUploadProxy(t *testing.T) {
	tmpDir := t.TempDir()
	ls := storage.NewLocal(tmpDir, "http://localhost:8092")
	r, _ := testRouter(ls)

	body := "zip file content"
	req := httptest.NewRequest(http.MethodPut, "/api/v1/_storage/upload/skills/abc/test.zip", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Verify file was written
	rc, err := ls.GetObject(context.Background(), "skills/abc/test.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != body {
		t.Errorf("stored content = %q, want %q", got, body)
	}
}

func TestLocalDownloadProxy(t *testing.T) {
	tmpDir := t.TempDir()
	ls := storage.NewLocal(tmpDir, "http://localhost:8092")
	r, _ := testRouter(ls)

	// Write a file first
	_ = ls.WriteObject("skills/xyz/dl.zip", strings.NewReader("download content"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/_storage/download/skills/xyz/dl.zip", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.String() != "download content" {
		t.Errorf("body = %q, want %q", w.Body.String(), "download content")
	}
}

func TestLocalDownloadProxy_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	ls := storage.NewLocal(tmpDir, "http://localhost:8092")
	r, _ := testRouter(ls)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/_storage/download/skills/missing/file.zip", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestInitUpload_InvalidBody(t *testing.T) {
	// This tests that the handler returns 400 for an empty body.
	// We need a full wiring for this, so we use a minimal approach.
	r := gin.New()
	auth := middleware.NewAuthenticator(false, nil,
		model.Identity{UID: "test-user", Name: "Tester"}, "test-space")
	v1 := r.Group("/api/v1")
	v1.Use(auth.Handler())

	// No parseSvc means we can only test input validation at the handler layer
	tmpDir := t.TempDir()
	ls := storage.NewLocal(tmpDir, "http://localhost:8092")
	h := New(nil, nil, ls)
	v1.POST("/skill/upload/init", h.InitUpload)

	body, _ := json.Marshal(map[string]any{"file_name": "", "file_size": 0})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/skill/upload/init", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}
