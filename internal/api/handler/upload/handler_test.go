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
	req.RemoteAddr = "127.0.0.1:12345"
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

func TestLocalProxyNotMountedWhenAuthEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	ls := storage.NewLocal(tmpDir, "http://localhost:8092")
	r := gin.New()
	h := New(nil, nil, ls)
	h.RegisterLocalProxy(r, true)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/_storage/upload/skills/abc/test.zip", strings.NewReader("data"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestLocalUploadProxyRejectsOversizedBody(t *testing.T) {
	tmpDir := t.TempDir()
	ls := storage.NewLocal(tmpDir, "http://localhost:8092")
	r := gin.New()
	h := New(nil, nil, ls, 1)
	h.RegisterLocalProxy(r, false)

	body := strings.NewReader(strings.Repeat("x", 1024*1024+1))
	req := httptest.NewRequest(http.MethodPut, "/api/v1/_storage/upload/skills/abc/large.zip", body)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d body=%s", w.Code, http.StatusRequestEntityTooLarge, w.Body.String())
	}
}

func TestLocalDownloadProxy(t *testing.T) {
	tmpDir := t.TempDir()
	ls := storage.NewLocal(tmpDir, "http://localhost:8092")
	r, _ := testRouter(ls)

	// Write a file first
	_ = ls.WriteObject("skills/xyz/dl.zip", strings.NewReader("download content"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/_storage/download/skills/xyz/dl.zip", nil)
	req.RemoteAddr = "127.0.0.1:12345"
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
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestLocalProxyRejectsRemoteClient(t *testing.T) {
	tmpDir := t.TempDir()
	ls := storage.NewLocal(tmpDir, "http://localhost:8092")
	r, _ := testRouter(ls)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/_storage/upload/skills/abc/test.zip", strings.NewReader("data"))
	req.RemoteAddr = "192.0.2.10:12345"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
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

func TestBotIdentityDevFallbackRequiresDevMode(t *testing.T) {
	r := gin.New()
	h := New(nil, nil, nil)
	r.GET("/probe", func(c *gin.Context) {
		identity := model.Identity{UID: "owner-1", Name: "Owner"}
		if _, ok := h.botIdentity(c, identity); ok {
			t.Fatal("bot identity should not be accepted when dev mode is disabled")
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer bf_local")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusNoContent, w.Body.String())
	}
}

func TestBotIdentityDevFallback(t *testing.T) {
	r := gin.New()
	h := New(nil, nil, nil)
	h.SetDevBotMode(true)
	r.GET("/probe", func(c *gin.Context) {
		c.Set("marketplace.space_id", "space-1")
		bot, ok := h.botIdentity(c, model.Identity{UID: "owner-1", Name: "Owner"})
		if !ok {
			t.Fatal("expected dev bot identity")
		}
		if bot.BotUID != "bot-1" || bot.BotName != "Publish Bot" || bot.OwnerUID != "owner-1" || bot.SpaceID != "space-1" {
			t.Fatalf("unexpected bot identity: %+v", bot)
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer bf_local")
	req.Header.Set("X-Dev-Bot-Uid", "bot-1")
	req.Header.Set("X-Dev-Bot-Name", "Publish Bot")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusNoContent, w.Body.String())
	}
}

func TestNormalizePublishTags(t *testing.T) {
	tests := []struct {
		name  string
		input json.RawMessage
		want  string
	}{
		{name: "empty", input: nil, want: ""},
		{name: "json array", input: json.RawMessage(`["bot","publish"]`), want: `["bot","publish"]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizePublishTags(tt.input)
			if err != nil {
				t.Fatalf("normalizePublishTags() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("normalizePublishTags() = %s, want %s", string(got), tt.want)
			}
		})
	}

	if _, err := normalizePublishTags(json.RawMessage(`["ok", 1]`)); err == nil {
		t.Fatal("expected invalid JSON array to fail")
	}
}

func TestUploadIDFromLink(t *testing.T) {
	tests := []struct {
		name string
		link string
		want string
	}{
		{
			name: "local presigned upload URL",
			link: "http://127.0.0.1:8092/api/v1/_storage/upload/skill-uploads/upload-1/skill.zip",
			want: "upload-1",
		},
		{
			name: "oss presigned URL",
			link: "https://bucket.example.com/skill-uploads/upload-2/skill.zip?X-Amz-Signature=abc",
			want: "upload-2",
		},
		{
			name: "object key",
			link: "skill-uploads/upload-3/skill.zip",
			want: "upload-3",
		},
		{
			name: "unrelated",
			link: "https://example.com/file.zip",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uploadIDFromLink(tt.link); got != tt.want {
				t.Fatalf("uploadIDFromLink() = %q, want %q", got, tt.want)
			}
		})
	}
}
