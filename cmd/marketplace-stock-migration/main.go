package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/stockmigration"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/go-sql-driver/mysql"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	f := flag.NewFlagSet("marketplace-stock-migration", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	artifactDir := f.String("artifact-dir", "", "explicit operator-authorized local object root (read only)")
	fixture := f.String("fixture", "", "offline evidence JSON")
	all := f.Bool("all-spaces", false, "acknowledge database-wide inventory")
	apply := f.Bool("apply", false, "apply one root from preview")
	rollback := f.Bool("rollback", false, "restore root pointers and retire batch")
	batch := f.String("batch-id", "", "audited batch identity")
	root := f.String("root-id", "", "one root from preview")
	digest := f.String("expected-digest", "", "fresh preview evidence digest")
	policy := f.Bool("g4-stop-on-exceptions-approved", false, "separately approved G4 stop-on-any-exception rule")
	limit := f.Int("limit", 10000, "maximum rows per inventory table")
	if f.Parse(args) != nil || f.NArg() != 0 || (*apply && *rollback) {
		return fmt.Errorf("invalid stock migration arguments")
	}
	var resolver stockmigration.SkillResolver
	if *artifactDir != "" {
		info, err := os.Stat(*artifactDir)
		if err != nil || !info.IsDir() || !filepath.IsAbs(*artifactDir) {
			return fmt.Errorf("artifact root must be an existing absolute directory")
		}
		store := storage.NewLocal(*artifactDir, "")
		resolver = func(ctx context.Context, p *model.Plugin) (map[string]string, error) {
			return pluginsvc.ResolveMigrationSkillFiles(ctx, store, p)
		}
	}
	if *fixture != "" {
		if *apply || *rollback || *all || *batch != "" || *root != "" || *digest != "" || *policy {
			return fmt.Errorf("fixture mode is preview only")
		}
		file, e := os.Open(*fixture)
		if e != nil {
			return fmt.Errorf("fixture read failed")
		}
		defer file.Close()
		raw, e := io.ReadAll(io.LimitReader(file, (32<<20)+1))
		if e != nil || len(raw) > 32<<20 {
			return fmt.Errorf("fixture exceeds limit")
		}
		var data stockmigration.Dataset
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&data) != nil || decoder.Decode(new(any)) != io.EOF {
			return fmt.Errorf("fixture decode failed")
		}
		return json.NewEncoder(out).Encode(stockmigration.Build(stockmigration.Hydrate(context.Background(), data, resolver)))
	}
	if !*all {
		return fmt.Errorf("database mode requires --all-spaces")
	}
	if !*apply && !*rollback && (*batch != "" || *root != "" || *digest != "" || *policy) {
		return fmt.Errorf("write options require explicit operation")
	}
	cfg, e := mysql.ParseDSN(os.Getenv("MARKETPLACE_STOCK_MIGRATION_DSN"))
	if e != nil || cfg.User == "" || cfg.DBName == "" || cfg.MultiStatements || cfg.AllowAllFiles || len(cfg.Params) > 0 {
		return fmt.Errorf("invalid stock migration database configuration")
	}
	cfg.ParseTime = true
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		return fmt.Errorf("database connection failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if *rollback {
		if *root != "" || *digest != "" || *policy {
			return fmt.Errorf("rollback accepts only batch identity")
		}
		if e = stockmigration.Rollback(ctx, db, *batch); e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(map[string]string{"batch_id": *batch, "state": "rolled_back"})
	}
	if *apply {
		n, e := stockmigration.ApplyRootWithResolver(ctx, db, *limit, *batch, *root, *digest, *policy, resolver)
		if e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(map[string]any{"batch_id": *batch, "root_id": *root, "applied_nodes": n})
	}
	d, e := stockmigration.ReadWithResolver(ctx, db, *limit, resolver)
	if e != nil {
		return e
	}
	return json.NewEncoder(out).Encode(stockmigration.Build(d))
}
