package main

import (
	"bytes"
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/snapshot"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvalidFlagsNeverEchoCredentials(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"snapshot", "--dsn", "user:secret@tcp(private)/db"}, func(string) string { return "" }, &out)
	if code == 0 || strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "private") {
		t.Fatal("unsafe invalid arguments")
	}
}
func TestDSNGuardAndSourceRawTimes(t *testing.T) {
	for _, dsn := range []string{"user:secret@tcp(localhost)/market?multiStatements=true", "user:secret@tcp(localhost)/market?allowAllFiles=true"} {
		if _, e := parseDSN(dsn, false); e == nil {
			t.Fatal("unsafe DSN accepted")
		}
	}
	cfg, e := parseDSN("u:p@tcp(localhost)/market?parseTime=true", false)
	if e != nil || cfg.ParseTime {
		t.Fatal("source raw time contract violated")
	}
	if _, e = parseDSN("u:p@tcp(localhost)/production", true); e == nil {
		t.Fatal("unsafe target accepted")
	}
}
func TestRehearseRequiresExplicitApplyAndIdentity(t *testing.T) {
	for _, args := range [][]string{{"rehearse", "--dir", "archive", "--namespace", "dmwork-prod", "--apply", "--expected-snapshot", "id", "--target-storage-dir", "objects"}, {"rehearse", "--dir", "archive", "--namespace", "dmwork-test", "--apply", "--target-storage-dir", "objects"}} {
		var out bytes.Buffer
		if run(args, func(string) string { return "" }, &out) == 0 {
			t.Fatal("unsafe rehearse accepted")
		}
	}
}
func TestRehearseRejectsSourceCredentialsBeforeOpeningArchive(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"rehearse", "--dir", "missing", "--namespace", "dmwork-test", "--apply", "--expected-snapshot", strings.Repeat("a", 64), "--target-storage-dir", "objects"}, func(k string) string {
		if k == "MIGRATION_SOURCE_DSN" {
			return "private-secret"
		}
		return ""
	}, &out)
	if code == 0 || !strings.Contains(out.String(), "source_credentials_present") || strings.Contains(out.String(), "private-secret") {
		t.Fatal(out.String())
	}
}

func TestResumeUsesProgressWithoutRestoringConvertedTables(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	b := snapshot.Bundle{ID: strings.Repeat("a", 64)}
	p := migration.Plan{ID: "plan"}
	m.ExpectQuery("SELECT COUNT").WithArgs(progressTable).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	m.ExpectQuery("SELECT snapshot_id, plan_id, storage_identity").WillReturnRows(sqlmock.NewRows([]string{"snapshot", "plan", "storage"}).AddRow(b.ID, p.ID, storageIdentity("objects")))
	if e := ensureRestored(context.Background(), db, b, p, "objects"); e != nil {
		t.Fatal(e)
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestResumeRejectsChangedPlanBeforeWrites(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	b := snapshot.Bundle{ID: strings.Repeat("a", 64)}
	p := migration.Plan{ID: "changed"}
	m.ExpectQuery("SELECT COUNT").WithArgs(progressTable).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	m.ExpectQuery("SELECT snapshot_id, plan_id, storage_identity").WillReturnRows(sqlmock.NewRows([]string{"snapshot", "plan", "storage"}).AddRow(b.ID, "original", storageIdentity("objects")))
	if e := ensureRestored(context.Background(), db, b, p, "objects"); e == nil {
		t.Fatal("changed run adopted")
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestTargetStorageRejectsPopulatedUnownedDirectory(t *testing.T) {
	id := strings.Repeat("a", 64)
	d := filepath.Join(t.TempDir(), "rehearsal-"+id[:12])
	os.Mkdir(d, 0700)
	os.WriteFile(filepath.Join(d, "unrelated"), []byte("data"), 0600)
	if validateTargetStorage(d, id) == nil {
		t.Fatal("unrelated storage adopted")
	}
}

func TestStorageOverlapResolvesParentSymlinks(t *testing.T) {
	base := t.TempDir()
	archive := filepath.Join(base, "archive")
	os.Mkdir(archive, 0700)
	nested := filepath.Join(archive, "rehearsal-aaaaaaaaaaaa")
	os.Mkdir(nested, 0700)
	alias := filepath.Join(base, "alias")
	os.Symlink(archive, alias)
	if !storageOverlaps(filepath.Join(alias, "rehearsal-aaaaaaaaaaaa"), archive) {
		t.Fatal("aliased archive became object target")
	}
}

func TestRestoreConnectorKeepsRawTemporalBytes(t *testing.T) {
	typed, e := parseDSN("u:p@tcp(localhost)/octo_marketplace_rehearsal_test?parseTime=true", true)
	if e != nil {
		t.Fatal(e)
	}
	raw := restoreConfig(typed)
	if raw.ParseTime || !typed.ParseTime || raw == typed || raw.DBName != typed.DBName {
		t.Fatal("restore must use an independent raw-time connector")
	}
}

func TestDSNForcesUTCSessionForRawAndTypedConnections(t *testing.T) {
	for _, target := range []bool{false, true} {
		cfg, e := parseDSN("u:p@tcp(localhost)/octo_marketplace_rehearsal_test?time_zone=%27%2B08%3A00%27&loc=Local", target)
		if e != nil {
			t.Fatal(e)
		}
		if cfg.Params["time_zone"] != "'+00:00'" || cfg.Loc.String() != "UTC" {
			t.Fatal("migration connection must use UTC on server and client")
		}
		if restoreConfig(cfg).Params["time_zone"] != "'+00:00'" {
			t.Fatal("raw restore lost UTC session")
		}
	}
}
