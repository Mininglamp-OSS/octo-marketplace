package response

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestInternalLogsCauseButKeepsResponseGeneric(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logs bytes.Buffer
	encoder := zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		MessageKey:  "msg",
		LevelKey:    "level",
		EncodeLevel: zapcore.LowercaseLevelEncoder,
	})
	restore := logging.Replace(zap.New(zapcore.NewCore(encoder, zapcore.AddSync(&logs), zapcore.DebugLevel)))
	defer restore()

	r := gin.New()
	r.Use(logging.RequestID())
	r.GET("/boom", func(c *gin.Context) {
		Internal(c, errors.New("Error 1267: secret=plain-token"), "skill.list")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.Header.Set(logging.RequestIDHeader, "req-test-1")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if got := w.Header().Get(logging.RequestIDHeader); got != "req-test-1" {
		t.Fatalf("request id header=%q", got)
	}

	var body map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"]["code"] != "INTERNAL_ERROR" {
		t.Fatalf("body=%s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Error 1267") || strings.Contains(w.Body.String(), "plain-token") {
		t.Fatalf("response leaked internal error: %s", w.Body.String())
	}

	line := logs.String()
	for _, want := range []string{"internal_api_error", "req-test-1", "skill.list", "Error 1267"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log %q does not contain %q", line, want)
		}
	}
	if strings.Contains(line, "plain-token") {
		t.Fatalf("log did not scrub secret value: %q", line)
	}
}
