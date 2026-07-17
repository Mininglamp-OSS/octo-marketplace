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
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/repository"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service"
	"github.com/gin-gonic/gin"
)

// @title Octo Marketplace API
// @version 1.0.0
// @description Skill and MCP marketplace API for OCTO.
// @contact.name OCTO API Team
// @contact.url https://github.com/Mininglamp-OSS/octo-marketplace
// @BasePath /v1
// @tag.name skill
// @tag.description Skill catalog and releases
// @tag.name skill_upload
// @tag.description Skill artifact ingestion and parsing
// @tag.name skill_category
// @tag.description Skill catalog categories
// @tag.name mcp
// @tag.description MCP server catalog
// @tag.name admin_mcp
// @tag.description Administrative MCP catalog
// @securityDefinitions.apikey Bearer
// @in header
// @name Authorization

func main() {
	gin.SetMode(gin.ReleaseMode)
	cfg := config.Load()
	if err := cfg.ValidateAPI(); err != nil {
		log.Fatal(err)
	}
	database, err := marketdb.Open(cfg.MySQLDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	if n, err := marketdb.RunMigrations(database); err != nil {
		log.Fatalf("[main] migration failed: %v", err)
	} else if n > 0 {
		log.Printf("[main] applied %d migration(s)", n)
	}
	var resolver auth.Resolver
	var botResolver auth.BotResolver
	if cfg.AuthEnabled {
		resolver = auth.NewCachedResolver(
			auth.NewHTTPResolver(cfg.OctoAPIURL),
			cfg.AuthCacheTTL,
			cfg.AuthCacheCapacity,
		)
		botResolver = auth.NewHTTPBotResolver(cfg.OctoAPIURL)
		log.Printf("[auth] enabled")
	} else {
		log.Printf("[auth] disabled; using development identity %q in Space %q", cfg.DevAuthUID, cfg.DevSpaceID)
	}
	authenticator := middleware.NewAuthenticator(
		cfg.AuthEnabled,
		resolver,
		model.Identity{UID: cfg.DevAuthUID, Name: cfg.DevAuthName},
		cfg.DevSpaceID,
		botResolver,
	)

	adminAuth := middleware.NewAdminAuthenticator(
		cfg.AuthEnabled,
		resolver,
		model.Identity{UID: cfg.DevAuthUID, Name: cfg.DevAuthName},
	)

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
			OSSSigningHost:     cfg.OSSSigningHost,
			OSSDownloadSigned:  cfg.OSSDownloadSigned,
			CORSAllowedOrigins: cfg.CORSAllowedOrigins,
		}, mcpHandler, adminMCPHandler, router.ParseConfig{
			ParseTimeout:   cfg.SkillParseTimeout,
			StaleTimeout:   cfg.SkillParseStaleTimeout,
			MaxAttempts:    cfg.SkillParseMaxAttempts,
			WorkerPoolSize: cfg.SkillParseWorkerPoolSize,
		}, router.RedisConfig{URL: cfg.RedisURL}),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
	go serve("public", publicServer)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = publicServer.Shutdown(ctx)
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
		log.Fatalf("[%s] %v", name, err)
	}
}
