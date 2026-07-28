package logging

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestScrubMasksSensitiveValues(t *testing.T) {
	input := `Authorization: Bearer abc123 MYSQL_DSN=user:pass@tcp(mysql:3306)/db password=hunter2 OSS_SECRET_KEY=s3cr3t`
	got := Scrub(input)
	for _, forbidden := range []string{"abc123", "pass@tcp", "hunter2", "s3cr3t"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("Scrub(%q) = %q, still contains %q", input, got, forbidden)
		}
	}
	for _, want := range []string{"Authorization: Bearer ***", "password=***", "OSS_SECRET_KEY=***"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Scrub(%q) = %q, missing %q", input, got, want)
		}
	}
}

func TestScrubMasksStructuredAndURLSecrets(t *testing.T) {
	inputs := []string{
		`Authorization: Basic dXNlcjpwYXNz`,
		`{"access_token":"eyJhbGciOi.abc.def"}`,
		`{"api_key":"api-secret","private_key":"private-secret","jwt":"jwt-secret"}`,
		`{"password": "hunter2"}`,
		`redis://default:sup3rs3cret@redis:6379/0`,
		`Set-Cookie: session=abcdef; HttpOnly`,
		`user:pw@tcp(mysql:3306)/db`,
	}
	for _, input := range inputs {
		got := Scrub(input)
		for _, forbidden := range []string{"dXNlcjpwYXNz", "eyJhbGciOi.abc.def", "api-secret", "private-secret", "jwt-secret", "hunter2", "sup3rs3cret", "abcdef", "user:pw", "mysql:3306"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("Scrub(%q) = %q, still contains %q", input, got, forbidden)
			}
		}
	}
}

func TestRequestIDRejectsInvalidClientValues(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, supplied := range []string{strings.Repeat("a", maxRequestIDLen+1), "request/id"} {
		t.Run(supplied[:min(len(supplied), 16)], func(t *testing.T) {
			r := gin.New()
			r.Use(RequestID())
			r.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(RequestIDHeader, supplied)
			r.ServeHTTP(w, req)

			got := w.Header().Get(RequestIDHeader)
			if got == supplied || !validRequestID(got) {
				t.Fatalf("generated request ID %q is invalid or reused supplied value", got)
			}
		})
	}
}

func TestNewLoggerWritesOptionalRotatedFile(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(Options{
		Level:       "debug",
		Format:      "console",
		FileEnabled: true,
		Dir:         dir,
		MaxSizeMB:   1,
		MaxBackups:  1,
		MaxAgeDays:  1,
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l.Info("info message")
	l.Warn("warn message")
	l.Error("error message")
	_ = l.Sync()

	data, err := os.ReadFile(filepath.Join(dir, fileLogName))
	if err != nil {
		t.Fatalf("read %s: %v", fileLogName, err)
	}
	for _, want := range []string{"info message", "warn message", "error message"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("%s does not contain %q: %q", fileLogName, want, data)
		}
	}

	for _, name := range []string{"info.log", "warn.log", "error.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be created in single-file logging mode", name)
		}
	}
}

func TestFileLoggerHonorsConfiguredMinimumLevel(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(Options{
		Level:       "error",
		Format:      "console",
		FileEnabled: true,
		Dir:         dir,
		MaxSizeMB:   1,
		MaxBackups:  1,
		MaxAgeDays:  1,
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l.Warn("warn message")
	l.Error("error message")
	_ = l.Sync()

	data, err := os.ReadFile(filepath.Join(dir, fileLogName))
	if err != nil {
		t.Fatalf("read %s: %v", fileLogName, err)
	}
	if strings.Contains(string(data), "warn message") {
		t.Fatalf("%s contains warning below configured minimum level: %q", fileLogName, data)
	}
	if !strings.Contains(string(data), "error message") {
		t.Fatalf("%s does not contain error at configured minimum level: %q", fileLogName, data)
	}
}

func TestRecoveryLogsRequestIDAndScrubsPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logs bytes.Buffer
	encoder := zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		MessageKey:  "msg",
		LevelKey:    "level",
		EncodeLevel: zapcore.LowercaseLevelEncoder,
	})
	restore := Replace(zap.New(zapcore.NewCore(encoder, zapcore.AddSync(&logs), zapcore.DebugLevel)))
	defer restore()

	r := gin.New()
	r.Use(RequestID(), Recovery())
	r.GET("/panic", func(*gin.Context) {
		panic("boom token=super-secret")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	req.Header.Set(RequestIDHeader, "panic-req-1")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	line := logs.String()
	for _, want := range []string{"panic_recovered", "panic-req-1", "boom"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log %q does not contain %q", line, want)
		}
	}
	if strings.Contains(line, "super-secret") || strings.Contains(w.Body.String(), "super-secret") {
		t.Fatalf("panic secret leaked: log=%q body=%q", line, w.Body.String())
	}
}
