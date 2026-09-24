package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCapabilityInstallationCORSHeaders(t *testing.T) {
	r := gin.New()
	r.Use(corsMiddleware([]string{"https://octo.example.com"}))
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/plugins/expert-1/installations", nil)
	req.Header.Set("Origin", "https://octo.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,x-workspace-id,idempotency-key")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "https://octo.example.com" {
		t.Fatalf("status=%d", rec.Code)
	}
	for _, header := range []string{"X-Workspace-Id", "Idempotency-Key"} {
		if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), header) {
			t.Fatalf("missing request header %s", header)
		}
	}
	for _, header := range []string{"Idempotency-Replayed", "Retry-After"} {
		if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), header) {
			t.Fatalf("missing response header %s", header)
		}
	}
}
