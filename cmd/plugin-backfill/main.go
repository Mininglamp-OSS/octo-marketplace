package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	backfill "github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/config"
	marketdb "github.com/Mininglamp-OSS/octo-marketplace/internal/db"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

func main() {
	// D10: this historical backfill entry is retired. It must not be able to
	// write after the dependency migration CLI takes over. Deployment still
	// revokes its DB account and removes/denies the old binary; this guard is a
	// fail-closed in-process barrier, not a substitute for that gate.
	fmt.Fprintln(os.Stderr, "plugin-backfill is retired; use marketplace-stock-migration with a frozen plan and offline authorization")
	os.Exit(2)
	var mode, dsn, phase string
	var runMigrations bool
	flag.StringVar(&mode, "mode", "dry-run", "dry-run, apply, or verify")
	flag.StringVar(&phase, "phase", "plan", "plan (deterministic historical backfill), enrich (icons, connector categories, tool counts, metrics migration), repackage (migrate stored packages to the plugin-lib layout), or expand-skills (STORAGE-AWARE: expand skill packages into the per-file attachment tree; needs object-storage credentials)")
	flag.StringVar(&dsn, "dsn", os.Getenv("MYSQL_DSN"), "MySQL DSN (default MYSQL_DSN)")
	flag.BoolVar(&runMigrations, "migrate", false, "run the embedded schema migrations (same set marketplace-api applies at startup) before the data phase; lets the standalone image prepare a database ahead of the service deploy")
	flag.Parse()
	if dsn == "" {
		fatal(fmt.Errorf("MYSQL_DSN or -dsn is required"))
	}
	db, e := marketdb.Open(dsn)
	if e != nil {
		fatal(e)
	}
	defer db.Close()
	if runMigrations {
		applied, err := marketdb.RunMigrations(db)
		if err != nil {
			fatal(fmt.Errorf("schema migrations: %w", err))
		}
		fmt.Fprintf(os.Stderr, "schema migrations applied: %d\n", applied)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	switch phase {
	case "plan":
		report, e := backfill.New(db).Run(ctx, backfill.Options{Mode: backfill.Mode(mode)})
		if e != nil {
			fatal(e)
		}
		if e = enc.Encode(report); e != nil {
			fatal(e)
		}
		gateExit(report.Observed.Missing > 0 || report.Observed.Conflicts > 0, report.Issues)
	case "enrich":
		report, e := backfill.New(db).Enrich(ctx, backfill.Options{Mode: backfill.Mode(mode)})
		if e != nil {
			fatal(e)
		}
		if e = enc.Encode(report); e != nil {
			fatal(e)
		}
		gateExit(report.Remaining != (backfill.EnrichCounts{}), report.Issues)
	case "repackage":
		report, e := backfill.New(db).Repackage(ctx, backfill.Options{Mode: backfill.Mode(mode)})
		if e != nil {
			fatal(e)
		}
		if e = enc.Encode(report); e != nil {
			fatal(e)
		}
		gateExit(report.Remaining != (backfill.RepackageCounts{}), report.Issues)
	case "expand-skills":
		expander := newSkillExpander(db)
		report, e := backfill.New(db).ExpandSkills(ctx, backfill.Options{Mode: backfill.Mode(mode)}, expander)
		if e != nil {
			fatal(e)
		}
		if e = enc.Encode(report); e != nil {
			fatal(e)
		}
		gateExit(report.Remaining != (backfill.ExpandCounts{}), report.Issues)
	default:
		fatal(fmt.Errorf("invalid phase %q", phase))
	}
}

// gateExit fails the process (exit 2) when a phase left work undone in ANY mode,
// not just verify (P1-5): a non-zero Remaining count, or any recorded error/skip
// Issue (rows that could not be migrated). The runbook treats exit 2 as "review
// the issues before proceeding", so an apply that silently skipped rows can no
// longer exit 0 and read as fully migrated.
func gateExit(remainingNonZero bool, issues []backfill.Issue) {
	if remainingNonZero {
		os.Exit(2)
	}
	for _, is := range issues {
		if is.Level == "error" || is.Level == "skip" {
			os.Exit(2)
		}
	}
}

// newSkillExpander builds a storage-backed plugin service whose ExpandSkillPackage
// the expand-skills phase drives. Storage config comes from the same environment
// the marketplace-api reads, so this phase requires object-storage credentials
// (STORAGE_DRIVER + OSS_*/LOCAL_STORAGE_DIR) that the pure DB phases do not.
func newSkillExpander(db *sql.DB) *pluginsvc.Service {
	cfg := config.Load()
	var store storage.Storage
	switch cfg.StorageDriver {
	case "local":
		store = storage.NewLocal(cfg.LocalStorageDir, cfg.PublicBaseURL)
	case "oss":
		oss, err := storage.NewOSS(storage.OSSConfig{
			Endpoint:        cfg.OSSEndpoint,
			Bucket:          cfg.OSSBucket,
			AccessKey:       cfg.OSSAccessKey,
			SecretKey:       cfg.OSSSecretKey,
			Region:          cfg.OSSRegion,
			KeyPrefix:       cfg.OSSKeyPrefix,
			PathStyle:       cfg.OSSPathStyle,
			PublicEndpoint:  cfg.OSSPublicEndpoint,
			PublicPathStyle: cfg.OSSPublicPathStyle,
			SigningHost:     cfg.OSSSigningHost,
			DownloadSigned:  cfg.OSSDownloadSigned,
		})
		if err != nil {
			fatal(fmt.Errorf("storage driver oss: %w", err))
		}
		store = oss
	default:
		fatal(fmt.Errorf("unsupported STORAGE_DRIVER %q for expand-skills", cfg.StorageDriver))
	}
	svc := pluginsvc.New(pluginrepo.New(db), store)
	svc.SetArtifactLimits(int64(cfg.MaxUploadMB) << 20)
	return svc
}
func fatal(e error) { fmt.Fprintln(os.Stderr, "plugin-backfill:", e); os.Exit(1) }
