// Command plugin-name-backfill computes the normalized display-name key for
// existing Experts/Skills. It is deliberately separate from startup migrations:
// production operators run a read-only report first, resolve duplicate groups,
// and only then apply the backfill. The unique active-name index is already in
// place; unbackfilled historical rows have name_key=NULL and therefore do not
// block deployment, but also do not participate in uniqueness until backfilled.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/go-sql-driver/mysql"
)

type row struct {
	ID         string  `json:"plugin_id"`
	Name       string  `json:"plugin_name"`
	Type       string  `json:"plugin_type"`
	SpaceID    *string `json:"space_id,omitempty"`
	DeletedAt  *string `json:"deleted_at,omitempty"`
	IsEmbedded bool    `json:"is_embedded,omitempty"`
	Key        string  `json:"-"`
	Scope      string  `json:"-"`
}

type duplicateGroup struct {
	Scope string   `json:"scope"`
	Type  string   `json:"plugin_type"`
	Key   string   `json:"normalized_name"`
	IDs   []string `json:"plugin_ids"`
}

type report struct {
	Applied         bool             `json:"applied"`
	Total           int              `json:"total"`
	Backfilled      int              `json:"backfilled"`
	SkippedDeleted  int              `json:"skipped_soft_deleted"`
	Invalid         []string         `json:"invalid,omitempty"`
	DuplicateGroups []duplicateGroup `json:"duplicate_groups,omitempty"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("plugin-name-backfill", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "write name_key/name_scope after all duplicate groups are resolved")
	allSpaces := fs.Bool("all-spaces", false, "acknowledge a database-wide backfill")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return fmt.Errorf("usage: plugin-name-backfill --all-spaces [--apply]")
	}
	if !*allSpaces {
		return fmt.Errorf("--all-spaces is required")
	}
	dsn := os.Getenv("MYSQL_DSN")
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || cfg.User == "" || cfg.DBName == "" || cfg.MultiStatements {
		return fmt.Errorf("invalid MYSQL_DSN")
	}
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	ctx := context.Background()
	rows, err := readRows(ctx, db)
	if err != nil {
		return err
	}
	out := report{Applied: *apply, Total: len(rows)}
	groups := map[string]*duplicateGroup{}
	for i := range rows {
		r := &rows[i]
		if r.Type != string(model.PluginTypeExpert) && r.Type != string(model.PluginTypeSkill) {
			continue
		}
		// Embedded legacy rows are not market-display names and are excluded by
		// active_name_key; only their extracted standalone replacements participate.
		if r.IsEmbedded {
			continue
		}
		scope := ""
		if r.SpaceID != nil {
			scope = *r.SpaceID
		}
		key, nerr := model.NormalizePluginName(r.Name)
		if nerr != nil {
			out.Invalid = append(out.Invalid, r.ID)
			continue
		}
		r.Key, r.Scope = key, scope
		// Soft-deleted rows are skipped: their active_name_key is NULL and they must
		// not reserve a name. Delisted rows are active and do participate.
		if r.DeletedAt != nil {
			out.SkippedDeleted++
			continue
		}
		gkey := scope + "\x00" + r.Type + "\x00" + key
		g := groups[gkey]
		if g == nil {
			g = &duplicateGroup{Scope: scope, Type: r.Type, Key: key}
			groups[gkey] = g
		}
		g.IDs = append(g.IDs, r.ID)
	}
	for _, g := range groups {
		if len(g.IDs) > 1 {
			sort.Strings(g.IDs)
			out.DuplicateGroups = append(out.DuplicateGroups, *g)
		}
	}
	sort.Slice(out.DuplicateGroups, func(i, j int) bool {
		if out.DuplicateGroups[i].Scope != out.DuplicateGroups[j].Scope {
			return out.DuplicateGroups[i].Scope < out.DuplicateGroups[j].Scope
		}
		return out.DuplicateGroups[i].Key < out.DuplicateGroups[j].Key
	})
	if len(out.Invalid) > 0 || len(out.DuplicateGroups) > 0 {
		if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
			return err
		}
		return fmt.Errorf("backfill blocked: resolve invalid names and duplicate groups first")
	}
	if !*apply {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		if r.Key == "" || r.DeletedAt != nil {
			continue
		}
		res, err := tx.ExecContext(ctx, `UPDATE plugins SET name_key=?,name_scope=? WHERE plugin_id=? AND deleted_at IS NULL`, r.Key, r.Scope, r.ID)
		if err != nil {
			return fmt.Errorf("backfill %s: %w", r.ID, err)
		}
		n, _ := res.RowsAffected()
		out.Backfilled += int(n)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

func readRows(ctx context.Context, db *sql.DB) ([]row, error) {
	rows, err := db.QueryContext(ctx, `SELECT plugin_id,plugin_name,plugin_type,is_embedded,space_id,DATE_FORMAT(deleted_at,'%Y-%m-%dT%H:%i:%s.%fZ')
FROM plugins WHERE plugin_type IN ('expert','skill') ORDER BY plugin_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		var space, deleted sql.NullString
		if err := rows.Scan(&r.ID, &r.Name, &r.Type, &r.IsEmbedded, &space, &deleted); err != nil {
			return nil, err
		}
		if space.Valid {
			r.SpaceID = &space.String
		}
		if deleted.Valid {
			r.DeletedAt = &deleted.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
