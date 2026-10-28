package main

import (
	"context"
	"fmt"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/artifacts"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/snapshot"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This optional test only creates private disposable databases under the
// rehearsal prefix. MIGRATION_TEST_MYSQL_DSN must already name a rehearsal database.
func TestRehearseMySQLRawTimestampRestoreAndResume(t *testing.T) {
	dsn := os.Getenv("MIGRATION_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MIGRATION_TEST_MYSQL_DSN unset")
	}
	cfg, e := parseDSN(dsn, true)
	if e != nil {
		t.Fatal("integration database configuration rejected")
	}
	adminCfg := cfg.Clone()
	adminCfg.DBName = ""
	admin, e := openDB(adminCfg)
	if e != nil {
		t.Fatal("integration admin connection failed")
	}
	defer admin.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	sourceName := "octo_marketplace_rehearsal_cli_source_" + suffix
	targetName := "octo_marketplace_rehearsal_cli_target_" + suffix
	for _, name := range []string{sourceName, targetName} {
		if _, e = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); e != nil {
			t.Fatal("integration database creation failed")
		}
		defer admin.Exec("DROP DATABASE `" + name + "`")
	}
	sourceCfg := restoreConfig(cfg)
	sourceCfg.DBName = sourceName
	// Exercise snapshot APIs independently of the CLI UTC DSN normalization.
	sourceCfg.Params["time_zone"] = "'+08:00'"
	source, e := openDB(sourceCfg)
	if e != nil {
		t.Fatal("integration source connection failed")
	}
	defer source.Close()
	if _, e = source.Exec("CREATE TABLE clock_fixture (id INT PRIMARY KEY, at DATETIME(3), payload BLOB, optional_text TEXT NULL, derived INT GENERATED ALWAYS AS (id+1) STORED, updated_at TIMESTAMP(3) DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)) ENGINE=InnoDB"); e != nil {
		t.Fatal("fixture schema failed")
	}
	if _, e = source.Exec("INSERT INTO clock_fixture (id,at,payload,optional_text,updated_at) VALUES (1,'2026-10-28 12:34:56.123',?,NULL,'2026-10-28 12:34:56.123')", []byte{0, 255, 65}); e != nil {
		t.Fatal("fixture row failed")
	}
	if _, e = source.Exec("CREATE TABLE child_fixture (id INT PRIMARY KEY, parent INT, CONSTRAINT fixture_fk FOREIGN KEY (parent) REFERENCES clock_fixture(id)) ENGINE=InnoDB"); e != nil {
		t.Fatal("fixture foreign key failed")
	}
	if _, e = source.Exec("INSERT INTO child_fixture VALUES (1,1)"); e != nil {
		t.Fatal("fixture child failed")
	}
	var sourceEpoch string
	if e = source.QueryRow("SELECT UNIX_TIMESTAMP(updated_at) FROM clock_fixture WHERE id=1").Scan(&sourceEpoch); e != nil {
		t.Fatal("source epoch verification failed")
	}
	ctx := context.Background()
	b, e := snapshot.Export(ctx, source, filepath.Join(t.TempDir(), "archive"), "synthetic-integration-source")
	if e != nil {
		t.Fatal(e)
	}
	b, e = artifacts.Collect(ctx, b, nil, artifacts.DefaultObjectLimits())
	if e != nil {
		t.Fatal(e)
	}
	typedCfg := cfg.Clone()
	typedCfg.DBName = targetName
	typed, e := openDB(typedCfg)
	if e != nil {
		t.Fatal("typed target connection failed")
	}
	defer typed.Close()
	rawCfg := restoreConfig(typedCfg)
	rawCfg.Params["time_zone"] = "'-07:00'"
	raw, e := openDB(rawCfg)
	if e != nil {
		t.Fatal("raw target connection failed")
	}
	defer raw.Close()
	dir := t.TempDir()
	p := migration.Plan{ID: strings.Repeat("b", 64), SnapshotID: b.ID}
	if e = rehearse(ctx, raw, typed, b, p, dir); e != nil {
		t.Fatal(e)
	}
	if e = rehearse(ctx, raw, typed, b, p, dir); e != nil {
		t.Fatal("resume failed: ", e)
	}
	if e = verifyObjects(ctx, b, p, dir); e != nil {
		t.Fatal(e)
	}
	var at string
	var payload []byte
	var nullable *string
	var derived int
	if e = raw.QueryRow("SELECT at,payload,optional_text,derived FROM clock_fixture WHERE id=1").Scan(&at, &payload, &nullable, &derived); e != nil {
		t.Fatal("fixture verification failed")
	}
	if at != "2026-10-28 12:34:56.123" || string(payload) != string([]byte{0, 255, 65}) || nullable != nil || derived != 2 {
		t.Fatal("fixture preservation mismatch")
	}
	// Raw text hashes alone cannot detect a TIMESTAMP instant shifted between
	// different source/target session time zones. Compare absolute epochs too.
	var targetEpoch, sourceZone, targetZone, typedZone string
	if e = raw.QueryRow("SELECT UNIX_TIMESTAMP(updated_at), @@SESSION.time_zone FROM clock_fixture WHERE id=1").Scan(&targetEpoch, &targetZone); e != nil {
		t.Fatal("target epoch verification failed")
	}
	if e = source.QueryRow("SELECT @@SESSION.time_zone").Scan(&sourceZone); e != nil {
		t.Fatal("source session verification failed")
	}
	if e = typed.QueryRow("SELECT @@SESSION.time_zone").Scan(&typedZone); e != nil {
		t.Fatal("typed session verification failed")
	}
	if targetEpoch != sourceEpoch || sourceZone != "+08:00" || targetZone != "-07:00" || typedZone != "+00:00" {
		t.Fatal("timestamp instant or pooled session isolation mismatch")
	}
	var typedTimestamp time.Time
	if e = typed.QueryRow("SELECT updated_at FROM clock_fixture WHERE id=1").Scan(&typedTimestamp); e != nil {
		t.Fatal("typed timestamp verification failed")
	}
	if !typedTimestamp.Equal(time.Date(2026, 10, 28, 4, 34, 56, 123000000, time.UTC)) {
		t.Fatal("typed target timestamp instant mismatch")
	}
	r, e := migration.Verify(ctx, typed, p)
	if e != nil || !r.Complete || r.InstallVerified {
		t.Fatal("conversion verification failed")
	}
}
