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
	"io"
	"os"
	"sort"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/go-sql-driver/mysql"
)

type row struct {
	ID            string  `json:"plugin_id"`
	Name          string  `json:"plugin_name"`
	Type          string  `json:"plugin_type"`
	SpaceID       *string `json:"space_id,omitempty"`
	DeletedAt     *string `json:"deleted_at,omitempty"`
	IsEmbedded    bool    `json:"is_embedded,omitempty"`
	Key           string  `json:"-"`
	PublishedKey  string  `json:"-"`
	PublishedName string  `json:"-"`
	Scope         string  `json:"-"`
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
	return runBackfill(context.Background(), db, *apply, os.Stdout)
}

func runBackfill(ctx context.Context, db *sql.DB, apply bool, output io.Writer) error {
	rows, err := readRows(ctx, db)
	if err != nil {
		return err
	}
	out := analyzeRows(rows)
	if len(out.Invalid) > 0 || len(out.DuplicateGroups) > 0 {
		if err := json.NewEncoder(output).Encode(out); err != nil {
			return err
		}
		return fmt.Errorf("backfill blocked: resolve invalid names and duplicate groups first")
	}
	if !apply {
		return json.NewEncoder(output).Encode(out)
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
		// Lock and revalidate the row against the snapshot before writing. A
		// concurrent rename must abort the backfill rather than be overwritten by
		// a stale key.
		var name, typ, publishedName string
		var scope sql.NullString
		var deleted sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT p.plugin_name,p.plugin_type,p.space_id,DATE_FORMAT(p.deleted_at,'%Y-%m-%dT%H:%i:%s.%fZ'),COALESCE(JSON_UNQUOTE(JSON_EXTRACT(pv.manifest_json,'$.plugin_name')),'') FROM plugins p LEFT JOIN plugin_versions pv ON pv.plugin_id=p.plugin_id AND pv.version_id=p.published_version_id WHERE p.plugin_id=? FOR UPDATE`, r.ID).Scan(&name, &typ, &scope, &deleted, &publishedName); err != nil {
			return fmt.Errorf("lock %s: %w", r.ID, err)
		}
		if name != r.Name || typ != r.Type || (scope.Valid != (r.SpaceID != nil)) || (scope.Valid && r.SpaceID != nil && scope.String != *r.SpaceID) || deleted.Valid != (r.DeletedAt != nil) || publishedName != r.PublishedName {
			return fmt.Errorf("backfill %s: row changed after snapshot; re-run", r.ID)
		}
		var published any
		if r.PublishedKey != "" {
			published = r.PublishedKey
		}
		res, err := tx.ExecContext(ctx, `UPDATE plugins SET name_key=?,published_name_key=?,name_scope=? WHERE plugin_id=? AND deleted_at IS NULL`, r.Key, published, r.Scope, r.ID)
		if err != nil {
			return fmt.Errorf("backfill %s: %w", r.ID, err)
		}
		n, _ := res.RowsAffected()
		out.Backfilled += int(n)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	out.Applied = true
	return json.NewEncoder(output).Encode(out)
}

// analyzeRows is the preview authority: it normalizes both the draft and the
// published display name, groups every active reservation key, and reports a
// collision even when one plugin's published name collides with another plugin's
// draft name. The apply path revalidates and writes the same two keys.
func analyzeRows(rows []row) report {
	out := report{Total: len(rows)}
	groups := map[string]*duplicateGroup{}
	seenInGroup := map[string]map[string]struct{}{}
	for i := range rows {
		r := &rows[i]
		if r.Type != string(model.PluginTypeExpert) && r.Type != string(model.PluginTypeSkill) && r.Type != string(model.PluginTypeExpertTeam) {
			continue
		}
		if r.IsEmbedded {
			continue
		}
		if r.DeletedAt != nil {
			out.SkippedDeleted++
			continue
		}
		scope := ""
		if r.SpaceID != nil {
			scope = *r.SpaceID
		}
		keys, err := model.PluginNameReservationKeys(r.Name, r.PublishedName)
		if err != nil {
			out.Invalid = append(out.Invalid, r.ID)
			continue
		}
		r.Scope = scope
		draftKey, draftErr := model.NormalizePluginName(r.Name)
		if draftErr != nil {
			out.Invalid = append(out.Invalid, r.ID)
			continue
		}
		r.Key = draftKey
		if r.PublishedName != "" {
			publishedKey, publishedErr := model.NormalizePluginName(r.PublishedName)
			if publishedErr != nil {
				out.Invalid = append(out.Invalid, r.ID)
				continue
			}
			r.PublishedKey = publishedKey
		}
		for _, key := range keys {
			gkey := scope + "\x00" + r.Type + "\x00" + key
			g := groups[gkey]
			if g == nil {
				g = &duplicateGroup{Scope: scope, Type: r.Type, Key: key}
				groups[gkey] = g
				seenInGroup[gkey] = map[string]struct{}{}
			}
			if _, ok := seenInGroup[gkey][r.ID]; ok {
				continue
			}
			seenInGroup[gkey][r.ID] = struct{}{}
			g.IDs = append(g.IDs, r.ID)
		}
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
		if out.DuplicateGroups[i].Type != out.DuplicateGroups[j].Type {
			return out.DuplicateGroups[i].Type < out.DuplicateGroups[j].Type
		}
		return out.DuplicateGroups[i].Key < out.DuplicateGroups[j].Key
	})
	return out
}

func readRows(ctx context.Context, db *sql.DB) ([]row, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.plugin_id,p.plugin_name,p.plugin_type,p.is_embedded,p.space_id,DATE_FORMAT(p.deleted_at,'%Y-%m-%dT%H:%i:%s.%fZ'),COALESCE(JSON_UNQUOTE(JSON_EXTRACT(pv.manifest_json,'$.plugin_name')),'')
FROM plugins p LEFT JOIN plugin_versions pv ON pv.plugin_id=p.plugin_id AND pv.version_id=p.published_version_id
WHERE p.plugin_type IN ('expert','skill','expert_team') ORDER BY p.plugin_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		var space, deleted sql.NullString
		if err := rows.Scan(&r.ID, &r.Name, &r.Type, &r.IsEmbedded, &space, &deleted, &r.PublishedName); err != nil {
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
