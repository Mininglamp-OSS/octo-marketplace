package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	RequestIDHeader = "X-Request-Id"

	fileLogName = "marketplace.log"

	requestIDGinKey = "marketplace.request_id"
	identityGinKey  = "marketplace.identity"
	spaceGinKey     = "marketplace.space_id"
)

type requestIDContextKey struct{}

type Options struct {
	Level       string
	Format      string
	AddCaller   bool
	FileEnabled bool
	Dir         string
	MaxSizeMB   int
	MaxBackups  int
	MaxAgeDays  int
}

var (
	mu     sync.RWMutex
	logger = zap.NewNop()
)

func Configure(opts Options) error {
	l, err := NewLogger(opts)
	if err != nil {
		return err
	}
	Replace(l)
	return nil
}

func NewLogger(opts Options) (*zap.Logger, error) {
	level := zapcore.InfoLevel
	if opts.Level != "" {
		if err := level.UnmarshalText([]byte(strings.ToLower(opts.Level))); err != nil {
			return nil, fmt.Errorf("invalid LOG_LEVEL %q: %w", opts.Level, err)
		}
	}
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "console"
	}
	if format != "console" && format != "json" {
		return nil, fmt.Errorf("invalid LOG_FORMAT %q", opts.Format)
	}

	encoderCfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
	var encoder zapcore.Encoder
	if format == "json" {
		encoder = zapcore.NewJSONEncoder(encoderCfg)
	} else {
		encoder = zapcore.NewConsoleEncoder(encoderCfg)
	}

	cores := []zapcore.Core{
		zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), level),
	}
	if opts.FileEnabled {
		if strings.TrimSpace(opts.Dir) == "" {
			return nil, fmt.Errorf("LOG_DIR is required when LOG_FILE_ENABLED=true")
		}
		if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
			return nil, fmt.Errorf("create LOG_DIR: %w", err)
		}
		if opts.MaxSizeMB <= 0 {
			opts.MaxSizeMB = 20
		}
		if opts.MaxBackups <= 0 {
			opts.MaxBackups = 3
		}
		if opts.MaxAgeDays <= 0 {
			opts.MaxAgeDays = 7
		}
		cores = append(cores, fileCore(encoder, opts, level))
	}

	zopts := []zap.Option{}
	if opts.AddCaller {
		zopts = append(zopts, zap.AddCaller())
	}
	return zap.New(zapcore.NewTee(cores...), zopts...), nil
}

func fileCore(encoder zapcore.Encoder, opts Options, level zapcore.Level) zapcore.Core {
	writer := zapcore.AddSync(&lumberjack.Logger{
		Filename:   filepath.Join(opts.Dir, fileLogName),
		MaxSize:    opts.MaxSizeMB,
		MaxBackups: opts.MaxBackups,
		MaxAge:     opts.MaxAgeDays,
	})
	return zapcore.NewCore(encoder.Clone(), writer, level)
}

func Replace(l *zap.Logger) func() {
	if l == nil {
		l = zap.NewNop()
	}
	mu.Lock()
	old := logger
	logger = l
	mu.Unlock()
	return func() { Replace(old) }
}

func Logger() *zap.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return logger
}

func Sync() {
	_ = Logger().Sync()
}

func RedirectStdLog() func() {
	return zap.RedirectStdLog(Logger())
}

func Info(msg string, fields ...zap.Field) {
	Logger().Info(msg, fields...)
}

func Warn(msg string, fields ...zap.Field) {
	Logger().Warn(msg, fields...)
}

func Error(msg string, fields ...zap.Field) {
	Logger().Error(msg, fields...)
}

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader(RequestIDHeader))
		if id == "" {
			id = newRequestID()
		}
		c.Set(requestIDGinKey, id)
		c.Header(RequestIDHeader, id)
		c.Request = c.Request.WithContext(WithRequestID(c.Request.Context(), id))
		c.Next()
	}
}

func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		fields := append(RequestFields(c),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
			zap.String("client_ip", c.ClientIP()),
		)
		switch {
		case c.Writer.Status() >= http.StatusInternalServerError:
			Error("request_completed", fields...)
		case c.Writer.Status() >= http.StatusBadRequest:
			Warn("request_completed", fields...)
		default:
			Info("request_completed", fields...)
		}
	}
}

func Recovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		fields := append(RequestFields(c),
			zap.String("panic", Scrub(fmt.Sprint(recovered))),
			zap.Stack("stack"),
		)
		Error("panic_recovered", fields...)
		if !c.Writer.Written() {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{
					"code":    "INTERNAL_ERROR",
					"message": "internal error",
					"details": gin.H{},
				},
			})
			return
		}
		c.Abort()
	})
}

func RequestFields(c *gin.Context) []zap.Field {
	fields := []zap.Field{
		zap.String("request_id", RequestIDFromGin(c)),
		zap.String("method", ""),
		zap.String("path", ""),
		zap.String("route", ""),
	}
	if c == nil {
		return fields
	}
	if c.Request != nil {
		fields[1] = zap.String("method", c.Request.Method)
		if c.Request.URL != nil {
			fields[2] = zap.String("path", Scrub(c.Request.URL.Path))
		}
	}
	fields[3] = zap.String("route", c.FullPath())
	if identity, ok := c.Get(identityGinKey); ok {
		if v, ok := identity.(model.Identity); ok {
			fields = append(fields, zap.String("uid", v.UID))
		}
	}
	if space, ok := c.Get(spaceGinKey); ok {
		if v, ok := space.(string); ok {
			fields = append(fields, zap.String("space_id", v))
		}
	}
	return fields
}

func ErrorField(err error) zap.Field {
	if err == nil {
		return zap.Skip()
	}
	return zap.String("error", Scrub(err.Error()))
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

func RequestIDFromGin(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if value, ok := c.Get(requestIDGinKey); ok {
		if id, ok := value.(string); ok {
			return id
		}
	}
	return RequestIDFromContext(c.Request.Context())
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

var scrubbers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)\b(Token|Authorization|X-Admin-Token)\s*[:=]\s*[^,\s]+`),
	regexp.MustCompile(`(?i)\b[A-Za-z0-9_]*(password|passwd|secret|token|credential|access[_-]?key|secret[_-]?key|dsn)[A-Za-z0-9_]*\s*[:=]\s*[^,\s&]+`),
	regexp.MustCompile(`(?i)[^:\s/@]+:[^@\s]+@tcp\([^)]+\)`),
}

func Scrub(value string) string {
	out := value
	for _, re := range scrubbers {
		out = re.ReplaceAllStringFunc(out, func(match string) string {
			if strings.Contains(match, "Bearer ") || strings.Contains(match, "bearer ") {
				return "Bearer ***"
			}
			if idx := strings.IndexAny(match, ":="); idx >= 0 {
				return match[:idx+1] + "***"
			}
			return "***@tcp(***)"
		})
	}
	return out
}
