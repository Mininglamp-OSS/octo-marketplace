package router

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler"
	categoryhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/category"
	skillhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/skill"
	uploadhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/upload"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
	categorysvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/category"
	parsesvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/gin-gonic/gin"
)

type Pinger interface {
	PingContext(context.Context) error
}

// StorageConfig holds configuration for the skill archive storage layer.
type StorageConfig struct {
	Driver            string // "local" or "oss"
	LocalDir          string
	BaseURL           string
	MaxMB             int
	OSSEndpoint       string
	OSSBucket         string
	OSSAccessKey      string
	OSSSecretKey      string
	OSSRegion         string
	OSSKeyPrefix      string
	OSSPathStyle      bool
	OSSPublicEndpoint string
	OSSSigningHost    string
	OSSDownloadSigned bool
}

func Public(database Pinger, authenticator *marketmiddleware.Authenticator, adminAuth *marketmiddleware.AdminAuthenticator, storageCfg StorageConfig, mcp *handler.MCP, adminMCP *handler.AdminMCP) *gin.Engine {
	return publicWithOptions(database, authenticator, adminAuth, storageCfg, mcp, adminMCP, authenticator.AuthEnabled())
}

func publicWithOptions(database Pinger, authenticator *marketmiddleware.Authenticator, adminAuth *marketmiddleware.AdminAuthenticator, storageCfg StorageConfig, mcp *handler.MCP, adminMCP *handler.AdminMCP, authEnabled bool) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery(), corsMiddleware())

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
				Endpoint:       storageCfg.OSSEndpoint,
				Bucket:         storageCfg.OSSBucket,
				AccessKey:      storageCfg.OSSAccessKey,
				SecretKey:      storageCfg.OSSSecretKey,
				Region:         storageCfg.OSSRegion,
				KeyPrefix:      storageCfg.OSSKeyPrefix,
				PathStyle:      storageCfg.OSSPathStyle,
				PublicEndpoint: storageCfg.OSSPublicEndpoint,
				SigningHost:    storageCfg.OSSSigningHost,
				DownloadSigned: storageCfg.OSSDownloadSigned,
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
		catH.RegisterAdmin(v1, generateID, authEnabled)
		skillhandler.New(skSvc).Register(v1)

		parseRepo := parsesvc.NewRepo(db)
		worker := parsesvc.NewWorker(store, parseRepo, db)
		pSvc := parsesvc.NewService(store, parseRepo, worker, generateID, storageCfg.MaxMB)

		uploadH := uploadhandler.New(pSvc, skSvc, localStorage)
		uploadH.Register(v1)
		uploadH.RegisterLocalProxy(r)
	}

	registerMCP(r, authenticator, mcp)
	registerAdminMCP(r, adminAuth, adminMCP)
	return r
}

// registerMCP mounts the MCP catalog surface (docs/api/mcp-v1.md §4) under
// /api/v1/mcps.
func registerMCP(r *gin.Engine, authenticator *marketmiddleware.Authenticator, mcp *handler.MCP) {
	if mcp == nil {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/mcps", authenticator.WrapMarket(http.HandlerFunc(mcp.Create)))
	mux.Handle("GET /api/v1/mcps", authenticator.WrapMarket(http.HandlerFunc(mcp.List)))
	mux.Handle("GET /api/v1/mcps/mine", authenticator.WrapMarket(http.HandlerFunc(mcp.ListMine)))
	mux.Handle("POST /api/v1/mcps/probe", authenticator.WrapMarket(http.HandlerFunc(mcp.Probe)))
	mux.Handle("GET /api/v1/mcps/{id}", authenticator.WrapMarket(http.HandlerFunc(mcp.Get)))
	mux.Handle("PATCH /api/v1/mcps/{id}", authenticator.WrapMarket(http.HandlerFunc(mcp.Patch)))
	mux.Handle("DELETE /api/v1/mcps/{id}", authenticator.WrapMarket(http.HandlerFunc(mcp.Delete)))
	mux.Handle("POST /api/v1/mcps/{id}/icon", authenticator.WrapMarket(http.HandlerFunc(mcp.UploadIcon)))

	r.Any("/api/v1/mcps", gin.WrapH(mux))
	r.Any("/api/v1/mcps/*any", gin.WrapH(mux))
}

// registerAdminMCP mounts the admin surface for system MCPs at /api/v1/admin/mcps.
func registerAdminMCP(r *gin.Engine, adminAuth *marketmiddleware.AdminAuthenticator, admin *handler.AdminMCP) {
	if admin == nil {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/admin/mcps", adminAuth.Wrap(http.HandlerFunc(admin.Create)))
	mux.Handle("POST /api/v1/admin/mcps/probe", adminAuth.Wrap(http.HandlerFunc(admin.Probe)))
	mux.Handle("GET /api/v1/admin/mcps", adminAuth.Wrap(http.HandlerFunc(admin.List)))
	mux.Handle("GET /api/v1/admin/mcps/{id}", adminAuth.Wrap(http.HandlerFunc(admin.Get)))
	mux.Handle("PATCH /api/v1/admin/mcps/{id}", adminAuth.Wrap(http.HandlerFunc(admin.Patch)))
	mux.Handle("DELETE /api/v1/admin/mcps/{id}", adminAuth.Wrap(http.HandlerFunc(admin.Delete)))

	r.Any("/api/v1/admin/mcps", gin.WrapH(mux))
	r.Any("/api/v1/admin/mcps/*any", gin.WrapH(mux))
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type,Authorization,Token,X-Space-Id,X-Admin-Token,X-Request-Id")
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
	adminAuth := marketmiddleware.NewAdminAuthenticator(false, "", model.Identity{})
	return Public(db, authenticator, adminAuth, storageCfg, nil, nil)
}

// PublicWithDBAndAuth is a test helper that overrides the authEnabled flag for admin routes.
func PublicWithDBAndAuth(db *sql.DB, authenticator *marketmiddleware.Authenticator, storageCfg StorageConfig, authEnabled bool) *gin.Engine {
	adminAuth := marketmiddleware.NewAdminAuthenticator(false, "", model.Identity{})
	return publicWithOptions(db, authenticator, adminAuth, storageCfg, nil, nil, authEnabled)
}
