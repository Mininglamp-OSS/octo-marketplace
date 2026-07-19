package router

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler"
	categoryhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/category"
	metricshandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/metrics"
	skillhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/skill"
	uploadhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/upload"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/auth"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	metricsredis "github.com/Mininglamp-OSS/octo-marketplace/internal/redis"
	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
	categorysvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/category"
	metricssvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/metrics"
	parsesvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
)

type Pinger interface {
	PingContext(context.Context) error
}

// StorageConfig holds configuration for the skill archive storage layer.
type StorageConfig struct {
	Driver             string // "local" or "oss"
	LocalDir           string
	BaseURL            string
	MaxMB              int
	OSSEndpoint        string
	OSSBucket          string
	OSSAccessKey       string
	OSSSecretKey       string
	OSSRegion          string
	OSSKeyPrefix       string
	OSSPathStyle       bool
	OSSPublicEndpoint  string
	OSSPublicPathStyle bool
	OSSSigningHost     string
	OSSDownloadSigned  bool
	CORSAllowedOrigins []string
}

// ParseConfig holds parse worker/service configuration passed from the caller.
type ParseConfig struct {
	ParseTimeout   time.Duration
	StaleTimeout   time.Duration
	MaxAttempts    int
	WorkerPoolSize int
}

// RedisConfig holds configuration for the Redis connection used by metrics.
type RedisConfig struct {
	URL string // e.g. "redis://localhost:6379/0"
}

func Public(database Pinger, authenticator *marketmiddleware.Authenticator, adminAuth *marketmiddleware.AdminAuthenticator, storageCfg StorageConfig, mcp *handler.MCP, adminMCP *handler.AdminMCP, parseCfg ParseConfig, redisCfg ...RedisConfig) *gin.Engine {
	var rc RedisConfig
	if len(redisCfg) > 0 {
		rc = redisCfg[0]
	}
	return publicWithOptions(database, authenticator, adminAuth, storageCfg, mcp, adminMCP, authenticator.AuthEnabled(), parseCfg, rc)
}

func publicWithOptions(database Pinger, authenticator *marketmiddleware.Authenticator, adminAuth *marketmiddleware.AdminAuthenticator, storageCfg StorageConfig, mcp *handler.MCP, adminMCP *handler.AdminMCP, authEnabled bool, parseCfg ParseConfig, redisCfg RedisConfig) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery(), corsMiddleware(storageCfg.CORSAllowedOrigins))

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/readyz", func(c *gin.Context) {
		if err := database.PingContext(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	v1 := r.Group("/api/v1")
	v1.Use(authenticator.Handler())
	v1.GET("/session", func(c *gin.Context) {
		identity, ok := marketmiddleware.Identity(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"uid":      identity.UID,
			"name":     identity.Name,
			"space_id": marketmiddleware.SpaceID(c),
		})
	})

	// Wire up skill marketplace handlers when we have a real *sql.DB.
	// adminMcpIcon carries the MCP icon-upload handler across the closure
	// boundary — constructed inside the db block (where pSvc lives), then
	// handed to registerAdminMCP so it can register the admin route on the
	// same http.ServeMux and avoid gin's wildcard-vs-static conflict.
	var adminMcpIcon *handler.McpIcon
	db, ok := database.(*sql.DB)
	if ok {
		catRepo := categoryrepo.New(db)
		skRepo := skillrepo.New(db)

		var store storage.Storage
		var localStorage *storage.LocalStorage
		switch storageCfg.Driver {
		case "local":
			ls := storage.NewLocal(storageCfg.LocalDir, storageCfg.BaseURL)
			store = ls
			localStorage = ls
		case "oss":
			oss, err := storage.NewOSS(storage.OSSConfig{
				Endpoint:        storageCfg.OSSEndpoint,
				Bucket:          storageCfg.OSSBucket,
				AccessKey:       storageCfg.OSSAccessKey,
				SecretKey:       storageCfg.OSSSecretKey,
				Region:          storageCfg.OSSRegion,
				KeyPrefix:       storageCfg.OSSKeyPrefix,
				PathStyle:       storageCfg.OSSPathStyle,
				PublicEndpoint:  storageCfg.OSSPublicEndpoint,
				PublicPathStyle: storageCfg.OSSPublicPathStyle,
				SigningHost:     storageCfg.OSSSigningHost,
				DownloadSigned:  storageCfg.OSSDownloadSigned,
			})
			if err != nil {
				panic("storage driver oss: " + err.Error())
			}
			store = oss
		default:
			panic("unsupported STORAGE_DRIVER: " + storageCfg.Driver)
		}

		catSvc := categorysvc.New(catRepo)
		skSvc := skillsvc.New(skRepo, catRepo, store, generateID)

		catH := categoryhandler.New(catSvc)
		catH.Register(v1)
		catH.RegisterAdmin(r, adminAuth, generateID)
		skHandler := skillhandler.New(skSvc)
		skHandler.Register(v1)
		skHandler.RegisterAdmin(r, adminAuth)

		// Wire up metrics service and handler.
		var mSvc *metricssvc.Service
		if redisCfg.URL != "" {
			opts, err := goredis.ParseURL(redisCfg.URL)
			if err == nil {
				rdb := goredis.NewClient(opts)
				metricsRedisClient := metricsredis.NewClient(rdb)
				mSvc = metricssvc.New(metricsRedisClient)
			}
		}
		if mSvc == nil {
			mSvc = metricssvc.New(nil)
		}
		metricssvc.RegisterResolver("skill", metricssvc.NewSkillResolver(skSvc))
		metricshandler.New(mSvc).Register(v1)

		parseRepo := parsesvc.NewRepo(db)
		worker := parsesvc.NewWorker(store, parseRepo, db, parsesvc.WorkerConfig{
			PoolSize:     parseCfg.WorkerPoolSize,
			ParseTimeout: parseCfg.ParseTimeout,
		})
		pSvc := parsesvc.NewService(store, parseRepo, worker, generateID, storageCfg.MaxMB, parsesvc.ServiceConfig{
			StaleTimeout: parseCfg.StaleTimeout,
			MaxAttempts:  parseCfg.MaxAttempts,
		})

		uploadH := uploadhandler.New(pSvc, skSvc, localStorage, storageCfg.MaxMB)
		uploadH.SetDevBotMode(!authEnabled)
		uploadH.SetMetricsService(mSvc)
		uploadH.Register(v1)
		uploadH.RegisterAdmin(r, adminAuth)
		uploadH.RegisterLocalProxy(r, authEnabled)

		// MCP icon presigned upload — user surface only. `/api/v1/mcp/upload/icon`
		// does not collide with the `/api/v1/mcps/*` wildcard mounted by
		// registerMCP (different prefix: `mcp` vs `mcps`). The admin twin is
		// wired inside registerAdminMCP below, sharing the same handler.
		mcpIconH := handler.NewMcpIcon(pSvc)
		v1.POST("/mcp_icon_uploads", mcpIconH.Init)
		v1.POST("/mcp/upload/icon", deprecatedRoute("/api/v1/mcp_icon_uploads"), mcpIconH.Init)
		adminMcpIcon = mcpIconH
	}

	registerMCP(r, authenticator, mcp)
	registerAdminMCP(r, adminAuth, adminMCP, adminMcpIcon)
	return r
}

// registerMCP mounts the MCP catalog surface (docs/api/mcp-v1.md §4) under
// /api/v1/mcps.
func registerMCP(r *gin.Engine, authenticator *marketmiddleware.Authenticator, mcp *handler.MCP) {
	if mcp == nil {
		return
	}
	rg := r.Group("/api/v1/mcps", authenticator.Handler())
	rg.POST("", mcp.Create)
	rg.GET("", mcp.List)
	rg.GET("/mine", mcp.ListMine)
	rg.POST("/_probe", mcp.Probe)
	rg.POST("/probe", deprecatedRoute("/api/v1/mcps/_probe"), mcp.Probe)
	rg.GET("/:mcp_id", mcp.Get)
	rg.PATCH("/:mcp_id", mcp.Patch)
	rg.DELETE("/:mcp_id", mcp.Delete)
	rg.POST("/:mcp_id/icon", mcp.UploadIcon)
	r.Group("/api/v1", authenticator.Handler()).GET("/mcp_categories", mcp.ListCategories)
}

// registerAdminMCP mounts the admin surface for system MCPs at /api/v1/admin/mcps.
// mcpIcon may be nil when the storage layer is not wired (Public called without
// a real *sql.DB) — in that case the icon upload endpoint is skipped.
func registerAdminMCP(r *gin.Engine, adminAuth *marketmiddleware.AdminAuthenticator, admin *handler.AdminMCP, mcpIcon *handler.McpIcon) {
	if admin == nil {
		return
	}
	rg := r.Group("/api/v1/admin/mcps", adminAuth.Handler())
	rg.POST("", admin.Create)
	rg.POST("/_probe", admin.Probe)
	rg.POST("/probe", deprecatedRoute("/api/v1/admin/mcps/_probe"), admin.Probe)
	rg.GET("", admin.List)
	rg.GET("/:mcp_id", admin.Get)
	rg.PATCH("/:mcp_id", admin.Patch)
	rg.DELETE("/:mcp_id", admin.Delete)
	if mcpIcon != nil {
		adminRoot := r.Group("/api/v1/admin", adminAuth.Handler())
		adminRoot.POST("/mcp_icon_uploads", mcpIcon.Init)
		rg.POST("/upload/icon", deprecatedRoute("/api/v1/admin/mcp_icon_uploads"), mcpIcon.Init)
	}
}

func deprecatedRoute(successor string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Deprecation", "true")
		c.Header("Sunset", "Thu, 01 Oct 2026 00:00:00 GMT")
		c.Header("Link", "<"+successor+">; rel=\"successor-version\"")
		c.Next()
	}
}

func corsMiddleware(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if origin == "" {
			continue
		}
		allowed[origin] = struct{}{}
	}
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type,Authorization,Token,X-Space-Id,X-Request-Id")
		if origin := c.GetHeader("Origin"); origin != "" {
			if _, ok := allowed[origin]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// generateID produces a UUID v4 string.
func generateID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// PublicWithDB is a convenience wrapper for tests that need a *sql.DB-backed
// engine without wiring MCP or admin handlers.
func PublicWithDB(db *sql.DB, authenticator *marketmiddleware.Authenticator, storageCfg StorageConfig) *gin.Engine {
	adminAuth := marketmiddleware.NewAdminAuthenticator(false, nil, model.Identity{})
	return Public(db, authenticator, adminAuth, storageCfg, nil, nil, ParseConfig{
		ParseTimeout:   time.Minute,
		StaleTimeout:   5 * time.Minute,
		MaxAttempts:    2,
		WorkerPoolSize: 10,
	}, RedisConfig{})
}

// PublicWithDBAndAdminAuth is a test helper that mounts the admin surface with
// a caller-supplied resolver so tests can inject fake SuperAdmin identities.
// Use authEnabled=false to short-circuit both public and admin auth chains
// (adminResolver is ignored in that case).
func PublicWithDBAndAdminAuth(db *sql.DB, authenticator *marketmiddleware.Authenticator, storageCfg StorageConfig, authEnabled bool, adminResolver auth.Resolver) *gin.Engine {
	adminAuth := marketmiddleware.NewAdminAuthenticator(authEnabled, adminResolver, model.Identity{})
	return publicWithOptions(db, authenticator, adminAuth, storageCfg, nil, nil, authEnabled, ParseConfig{
		ParseTimeout:   time.Minute,
		StaleTimeout:   5 * time.Minute,
		MaxAttempts:    2,
		WorkerPoolSize: 10,
	}, RedisConfig{})
}
