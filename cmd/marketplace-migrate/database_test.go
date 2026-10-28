package main

import (
	"bytes"
	"database/sql"
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
	for i := 0; i < 7; i++ {
		m.ExpectQuery(`SELECT .* LIMIT 0`).WillReturnRows(sqlmock.NewRows([]string{"unused"}))
	}
	m.ExpectExec(`SELECT RELEASE_LOCK`).WillReturnResult(sqlmock.NewResult(0, 0))
	r.ExpectBegin()
	for i := 0; i < 7; i++ {
		r.ExpectQuery(`SELECT .* LIMIT 0`).WillReturnRows(sqlmock.NewRows([]string{"unused"}))
	}
	for _, table := range []string{"plugins", "plugin_relations", "plugin_versions", "plugin_version_releases", "plugin_review_requests"} {
		r.ExpectQuery(`SELECT \* FROM ` + table + ` ORDER BY `).WillReturnRows(sqlmock.NewRows([]string{"unused"}))
	}
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
