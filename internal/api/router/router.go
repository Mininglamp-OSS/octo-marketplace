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
	experthandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/expert"
	metricshandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/metrics"
	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	skillhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/skill"
	uploadhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/upload"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/auth"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/notify"
	metricsredis "github.com/Mininglamp-OSS/octo-marketplace/internal/redis"
	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
	expertrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/expert"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
	categorysvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/category"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
	metricssvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/metrics"
	parsesvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
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
	ParseTimeout      time.Duration
	StaleTimeout      time.Duration
	MaxAttempts       int
	WorkerPoolSize    int
	BotPublishTimeout time.Duration
	DevBotMode        bool
}

// RedisConfig holds configuration for the Redis connection used by metrics.
type RedisConfig struct {
	Client *goredis.Client
}

// ReviewConfig wires the Space review workflow's octo-server integration.
//
// Everything here is optional and independent of the review workflow itself:
// with an empty config the six tenant review endpoints work exactly as they do
// with it, they simply dispatch no IM approval card, and the card-action
// callback stays permanently closed (no secret => every request 401s). Web-path
// authorization reads the caller's Space role from the verified identity and
// never consults this.
type ReviewConfig struct {
	// OctoAPIURL is the octo-server base URL used for the internal role lookup
	// and notify dispatch. Empty disables both.
	OctoAPIURL string
	// InternalToken authenticates against octo-server /v1/internal/*
	// (OCTO_MARKETPLACE_INTERNAL_TOKEN).
	InternalToken string
	// CardActionSecret is the HMAC key octo-server signs card-action callbacks
	// with (OCTO_MARKETPLACE_CARD_ACTION_SECRET). Empty leaves the callback
	// endpoint permanently closed rather than open.
	CardActionSecret  string
	NotifyTimeout     time.Duration
	CardActionMaxSkew time.Duration
}

func Public(database Pinger, authenticator *marketmiddleware.Authenticator, adminAuth *marketmiddleware.AdminAuthenticator, storageCfg StorageConfig, mcp *handler.MCP, adminMCP *handler.AdminMCP, parseCfg ParseConfig, fleetClient expertsvc.FleetProvisioner, reviewCfg ReviewConfig, redisCfg ...RedisConfig) *gin.Engine {
	var rc RedisConfig
	if len(redisCfg) > 0 {
		rc = redisCfg[0]
	}
	return publicWithOptions(database, authenticator, adminAuth, storageCfg, mcp, adminMCP, authenticator.AuthEnabled(), parseCfg, fleetClient, rc, reviewCfg)
}

func publicWithOptions(database Pinger, authenticator *marketmiddleware.Authenticator, adminAuth *marketmiddleware.AdminAuthenticator, storageCfg StorageConfig, mcp *handler.MCP, adminMCP *handler.AdminMCP, authEnabled bool, parseCfg ParseConfig, fleetClient expertsvc.FleetProvisioner, redisCfg RedisConfig, reviewCfg ReviewConfig) *gin.Engine {
	r := gin.New()
	r.Use(logging.RequestID(), logging.AccessLog(), logging.Recovery(), corsMiddleware(storageCfg.CORSAllowedOrigins))
	logReviewConfigWarnings(reviewCfg)

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
	handler.NewSession().Register(v1)

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
		pluginRepo := pluginrepo.New(db)

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

		pluginSvc := pluginsvc.New(pluginRepo, store)
		pluginSvc.SetArtifactLimits(int64(storageCfg.MaxMB) << 20)
		// Space review IM integration. A disabled notifier keeps the review
		// endpoints fully functional and simply sends no approval card. Partial
		// configurations (URL-without-token, token-without-card-secret) are
		// warned about at engine construction by logReviewConfigWarnings; a card
		// secret without an internal token is rejected by config.ValidateAPI at
		// boot because it would leave the callback mounted but unable to
		// authorize anyone.
		notifier := notify.New(reviewCfg.OctoAPIURL, reviewCfg.InternalToken, reviewCfg.NotifyTimeout)
		if notifier.Enabled() {
			pluginSvc.WithNotify(notifier, notify.BestEffort)
		}
		pluginCats := pluginsvc.NewCategories(pluginRepo, generateID)
		pluginhandler.New(pluginSvc, pluginCats).Register(v1)
		// The octo-server card-action callback is mounted on the ROOT engine, not
		// on v1: it carries an HMAC signature instead of a user token, so it must
		// not pass through the tenant Authenticator, and its path is covered by
		// that signature (it cannot be moved under /api/v1 unilaterally).
		pluginhandler.NewCardAction(pluginSvc, reviewCfg.CardActionSecret, reviewCfg.CardActionMaxSkew).Register(r)
		// Admin surface: /api/v1/admin/plugins(+/plugin_categories), cross-Space
		// management of system connectors and global skills/experts, gated by the
		// admin authenticator.
		pluginhandler.NewAdmin(pluginSvc, pluginCats).RegisterAdmin(r, adminAuth)

		catSvc := categorysvc.New(catRepo, skRepo)
		skSvc := skillsvc.New(skRepo, catRepo, store, generateID)
		skSvc.SetMaxArchiveBytes(int64(storageCfg.MaxMB) << 20)

		catH := categoryhandler.New(catSvc)
		catH.Register(v1)
		skHandler := skillhandler.New(skSvc)
		skHandler.Register(v1)
		skHandler.RegisterAdmin(r, adminAuth)

		// Metrics service: Redis-buffered counters (no-op without Redis). Built
		// before the expert service so install tracking can be wired into it.
		var mSvc *metricssvc.Service
		if redisCfg.Client != nil {
			metricsRedisClient := metricsredis.NewClient(redisCfg.Client)
			mSvc = metricssvc.New(metricsRedisClient)
		}
		if mSvc == nil {
			mSvc = metricssvc.New(nil)
		}

		// Expert Marketplace (docs/api/expert-v1.md): experts + squads + tags +
		// the dedicated expert_categories taxonomy. Reuses the shared id generator.
		// A fleet client (when configured) powers POST /experts/{id}/install.
		expertSvc := expertsvc.New(expertrepo.New(db), store, generateID).WithMetrics(mSvc)
		if fleetClient != nil {
			expertSvc = expertSvc.WithFleet(fleetClient)
		}
		// The unified plugin install reuses the expert provisioning flow, accrues
		// counters under resource_type "plugin", and imports skill uploads
		// through the legacy parse pipeline.
		pluginSvc.WithProvisioner(expertSvc).WithMetrics(mSvc).WithParseTasks(skRepo)
		expertHandler := experthandler.New(expertSvc)
		expertHandler.Register(v1)

		metricssvc.RegisterResolver("skill", metricssvc.NewSkillResolver(skSvc))
		metricssvc.RegisterResolver("expert", metricssvc.NewExpertResolver(expertSvc))
		metricssvc.RegisterResolver("squad", metricssvc.NewSquadResolver(expertSvc))
		metricssvc.RegisterResolver("plugin", metricssvc.NewPluginResolver(pluginSvc))
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
		uploadH.SetBotPublishTimeout(parseCfg.BotPublishTimeout)
		uploadH.SetDevBotMode(parseCfg.DevBotMode)
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
	connectors := r.Group("/api/v1/connectors", authenticator.Handler())
	connectors.POST("/_probe", mcp.ConnectorProbe)
	rg.GET("/:mcp_id", mcp.Get)
	rg.PATCH("/:mcp_id", mcp.Patch)
	rg.DELETE("/:mcp_id", mcp.Delete)
	rg.POST("/:mcp_id/icon", mcp.UploadIcon)
	v1 := r.Group("/api/v1", authenticator.Handler())
	v1.GET("/mcp_categories", mcp.ListCategories)
	v1.GET("/mcp_tags", mcp.ListTags)
}

// registerAdminMCP mounts the admin surface for system MCPs at /api/v1/admin/mcps.
// mcpIcon may be nil when the storage layer is not wired (Public called without
// a real *sql.DB) — in that case the icon upload endpoint is skipped.
func registerAdminMCP(r *gin.Engine, adminAuth *marketmiddleware.AdminAuthenticator, admin *handler.AdminMCP, mcpIcon *handler.McpIcon) {
	if admin == nil {
		return
	}
	rg := r.Group("/api/v1/admin/mcps", adminAuth.Handler(marketmiddleware.RoleMarketAdmin))
	rg.POST("/_probe", admin.Probe)
	rg.POST("/probe", deprecatedRoute("/api/v1/admin/mcps/_probe"), admin.Probe)
	if mcpIcon != nil {
		adminRoot := r.Group("/api/v1/admin", adminAuth.Handler(marketmiddleware.RoleMarketAdmin))
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

// logReviewConfigWarnings emits a startup warning for each partial
// plugin-review configuration. These are non-fatal: the web review path stays
// fully usable, but one half of the IM integration is silently disabled, which
// an operator would otherwise only discover when humans complain. Each message
// names the variables involved and never echoes any secret value.
//
// A card secret without an internal token is rejected at boot by
// config.ValidateAPI, so it does not reach here.
func logReviewConfigWarnings(cfg ReviewConfig) {
	hasURL := cfg.OctoAPIURL != ""
	hasToken := cfg.InternalToken != ""
	hasSecret := cfg.CardActionSecret != ""
	if hasSecret && !hasURL {
		logging.Warn("review_callback_misconfigured",
			zap.String("operation", "plugin_review.config"),
			zap.String("reason", "OCTO_MARKETPLACE_CARD_ACTION_SECRET set without OCTO_API_URL; the card callback will verify signatures but operator role lookups always fail (no notifier endpoint), so every real admin click returns 503 forever"))
	}
	if hasURL && !hasToken {
		logging.Warn("review_notify_disabled",
			zap.String("operation", "plugin_review.config"),
			zap.String("reason", "OCTO_API_URL set without OCTO_MARKETPLACE_INTERNAL_TOKEN; approval cards will not be dispatched"))
	}
	if hasToken && !hasSecret {
		logging.Warn("review_callback_disabled",
			zap.String("operation", "plugin_review.config"),
			zap.String("reason", "OCTO_MARKETPLACE_INTERNAL_TOKEN set without OCTO_MARKETPLACE_CARD_ACTION_SECRET; approval cards will be sent but every admin click will be rejected (401)"))
	}
	if hasSecret && !hasToken {
		// This is a boot-failure (see Config.validateOctoSecrets); unreachable
		// at runtime but kept as a belt-and-braces warning if anyone constructs
		// ReviewConfig directly outside ValidateAPI.
		logging.Warn("review_callback_misconfigured",
			zap.String("operation", "plugin_review.config"),
			zap.String("reason", "OCTO_MARKETPLACE_CARD_ACTION_SECRET set without OCTO_MARKETPLACE_INTERNAL_TOKEN; card signatures verify but operator role lookups always 503"))
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
	}, nil, ReviewConfig{}, RedisConfig{})
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
	}, nil, RedisConfig{}, ReviewConfig{})
}
