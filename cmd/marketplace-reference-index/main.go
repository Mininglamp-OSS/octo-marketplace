// marketplace-reference-index is an explicitly operated maintenance tool.
// It never runs as part of server startup or migrations.
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
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/referenceindex"
	"github.com/go-sql-driver/mysql"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("marketplace-reference-index", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	fixture := flags.String("fixture", "", "offline evidence JSON")
	all := flags.Bool("all-spaces", false, "acknowledge database-wide evidence read")
	limit := flags.Int("limit", 10000, "maximum rows per evidence table")
	apply := flags.Bool("apply", false, "insert only verified index facts for one version")
	version := flags.String("version-id", "", "one immutable source version to apply")
	digest := flags.String("expected-digest", "", "snapshot digest from preview")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return fmt.Errorf("invalid reference index arguments")
	}
	if *fixture != "" {
		if *apply || *all || *version != "" || *digest != "" {
			return fmt.Errorf("fixture preview cannot apply")
		}
		f, err := os.Open(*fixture)
		if err != nil {
			return fmt.Errorf("fixture read failed")
		}
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
		if err != nil || len(raw) > 32<<20 {
			return fmt.Errorf("fixture exceeds size limit")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var data referenceindex.Dataset
		if decoder.Decode(&data) != nil {
			return fmt.Errorf("fixture decode failed")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return fmt.Errorf("fixture must contain one JSON document")
		}
		return json.NewEncoder(out).Encode(referenceindex.Build(data))
	}
	if !*all {
		return fmt.Errorf("database mode requires explicit --all-spaces")
	}
	if !*apply && (*version != "" || *digest != "") {
		return fmt.Errorf("apply selection requires --apply")
	}
	cfg, err := mysql.ParseDSN(os.Getenv("MARKETPLACE_REFERENCE_INDEX_DSN"))
	if err != nil || cfg.User == "" || cfg.DBName == "" || cfg.MultiStatements || cfg.AllowAllFiles || len(cfg.Params) > 0 {
		return fmt.Errorf("invalid reference index database configuration")
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("database connection failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if *apply {
		n, err := referenceindex.ApplyVersion(ctx, db, *limit, *version, *digest)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(struct {
			VersionID string `json:"version_id"`
			Inserted  int    `json:"inserted"`
		}{*version, n})
	}
	d, err := referenceindex.Read(ctx, db, *limit)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(referenceindex.Build(d))
}
