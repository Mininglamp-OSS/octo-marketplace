package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

func nameBackfillTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("octo_marketplace_name_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE `" + name + "`") })
	cfg.DBName = name
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	if _, err = migrate.Exec(db, "mysql", source, migrate.Up); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestNameBackfillGlobalReservationsMySQL(t *testing.T) {
	for _, tc := range []struct {
		name, visibility, listing, otherVisibility string
		conflict                                   bool
	}{
		{"public/private", "public", "published", "private", false},
		{"system/private", "system", "published", "private", false},
		{"public/public", "public", "published", "public", true},
		{"system/system", "system", "published", "system", true},
		{"foreign-private", "private", "published", "private", false},
		{"unreleased-global", "system", "draft", "private", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := nameBackfillTestDB(t)
			otherListing := "draft"
			if tc.otherVisibility != "private" {
				otherListing = "published"
			}
			for _, r := range []struct{ id, name, space, visibility, listing string }{
				{"market", "Draft", "space-a", tc.visibility, tc.listing},
				{"tenant", " ＯＦＦＩＣＩＡＬ ", "space-b", tc.otherVisibility, otherListing},
			} {
				if _, err := db.Exec(`INSERT INTO plugins(plugin_id,plugin_name,plugin_type,tags_json,owner_uid,space_id,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,created_at,updated_at) VALUES (?,?,'skill',JSON_ARRAY(),'owner',?,?,?,JSON_OBJECT(),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),NOW(3),NOW(3))`, r.id, r.name, r.space, r.visibility, r.listing); err != nil {
					t.Fatal(err)
				}
			}
			if tc.listing == "published" {
				if _, err := db.Exec(`INSERT INTO plugin_versions(version_id,plugin_id,version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,created_by,created_at) VALUES ('release','market','1',JSON_OBJECT('plugin_name','Official'),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),JSON_ARRAY(),'owner',NOW(3))`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES ('market','release','1.0.0','owner',NOW(3))`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE plugins SET published_version_id='release' WHERE plugin_id='market'`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.Exec(`UPDATE plugins SET plugin_name='Official' WHERE plugin_id='market'`); err != nil {
					t.Fatal(err)
				}
			}
			for _, apply := range []bool{false, true} {
				var output bytes.Buffer
				err := runBackfill(context.Background(), db, apply, &output)
				if (err != nil) != tc.conflict {
					t.Fatalf("apply=%t error=%v report=%s", apply, err, output.String())
				}
				var out report
				if err := json.Unmarshal(output.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if out.Applied != (apply && !tc.conflict) {
					t.Fatalf("wrong apply result: %+v", out)
				}
				if tc.conflict && (len(out.DuplicateGroups) != 1 || out.DuplicateGroups[0].Scope != "global-market") {
					t.Fatalf("missing global conflict: %+v", out)
				}
			}
			var written int
			if err := db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE name_key IS NOT NULL`).Scan(&written); err != nil {
				t.Fatal(err)
			}
			if tc.conflict && written != 0 || !tc.conflict && written != 2 {
				t.Fatalf("unexpected writes: %d", written)
			}
		})
	}
}
