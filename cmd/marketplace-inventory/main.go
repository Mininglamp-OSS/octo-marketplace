// marketplace-inventory is an offline/operator CLI, not a tenant HTTP API.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/inventory"
	"github.com/go-sql-driver/mysql"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("marketplace-inventory", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	fixture := flags.String("fixture", "", "read offline JSON metadata instead of a database")
	all := flags.Bool("all-spaces", false, "acknowledge operator inventory of all Marketplace spaces, including deleted records")
	max := flags.Int("max-rows", 100000, "maximum rows per table; exceeding this aborts")
	timeout := flags.Duration("timeout", 2*time.Minute, "database inventory deadline")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("invalid inventory arguments; use --fixture PATH or --all-spaces")
	}
	if *max < 1 || *max > 1000000 || *timeout <= 0 {
		return errors.New("inventory requires max-rows between 1 and 1000000 and a positive timeout")
	}
	var in inventory.Input
	if *fixture != "" {
		f, err := os.Open(*fixture)
		if err != nil {
			return errors.New("cannot open inventory fixture")
		}
		defer f.Close()
		dec := json.NewDecoder(io.LimitReader(f, 64<<20))
		dec.DisallowUnknownFields()
		if dec.Decode(&in) != nil {
			return errors.New("invalid inventory fixture")
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return errors.New("trailing or oversized inventory fixture")
		}
	} else {
		if !*all {
			return errors.New("database mode requires --all-spaces; use an authorized read-only database account")
		}
		raw := os.Getenv("MARKETPLACE_INVENTORY_DSN")
		if raw == "" {
			return errors.New("MARKETPLACE_INVENTORY_DSN is required")
		}
		cfg, err := mysql.ParseDSN(raw)
		if err != nil {
			return errors.New("invalid inventory database configuration")
		}
		// DSN session variables could execute SET statements; reject rather than
		// passing arbitrary session configuration through a read-only utility.
		if len(cfg.Params) > 0 || cfg.MultiStatements || cfg.AllowAllFiles {
			return errors.New("inventory DSN must not contain session parameters, multiStatements or allowAllFiles")
		}
		cfg.Timeout = 10 * time.Second
		cfg.ReadTimeout = 30 * time.Second
		cfg.WriteTimeout = 10 * time.Second
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			return errors.New("invalid inventory database configuration")
		}
		db := sql.OpenDB(connector)
		defer db.Close()
		db.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		in, err = inventory.Read(ctx, db, *max)
		if err != nil {
			return err
		}
	}
	enc := json.NewEncoder(output)
	enc.SetIndent("", "  ")
	if enc.Encode(inventory.Build(in)) != nil {
		return errors.New("cannot write inventory report")
	}
	return nil
}
