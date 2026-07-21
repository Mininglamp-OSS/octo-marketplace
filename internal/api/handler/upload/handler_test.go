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
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
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

func TestBotPublishUnavailableUsesUpstreamErrorCode(t *testing.T) {
	r := gin.New()
	auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "owner-1", Name: "Owner"}, "space-1")
	v1 := r.Group("/api/v1")
	v1.Use(auth.Handler())

	h := New(nil, nil, nil)
	h.Register(v1)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/bot/skills/publish", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != errcode.UpstreamUnavailable {
		t.Fatalf("code = %q, want %q", body.Error.Code, errcode.UpstreamUnavailable)
	}
}

func TestBotPublishInvalidVisibilityReturnsBadRequest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	desc := "desc"
	readme := "# Bot Skill"
	parseRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{
			"id", "upload_id", "file_name", "file_size", "file_url", "status",
			"error_code", "error_message",
			"result_name", "result_description", "result_version", "result_tags", "result_readme",
			"result_id", "result_forked_from", "result_metadata",
			"file_sha256", "attempts", "owner_id", "space_id", "skill_id", "created_at", "updated_at",
		}).AddRow(
			"task-1", "upload-1", "skill.zip", int64(100), "skill-uploads/upload-1/skill.zip", "success",
			"", "",
			"Bot Skill", &desc, "1.0.0", []byte(`[]`), &readme,
			"", "", nil,
			"sha", 0, "owner-1", "space-1", "", now, now,
		)
	}
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("upload-1").
		WillReturnRows(parseRows())
	mock.ExpectQuery("SELECT id, upload_id, file_name, file_size, file_url, status,").
		WithArgs("task-1").
		WillReturnRows(parseRows())
	mock.ExpectQuery("SELECT .+ FROM parse_tasks WHERE id").
		WithArgs("task-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "upload_id", "file_name", "file_size", "file_url", "file_sha256",
			"status", "result_name", "result_description", "result_version",
			"result_tags", "result_readme", "result_id", "result_forked_from", "result_metadata", "attempts",
			"owner_id", "space_id", "skill_id",
		}).AddRow(
			"task-1", "upload-1", "skill.zip", int64(100), "skill-uploads/upload-1/skill.zip", "sha",
			"success", "Bot Skill", &desc, "1.0.0",
			[]byte(`[]`), &readme, "", "", nil, 0,
			"owner-1", "space-1", "",
		))

	parseRepo := parse.NewRepo(db)
	parseSvc := parse.NewService(nil, parseRepo, nil, func() string { return "id" }, 20, parse.ServiceConfig{})
	skillSvc := skillsvc.New(skillrepo.New(db), categoryrepo.New(db), nil, func() string { return "skill-id" })
	h := New(parseSvc, skillSvc, nil)
	h.SetDevBotMode(true)

	r := gin.New()
	auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "owner-1", Name: "Owner"}, "space-1")
	v1 := r.Group("/api/v1")
	v1.Use(auth.Handler())
	h.Register(v1)

	body, _ := json.Marshal(map[string]any{
		"skill_upload_id": "upload-1",
		"visibility":      "system",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bot/skills/publish", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer bf_local")
	req.Header.Set("X-Dev-Bot-Uid", "bot-1")
	req.Header.Set("X-Dev-Bot-Name", "Bot")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "visibility must be one of") {
		t.Fatalf("body = %s, want visibility validation message", w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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
