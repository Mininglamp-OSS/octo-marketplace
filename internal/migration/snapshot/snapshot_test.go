package snapshot

import (
	"context"
	"errors"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDDLRejectsInjectedOrCrossDatabase(t *testing.T) {
	for _, ddl := range []string{"CREATE TABLE `items` (id INT); DROP TABLE items", "CREATE TABLE `other`.`items` (id INT)", "CREATE TABLE `items` (id INT REFERENCES `other`.`users`(id))", "CREATE TABLE `wrong` (id INT)", "CREATE TABLE `items` (id INT) ENGINE=FEDERATED CONNECTION='secret'"} {
		if validateDDL(ddl, "items") == nil {
			t.Errorf("unsafe DDL accepted")
		}
	}
	if err := validateDDL("CREATE TABLE `items` (`id` bigint, `v` json, CONSTRAINT `fk` FOREIGN KEY (`id`) REFERENCES `parent` (`id`)) ENGINE=InnoDB", "items"); err != nil {
		t.Fatal(err)
	}
}
func TestLoadRejectsTamperAndSymlink(t *testing.T) {
	dir := t.TempDir()
	b := Bundle{Dir: dir, Manifest: Manifest{Format: 1, Source: Source{DBName: "market", Fingerprint: "prod"}}}
	if err := writeManifest(&b); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("tampered manifest accepted")
	}
	os.Remove(filepath.Join(dir, "manifest.json"))
	os.Symlink("/etc/passwd", filepath.Join(dir, "manifest.json"))
	if _, err := Load(dir); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestExportNullBinaryAndGenerated(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectExec("SET SESSION time_zone = ").WithArgs("+00:00").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectBegin()
	m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"database", "uuid", "version"}).AddRow("market", "server", "8.0"))
	m.ExpectQuery("SELECT TABLE_NAME").WillReturnRows(sqlmock.NewRows([]string{"name", "engine"}).AddRow("items", "InnoDB"))
	m.ExpectQuery("SHOW CREATE TABLE").WillReturnRows(sqlmock.NewRows([]string{"table", "ddl"}).AddRow("items", "CREATE TABLE `items` (`id` int, `data` blob, `nil` text, `derived` int GENERATED ALWAYS AS (`id` + 1) STORED) ENGINE=InnoDB"))
	m.ExpectQuery("SELECT COLUMN_NAME").WillReturnRows(sqlmock.NewRows([]string{"name", "extra"}).AddRow("id", "").AddRow("data", "").AddRow("nil", "").AddRow("derived", "STORED GENERATED"))
	m.ExpectQuery("SELECT `id`,`data`,`nil` FROM `items`").WillReturnRows(sqlmock.NewRows([]string{"id", "data", "nil"}).AddRow(1, []byte{0, 255}, nil))
	m.ExpectCommit()
	m.ExpectClose() // The changed source session must not return to the pool.
	b, err := Export(context.Background(), db, filepath.Join(t.TempDir(), "bundle"), "fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if b.Manifest.Tables[0].Rows != 1 || len(b.Manifest.Tables[0].Columns) != 3 {
		t.Fatal("rows/columns lost")
	}
	f, err := os.ReadFile(filepath.Join(b.Dir, "items.rows.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(f) != "[\"MQ==\",\"AP8=\",null]\n" {
		t.Fatalf("bad bytes: %q", f)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRejectsUnrelatedTargetBeforeWrites(t *testing.T) {
	dir := t.TempDir()
	b := Bundle{Dir: dir, Manifest: Manifest{Format: 1, Source: Source{DBName: "source", ServerUUID: "source-server"}}}
	if e := writeManifest(&b); e != nil {
		t.Fatal(e)
	}
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectExec("SET SESSION time_zone = ").WithArgs("+00:00").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db", "server"}).AddRow("production", "target"))
	if Restore(context.Background(), db, b) == nil {
		t.Fatal("production target accepted")
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestObjectSealChangesIdentityAndRejectsTampering(t *testing.T) {
	dir := t.TempDir()
	b := Bundle{Dir: dir, Manifest: Manifest{Format: 1, Source: Source{DBName: "source"}}}
	if e := writeManifest(&b); e != nil {
		t.Fatal(e)
	}
	old := b.ID
	f, e := AddObject(dir, strings.NewReader("opaque\x00bytes"))
	if e != nil {
		t.Fatal(e)
	}
	b.Manifest.Objects = append(b.Manifest.Objects, f)
	b, e = Seal(b)
	if e != nil {
		t.Fatal(e)
	}
	if b.ID == old {
		t.Fatal("object not bound to identity")
	}
	if _, e = Load(dir); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(dir, f.Path), []byte("changed"), 0600)
	if _, e = Load(dir); e == nil {
		t.Fatal("object tamper accepted")
	}
}
func TestRestoreTamperFailsBeforeTargetConnection(t *testing.T) {
	dir := t.TempDir()
	b := Bundle{Dir: dir, Manifest: Manifest{Format: 1, Source: Source{DBName: "source"}}}
	if e := writeManifest(&b); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(dir, "manifest.sha256"), []byte("bad"), 0600)
	db, m, _ := sqlmock.New()
	defer db.Close()
	if Restore(context.Background(), db, b) == nil {
		t.Fatal("tamper accepted")
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestDDLRejectsCreateSelect(t *testing.T) {
	if validateDDL("CREATE TABLE `items` (`id` int) AS SELECT * FROM secret", "items") == nil {
		t.Fatal("CREATE SELECT accepted")
	}
}

func TestRestoreRejectsForeignReceipt(t *testing.T) {
	dir := t.TempDir()
	b := Bundle{Dir: dir, Manifest: Manifest{Format: 1, Source: Source{DBName: "source"}}}
	if e := writeManifest(&b); e != nil {
		t.Fatal(e)
	}
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectExec("SET SESSION time_zone = ").WithArgs("+00:00").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow("octo_marketplace_rehearsal_test", "target"))
	m.ExpectQuery("SELECT VERSION").WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow("8.0.44"))
	m.ExpectQuery("SELECT GET_LOCK").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	m.ExpectQuery("SELECT TABLE_NAME").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow(receiptTable))
	m.ExpectQuery("SELECT table_name, snapshot_id").WillReturnRows(sqlmock.NewRows([]string{"table", "snapshot", "schema", "rows", "complete"}).AddRow("", "foreign", "", "", true))
	m.ExpectExec("SELECT RELEASE_LOCK").WillReturnResult(sqlmock.NewResult(0, 0))
	if Restore(context.Background(), db, b) == nil {
		t.Fatal("foreign receipt accepted")
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestRestoreResumesOwnedEmptyBundle(t *testing.T) {
	dir := t.TempDir()
	b := Bundle{Dir: dir, Manifest: Manifest{Format: 1, Source: Source{DBName: "source"}}}
	if e := writeManifest(&b); e != nil {
		t.Fatal(e)
	}
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectExec("SET SESSION time_zone = ").WithArgs("+00:00").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow("octo_marketplace_rehearsal_test", "target"))
	m.ExpectQuery("SELECT VERSION").WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow("8.0.44"))
	m.ExpectQuery("SELECT GET_LOCK").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	m.ExpectQuery("SELECT TABLE_NAME").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow(receiptTable))
	m.ExpectQuery("SELECT table_name, snapshot_id").WillReturnRows(sqlmock.NewRows([]string{"table", "snapshot", "schema", "rows", "complete"}).AddRow("", b.ID, "", "", true))
	m.ExpectExec("SET SESSION sql_mode = ").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectExec("SET SESSION FOREIGN_KEY_CHECKS = 0").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery("SELECT TABLE_NAME, CONSTRAINT_NAME").WillReturnRows(sqlmock.NewRows([]string{"table", "constraint", "column", "parent", "ref"}))
	m.ExpectExec("SET SESSION FOREIGN_KEY_CHECKS = 1").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectExec("SET SESSION FOREIGN_KEY_CHECKS = 1").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectExec("SELECT RELEASE_LOCK").WillReturnResult(sqlmock.NewResult(0, 0))
	if e := Restore(context.Background(), db, b); e != nil {
		t.Fatal(e)
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestDDLAllowsLegacyTimestampOnUpdate(t *testing.T) {
	ddl := "CREATE TABLE `skills` (`id` varchar(64) NOT NULL, `updated_at` timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3), PRIMARY KEY (`id`)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	if e := validateDDL(ddl, "skills"); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{"CREATE TABLE `skills` (`id` int) UPDATE other SET id=1", "CREATE TABLE `skills` (`id` int ON UPDATE secret) ENGINE=InnoDB"} {
		if validateDDL(bad, "skills") == nil {
			t.Fatal("unsafe UPDATE accepted")
		}
	}
}

func TestRestoreCreatesIdentityAtomicallyAndRetriesInterruptedCreation(t *testing.T) {
	dir := t.TempDir()
	b := Bundle{Dir: dir, Manifest: Manifest{Format: 1, Source: Source{DBName: "source", ServerUUID: "source-server"}}}
	if e := writeManifest(&b); e != nil {
		t.Fatal(e)
	}
	for _, interrupted := range []bool{true, false} {
		t.Run(fmt.Sprintf("interrupted_%t", interrupted), func(t *testing.T) {
			db, m, _ := sqlmock.New()
			defer db.Close()
			m.ExpectExec("SET SESSION time_zone = ").WithArgs("+00:00").WillReturnResult(sqlmock.NewResult(0, 0))
			m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow("octo_marketplace_rehearsal_atomic", "target"))
			m.ExpectQuery("SELECT VERSION").WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow("8.0.44"))
			m.ExpectQuery("SELECT GET_LOCK").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
			m.ExpectQuery("SELECT TABLE_NAME").WillReturnRows(sqlmock.NewRows([]string{"name"}))
			create := m.ExpectExec("CREATE TABLE .* AS SELECT '' AS table_name, \\? AS snapshot_id, '' AS schema_digest, '' AS rows_digest, 1 AS complete").WithArgs(b.ID)
			if interrupted {
				create.WillReturnError(errors.New("simulated interrupted atomic DDL"))
			} else {
				create.WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectQuery("SELECT table_name, snapshot_id").WillReturnRows(sqlmock.NewRows([]string{"table", "snapshot", "schema", "rows", "complete"}).AddRow("", b.ID, "", "", true))
				m.ExpectExec("SET SESSION sql_mode = ").WillReturnResult(sqlmock.NewResult(0, 0))
				m.ExpectExec("SET SESSION FOREIGN_KEY_CHECKS = 0").WillReturnResult(sqlmock.NewResult(0, 0))
				m.ExpectQuery("SELECT TABLE_NAME, CONSTRAINT_NAME").WillReturnRows(sqlmock.NewRows([]string{"table", "constraint", "column", "parent", "ref"}))
				m.ExpectExec("SET SESSION FOREIGN_KEY_CHECKS = 1").WillReturnResult(sqlmock.NewResult(0, 0))
				m.ExpectExec("SET SESSION FOREIGN_KEY_CHECKS = 1").WillReturnResult(sqlmock.NewResult(0, 0))
			}
			m.ExpectExec("SELECT RELEASE_LOCK").WillReturnResult(sqlmock.NewResult(0, 0))
			e := Restore(context.Background(), db, b)
			if (e != nil) != interrupted {
				t.Fatalf("restore interrupted=%t error=%v", interrupted, e)
			}
			if e := m.ExpectationsWereMet(); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestAtomicRestoreRequiresMySQL8021(t *testing.T) {
	for _, version := range []string{"5.7.44", "8.0.20", "10.11.6-MariaDB", "unknown"} {
		if supportsAtomicIdentity(version) {
			t.Fatalf("unsafe version accepted: %s", version)
		}
	}
	for _, version := range []string{"8.0.21", "8.0.44-log", "8.4.0", "9.0.1"} {
		if !supportsAtomicIdentity(version) {
			t.Fatalf("supported version rejected: %s", version)
		}
	}
}

func TestExportFailsClosedWhenUTCSessionCannotBeSet(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectExec("SET SESSION time_zone = ").WithArgs("+00:00").WillReturnError(errors.New("session rejected"))
	m.ExpectClose()
	if _, e := Export(context.Background(), db, filepath.Join(t.TempDir(), "archive"), "source"); e == nil {
		t.Fatal("snapshot without UTC accepted")
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestRestoreFailsClosedWhenUTCSessionCannotBeSet(t *testing.T) {
	b := Bundle{Dir: t.TempDir(), Manifest: Manifest{Format: 1, Source: Source{DBName: "source"}}}
	if e := writeManifest(&b); e != nil {
		t.Fatal(e)
	}
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectExec("SET SESSION time_zone = ").WithArgs("+00:00").WillReturnError(errors.New("session rejected"))
	m.ExpectClose()
	if e := Restore(context.Background(), db, b); e == nil {
		t.Fatal("restore without UTC accepted")
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
