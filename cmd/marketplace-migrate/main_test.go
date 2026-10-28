package main

import (
	"bytes"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSingleDatabaseArguments(t *testing.T) {
	for _, args := range [][]string{{"--database", "production"}, {"--database", "production", "--apply", "--state-file", "run.state"}, {"--database", "test", "--verify", "--state-file", "run.state"}} {
		if _, e := parseOptions(args); e != nil {
			t.Fatal(e)
		}
	}
	for _, args := range [][]string{{}, {"snapshot", "--dir", "x"}, {"--database", "x", "--apply"}, {"--database", "x", "--apply", "--verify", "--state-file", "x"}, {"--database", "x", "--source-dsn", "x"}} {
		if _, e := parseOptions(args); e == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestSingleDatabaseDSNAndUTC(t *testing.T) {
	c, e := parseDSN("user:secret@tcp(localhost:3306)/production?time_zone=%27%2B08%3A00%27", "production")
	if e != nil {
		t.Fatal(e)
	}
	if !c.ParseTime || c.Loc != time.UTC || c.Params["time_zone"] != "'+00:00'" {
		t.Fatal("UTC not enforced")
	}
	for _, s := range []string{"user@tcp(localhost)/other", "user@tcp(localhost)/production?multiStatements=true", "user@tcp(localhost)/production?allowAllFiles=true"} {
		if _, e := parseDSN(s, "production"); e == nil {
			t.Fatal("unsafe config accepted")
		}
	}
}
func TestRunNeverReadsSourceOrTargetConfiguration(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"--database", "x"}, func(k string) string {
		if strings.HasPrefix(k, "MIGRATION_SOURCE") || strings.HasPrefix(k, "MIGRATION_TARGET") {
			t.Fatal(k)
		}
		return ""
	}, &out)
	if code == 0 || !strings.Contains(out.String(), "unsafe_database_configuration") {
		t.Fatal(out.String())
	}
}
func TestRunStateKeepsPrivateFileBytesAndBindings(t *testing.T) {
	name := filepath.Join(t.TempDir(), "run.state")
	db := migration.DatabaseIdentity{Name: "production", ServerUUID: "server"}
	s := runState{Version: 1, Database: db, StorageID: "store", PolicyID: "policy", Plan: migration.Plan{ID: "plan", SnapshotID: "input", Nodes: []migration.Node{{Files: []migration.File{{Data: []byte{0, 255, 1}}}}}}}
	if e := saveState(name, s); e != nil {
		t.Fatal(e)
	}
	got, e := loadState(name)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got.Plan.Nodes[0].Files[0].Data, []byte{0, 255, 1}) {
		t.Fatal("file bytes lost")
	}
	if validateState(got, db, "store", "policy") != nil {
		t.Fatal("correct binding rejected")
	}
	if validateState(got, db, "other", "policy") == nil || validateState(got, migration.DatabaseIdentity{Name: "other"}, "store", "policy") == nil {
		t.Fatal("wrong binding accepted")
	}
	if saveState(name, s) == nil {
		t.Fatal("overwrote checkpoint")
	}
	info, _ := os.Stat(name)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	b, _ := os.ReadFile(name)
	b[0] ^= 1
	_ = os.WriteFile(name, b, 0600)
	if _, e = loadState(name); e == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
}
func TestStateRejectsSymlinkAndBroadPermissions(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "run")
	if e := saveState(name, runState{}); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link")
	if e := os.Symlink(name, link); e != nil {
		t.Fatal(e)
	}
	if _, e := loadState(link); e == nil {
		t.Fatal("symlink accepted")
	}
	_ = os.Chmod(name, 0644)
	if _, e := loadState(name); e == nil {
		t.Fatal("public file accepted")
	}
}
