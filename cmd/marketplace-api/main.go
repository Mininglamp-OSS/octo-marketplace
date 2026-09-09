package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/router"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/auth"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/blob"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/config"
	marketdb "github.com/Mininglamp-OSS/octo-marketplace/internal/db"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/repository"
	metricsrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/metrics"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
	metricssvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/metrics"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// @title Octo Marketplace API
// @version 1.0.0
// @description Skill and MCP marketplace API for OCTO.
// @description
// @description A 409 CONFLICT carries `error.details.conflict_reason`, which is what a
// @description client should branch on — the status code alone does not distinguish these:
// @description `review_pending` (a request is already open on this plugin; cancel it first),
// @description `listed_requires_review` (the plugin is listed to the organization, so the
// @description change must go through review or an admin must delist it first),
// @description `label_taken` (the submitted version label is already published; pick another
// @description and resubmit), `already_published` / `not_published` (the listing transition
// @description does not apply in the plugin's current state), `deadlock` (transient lock
// @description contention; also sets `retryable: true`, so retry the request unchanged), and
// @description `state` (a generic raced state change; refresh and retry).
// @contact.name OCTO API Team
// @contact.url https://github.com/Mininglamp-OSS/octo-marketplace
// @BasePath /v1
// @tag.name skill
// @tag.description Skill catalog and releases
// @tag.name skill_upload
// @tag.description Skill artifact ingestion and parsing
// @tag.name skill_category
// @tag.description Skill catalog categories
// @tag.name admin_skill
// @tag.description Administrative Skill catalog
// @tag.name mcp
// @tag.description MCP server catalog
// @tag.name plugin
// @tag.description Unified Plugin catalog, versions, audit history, categories, and Connector probes
// @tag.name admin_plugin
// @tag.description Administrative unified Plugin catalog — cross-Space system connectors, global skills/experts, and categories
// @tag.name admin_mcp
// @tag.description Administrative MCP catalog
// @tag.name session
// @tag.description Current authenticated user context
// @tag.name metrics
// @tag.description Marketplace interaction metrics
// @tag.name expert
// @tag.description Expert catalog — single experts (专家) and tag suggestions
// @tag.name expert_squad
// @tag.description Expert squad catalog — expert teams (专家团)
// @securityDefinitions.bearerauth Bearer

// @securityDefinitions.apikey AdminToken
// @in header
// @name X-Admin-Token

func main() {
	gin.SetMode(gin.ReleaseMode)
	cfg := config.Load()
	if err := logging.Configure(logging.Options{
		Level:       cfg.Log.Level,
		Format:      cfg.Log.Format,
		AddCaller:   cfg.Log.AddCaller,
		FileEnabled: cfg.Log.FileEnabled,
		Dir:         cfg.Log.Dir,
		MaxSizeMB:   cfg.Log.MaxSizeMB,
		MaxBackups:  cfg.Log.MaxBackups,
		MaxAgeDays:  cfg.Log.MaxAgeDays,
	}); err != nil {
		log.Fatal(err)
	}
	defer logging.Sync()
	undoStdLog := logging.RedirectStdLog()
	defer undoStdLog()

	if err := cfg.ValidateAPI(); err != nil {
		fatal("configuration invalid", err)
	}
	database, err := marketdb.Open(cfg.MySQLDSN)
	if err != nil {
		fatal("database unavailable", err)
	}
	defer database.Close()
	if n, err := marketdb.RunMigrations(database); err != nil {
		fatal("migration failed", err)
	} else if n > 0 {
		log.Printf("[main] applied %d migration(s)", n)
	}
	var resolver auth.Resolver
	var botResolver auth.BotResolver
	// devIdentity is only consulted when AUTH_ENABLED=false. The Space role is
	// keyed on DEV_SPACE_ID; the authenticator rebinds it onto whichever Space
	// an actual request names (see middleware.Authenticator.devIdentityFor).
	devIdentity := model.Identity{
		UID:        cfg.DevAuthUID,
		Name:       cfg.DevAuthName,
		SpaceRoles: map[string]int{cfg.DevSpaceID: cfg.DevAuthSpaceRole},
	}
	if cfg.AuthEnabled {
		resolver = auth.NewCachedResolver(
			auth.NewHTTPResolver(cfg.OctoAPIURL),
			cfg.AuthCacheTTL,
			cfg.AuthCacheCapacity,
		)
		botResolver = auth.NewHTTPBotResolver(cfg.OctoAPIURL)
		log.Printf("[auth] enabled")
	} else {
		// The effective dev Space role is printed so a misconfiguration is
		// visible in the boot line rather than only as an unexplained 403 on
		// the review endpoints.
		log.Printf("[auth] disabled; using development identity %q in Space %q with space role %d (%s)",
			cfg.DevAuthUID, cfg.DevSpaceID, cfg.DevAuthSpaceRole, devSpaceRoleName(cfg.DevAuthSpaceRole))
	}
	authenticator := middleware.NewAuthenticator(
		cfg.AuthEnabled,
		resolver,
		devIdentity,
		cfg.DevSpaceID,
		botResolver,
	)

	// Admin routes carry no Space, so the admin dev identity gets no role map.
	adminAuth := middleware.NewAdminAuthenticator(
		cfg.AuthEnabled,
		resolver,
		model.Identity{UID: cfg.DevAuthUID, Name: cfg.DevAuthName},
	)

	// Fleet client powers POST /experts/{id}/install. Left nil (interface, not a
	// typed-nil) when OCTO_FLEET_URL is unset so the endpoint returns a clean
	// UPSTREAM_UNAVAILABLE instead of dialing an empty base URL.
	var fleetClient expertsvc.FleetProvisioner
	if cfg.OctoFleetURL != "" {
		fleetClient = fleet.New(cfg.OctoFleetURL)
		log.Printf("[fleet] expert install enabled (url=%q)", cfg.OctoFleetURL)
	} else {
		log.Printf("[fleet] expert install disabled: OCTO_FLEET_URL not set")
	}

	mcpSvc := service.New(repository.New(database)).WithProbeAllowPrivate(cfg.ProbeAllowPrivate)
	if cfg.Storage.Enabled() {
		mcpSvc.WithIconStore(
			blob.NewS3Client(blob.S3Config{
				Endpoint:      cfg.Storage.Endpoint,
				Region:        cfg.Storage.Region,
				Bucket:        cfg.Storage.Bucket,
				AccessKey:     cfg.Storage.AccessKey,
				SecretKey:     cfg.Storage.SecretKey,
				PublicBaseURL: cfg.Storage.PublicBaseURL,
				PathStyle:     cfg.Storage.PathStyle,
			}),
			service.IconConfig{Partition: cfg.Storage.IconPartition},
		)
		log.Printf("[storage] icon uploads enabled (bucket=%q)", cfg.Storage.Bucket)
	} else {
		log.Printf("[storage] object storage not configured; icon uploads disabled")
	}
	mcpHandler := handler.NewMCP(mcpSvc)
	adminMCPHandler := handler.NewAdminMCP(mcpSvc)
	devBotMode := !cfg.AuthEnabled && cfg.IsDev()
	if devBotMode {
		log.Printf("[bot-publish] WARNING: dev bot mode enabled; this must not be active outside local development")
	}

	// Start flush worker if Redis is configured.
	flushCtx, flushCancel := context.WithCancel(context.Background())
	defer flushCancel()
	var metricsRDB *goredis.Client
	if cfg.RedisURL != "" {
		opts, err := goredis.ParseURL(cfg.RedisURL)
		if err == nil {
			metricsRDB = goredis.NewClient(opts)
			defer func() {
				if err := metricsRDB.Close(); err != nil {
					log.Printf("[redis] close failed: %v", err)
				}
			}()
			mRepo := metricsrepo.New(database)
			flushCfg := metricssvc.DefaultFlushWorkerConfig()
			flushCfg.Interval = cfg.MetricsFlushInterval
			flushCfg.Batch = int64(cfg.MetricsFlushBatch)
			flushCfg.LockTTL = cfg.MetricsFlushLockTTL
			fw := metricssvc.NewFlushWorker(metricsRDB, mRepo, flushCfg)
			go fw.Start(flushCtx)
			log.Printf("[flush-worker] enabled (interval=%s)", cfg.MetricsFlushInterval)
		} else {
			logging.Warn("flush_worker_disabled", zap.String("reason", "invalid REDIS_URL"), logging.ErrorField(err))
		}
	} else {
		log.Printf("[flush-worker] disabled: REDIS_URL not set")
	}

	publicServer := &http.Server{
		Addr: ":" + cfg.APIPort,
		Handler: router.Public(database, authenticator, adminAuth, router.StorageConfig{
			Driver:             cfg.StorageDriver,
			LocalDir:           cfg.LocalStorageDir,
			BaseURL:            publicBaseURL(cfg),
			MaxMB:              cfg.MaxUploadMB,
			OSSEndpoint:        cfg.OSSEndpoint,
			OSSBucket:          cfg.OSSBucket,
			OSSAccessKey:       cfg.OSSAccessKey,
			OSSSecretKey:       cfg.OSSSecretKey,
			OSSRegion:          cfg.OSSRegion,
			OSSKeyPrefix:       cfg.OSSKeyPrefix,
			OSSPathStyle:       cfg.OSSPathStyle,
			OSSPublicEndpoint:  cfg.OSSPublicEndpoint,
			OSSPublicPathStyle: cfg.OSSPublicPathStyle,
			OSSSigningHost:     cfg.OSSSigningHost,
			OSSDownloadSigned:  cfg.OSSDownloadSigned,
			CORSAllowedOrigins: cfg.CORSAllowedOrigins,
		}, mcpHandler, adminMCPHandler, router.ParseConfig{
			ParseTimeout:      cfg.SkillParseTimeout,
			StaleTimeout:      cfg.SkillParseStaleTimeout,
			MaxAttempts:       cfg.SkillParseMaxAttempts,
			WorkerPoolSize:    cfg.SkillParseWorkerPoolSize,
			BotPublishTimeout: cfg.BotPublishTimeout,
			DevBotMode:        devBotMode,
		}, fleetClient, router.ReviewConfig{
			// Blank InternalToken / CardActionSecret disable the card dispatch
			// and the callback endpoint respectively; see the config fields.
			OctoAPIURL:        cfg.OctoAPIURL,
			InternalToken:     cfg.OctoInternalToken,
			CardActionSecret:  cfg.OctoCardActionSecret,
			NotifyTimeout:     cfg.OctoNotifyTimeout,
			CardActionMaxSkew: cfg.OctoCardActionMaxSkew,
		}, router.RedisConfig{Client: metricsRDB}),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
	go serve("public", publicServer)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	flushCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = publicServer.Shutdown(ctx)
}

// devSpaceRoleName labels the dev Space role in the boot line. ValidateAPI has
// already rejected anything outside 0..2 by the time this runs.
func devSpaceRoleName(role int) string {
	switch role {
	case model.SpaceRoleOwner:
		return "owner"
	case model.SpaceRoleAdmin:
		return "admin"
	case model.SpaceRoleMember:
		return "member, cannot review"
	default:
		return "unknown"
	}
}

func publicBaseURL(cfg config.Config) string {
	if cfg.PublicBaseURL != "" {
		return cfg.PublicBaseURL
	}
	return "http://127.0.0.1:" + cfg.APIPort
}

func serve(name string, server *http.Server) {
	log.Printf("[%s] listening on %s", name, server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatal(name+" server failed", err)
	}
}

func fatal(message string, err error) {
	logging.Error(message, logging.ErrorField(err))
	logging.Sync()
	os.Exit(1)
}
