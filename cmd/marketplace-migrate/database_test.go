package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

// Exercise the real command orchestration. Unexpected writes fail sqlmock.
func TestPreviewReadsOnlyOneDatabase(t *testing.T) {
	typed, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer typed.Close()
	raw, r, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	m.ExpectQuery(`SELECT DATABASE\(\), @@server_uuid`).WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow("production", "server"))
	m.ExpectQuery(`SELECT GET_LOCK`).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	for i := 0; i < 9; i++ {
		m.ExpectQuery(`SELECT .* LIMIT 0`).WillReturnRows(sqlmock.NewRows([]string{"unused"}))
	}
	m.ExpectExec(`SELECT RELEASE_LOCK`).WillReturnResult(sqlmock.NewResult(0, 0))
	r.ExpectBegin()
	for i := 0; i < 9; i++ {
		r.ExpectQuery(`SELECT .* LIMIT 0`).WillReturnRows(sqlmock.NewRows([]string{"unused"}))
	}
	for _, table := range []string{"plugins", "plugin_relations", "plugin_versions", "plugin_version_releases", "plugin_review_requests"} {
		r.ExpectQuery(`SELECT \* FROM ` + table + ` ORDER BY `).WillReturnRows(sqlmock.NewRows([]string{"unused"}))
	}
	r.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugin_category_placements ORDER BY placement_id")).WillReturnRows(sqlmock.NewRows([]string{"placement_id"}))
	r.ExpectCommit()
	opens := 0
	var out bytes.Buffer
	code := runWithDatabase([]string{"--database", "production"}, func(k string) string {
		switch k {
		case "MYSQL_DSN":
			return "user@tcp(localhost)/production"
		case "LOCAL_STORAGE_DIR":
			return t.TempDir()
		}
		return ""
	}, &out, func(c *mysql.Config) (*sql.DB, error) {
		if c.DBName != "production" {
			t.Fatal("connected another database")
		}
		opens++
		if opens == 1 {
			if !c.ParseTime {
				t.Fatal("typed connector not typed")
			}
			return typed, nil
		}
		if opens != 2 || c.ParseTime {
			t.Fatal("unexpected connector")
		}
		return raw, nil
	})
	if code != 0 || !strings.Contains(out.String(), `"dry_run":true`) {
		t.Fatalf("code=%d output=%s", code, out.String())
	}
	if opens != 2 {
		t.Fatal(opens)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
	if e = r.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestApplyRejectsReplacedSourceObjectBeforeDestinationOrSQLWrites(t *testing.T) {
	for _, kind := range []string{"object", "source row"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			key := "skills/source/SKILL.md"
			name := filepath.Join(dir, filepath.FromSlash(key))
			if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, func() []byte {
				if kind == "object" {
					return []byte("replacement")
				}
				return []byte("original")
			}(), 0600); err != nil {
				t.Fatal(err)
			}
			env := func(key string) string {
				switch key {
				case "MYSQL_DSN":
					return "user@tcp(localhost)/test"
				case "LOCAL_STORAGE_DIR":
					return dir
				}
				return ""
			}
			_, storeID, err := loadStorage(env)
			if err != nil {
				t.Fatal(err)
			}
			policy, _ := json.Marshal(migration.Policy{})
			stateFile := filepath.Join(t.TempDir(), "run.state")
			tables := map[string]string{}
			for _, table := range []string{"plugin_relations", "plugin_versions", "plugin_version_releases", "plugin_review_requests", "plugin_placements", "plugin_version_relation_refs", "plugin_version_reference_index_state", "plugin_audit_logs"} {
				tables[table] = digest([]byte("[]"))
			}
			rowJSON, _ := json.Marshal(map[string][]byte{"plugin_id": []byte("source"), "icon": []byte("")})
			rowHash := digest(rowJSON)
			node := migration.Node{SourceID: "source", SourceRowHash: rowHash, Evidence: map[string]migration.SourceEvidence{"source": {RowHash: rowHash, Tables: tables, Objects: []migration.SourceObject{{Key: key, Hash: digest([]byte("original")), Size: 8}}}}}
			state := runState{Version: 5, Database: migration.DatabaseIdentity{Name: "test", ServerUUID: "server"}, StorageID: storeID, PolicyID: digest(policy), Plan: migration.Plan{CategorySnapshotCaptured: true, ID: "run", SnapshotID: "snapshot", Nodes: []migration.Node{node}}}
			if err := saveState(stateFile, state); err != nil {
				t.Fatal(err)
			}
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery(`SELECT DATABASE\(\), @@server_uuid`).WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow("test", "server"))
			mock.ExpectQuery("SELECT GET_LOCK").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
			for i := 0; i < 9; i++ {
				mock.ExpectQuery(`SELECT .* LIMIT 0`).WillReturnRows(sqlmock.NewRows([]string{"unused"}))
			}
			mock.ExpectQuery(`SELECT DATABASE\(\),@@server_uuid`).WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow("test", "server"))
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT COUNT.*information_schema.TABLES").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugin_category_placements ORDER BY placement_id FOR UPDATE")).WillReturnRows(sqlmock.NewRows([]string{"placement_id"}))
			mock.ExpectQuery("SELECT COUNT.*information_schema.TABLES.*marketplace_conversion_receipt").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
			mock.ExpectQuery(`SELECT \* FROM plugins WHERE plugin_id=\? FOR UPDATE`).WithArgs("source").WillReturnRows(sqlmock.NewRows([]string{"plugin_id", "icon"}).AddRow("source", func() string {
				if kind == "source row" {
					return "changed"
				}
				return ""
			}()))
			if kind == "object" {
				for _, spec := range []struct{ table, owner, order string }{
					{"plugin_relations", "source_plugin_id", "relation_id"}, {"plugin_versions", "plugin_id", "version_id"}, {"plugin_version_releases", "plugin_id", "version_id"}, {"plugin_review_requests", "plugin_id", "review_id"}, {"plugin_placements", "plugin_id", "placement_id"}, {"plugin_version_relation_refs", "source_plugin_id", "source_version_id,relation_id"}, {"plugin_version_reference_index_state", "source_plugin_id", "source_version_id"}, {"plugin_audit_logs", "plugin_id", "audit_log_id"},
				} {
					mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM " + spec.table + " WHERE " + spec.owner + "=? ORDER BY " + spec.order + " FOR UPDATE")).WithArgs("source").WillReturnRows(sqlmock.NewRows([]string{"unused"}))
				}
			}
			mock.ExpectRollback()
			mock.ExpectExec("SELECT RELEASE_LOCK").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectClose()
			var out bytes.Buffer
			code := runWithDatabase([]string{"--database", "test", "--apply", "--state-file", stateFile}, env, &out, func(*mysql.Config) (*sql.DB, error) { return db, nil })
			if code != 2 || !strings.Contains(out.String(), func() string {
				if kind == "object" {
					return `"source_object_changed"`
				}
				return `"source_state_changed"`
			}()) || !strings.Contains(out.String(), `"keep_state_file":true`) {
				t.Fatalf("code=%d output=%s", code, out.String())
			}
			if _, err := loadState(stateFile); err != nil {
				t.Fatal("checkpoint damaged", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalStorageMissingDirectoryFailsWithoutCreatingIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	_, _, err := loadStorage(func(key string) string {
		if key == "LOCAL_STORAGE_DIR" {
			return dir
		}
		return ""
	})
	if err == nil {
		t.Fatal("missing source directory accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("preflight created source directory")
	}
}
