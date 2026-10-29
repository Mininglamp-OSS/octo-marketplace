package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

// Exercise distinct command invocations: an in-memory retry cannot detect a
// checkpoint codec changing nil/empty slices or pointers to empty strings.
func TestFreshCheckpointApplyVerifyAndReapplyMySQL(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_MYSQL_DSN required in CI")
		}
		t.Skip("TEST_MYSQL_DSN not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open admin")
	}
	defer admin.Close()
	name := fmt.Sprintf("octo_marketplace_checkpoint_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP DATABASE `" + name + "`")
	cfg.DBName, cfg.MultiStatements = name, true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open fixture")
	}
	defer db.Close()
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	if _, err = migrate.Exec(db, "mysql", source, migrate.Up); err != nil {
		t.Fatal(err)
	}
	manifest := `{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"fixture","plugin_type":"skill","name":"fixture","description":"fixture description","labels":[],"examples":[]}`
	pkg := `{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"---\nname: fixture\ndescription: fixture description\n---\nInstructions"}]}`
	if _, err = db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,space_id,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,created_at,updated_at) VALUES ('fixture','fixture','skill',JSON_ARRAY(),'owner','','system','published',CAST(? AS JSON),CAST(? AS JSON),'','',NOW(3),NOW(3))`, manifest, pkg); err != nil {
		t.Fatal(err)
	}
	cfg.MultiStatements = false
	dir := t.TempDir()
	state := filepath.Join(dir, "migration.state")
	env := func(k string) string {
		switch k {
		case "MYSQL_DSN":
			return cfg.FormatDSN()
		case "LOCAL_STORAGE_DIR":
			return dir
		case "STORAGE_DRIVER":
			return "local"
		}
		return ""
	}
	for _, mode := range []string{"--apply", "--verify", "--apply"} {
		var out bytes.Buffer
		code := run([]string{"--database", name, mode, "--state-file", state}, env, &out)
		var report struct {
			Complete bool `json:"complete"`
			Verified int
		}
		if err = json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if code != 0 || !report.Complete || report.Verified != 1 {
			t.Fatalf("%s: code=%d output=%s", mode, code, out.String())
		}
	}
}
