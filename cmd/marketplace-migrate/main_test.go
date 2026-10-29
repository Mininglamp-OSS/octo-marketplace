package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
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
	s := runState{Version: 5, Database: db, StorageID: "store", PolicyID: "policy", Plan: migration.Plan{CategorySnapshotCaptured: true, ID: "plan", SnapshotID: "input", Nodes: []migration.Node{{IconFile: &migration.File{Data: []byte{1, 255, 0}}, Files: []migration.File{{Data: []byte{0, 255, 1}}}}}}}
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
	if got.Plan.Nodes[0].IconFile == nil || !bytes.Equal(got.Plan.Nodes[0].IconFile.Data, []byte{1, 255, 0}) {
		t.Fatal("private icon bytes lost")
	}
	for _, v := range []int{1, 2, 3, 4} {
		old := got
		old.Version = v
		if validateState(old, db, "store", "policy") == nil {
			t.Fatal("old icon-incomplete format accepted")
		}
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

func TestOldStateCannotBypassSourceRevalidation(t *testing.T) {
	db := migration.DatabaseIdentity{Name: "test", ServerUUID: "server"}
	for _, version := range []int{1, 2, 3} {
		legacy := runState{Version: version, Database: db, StorageID: "store", PolicyID: "policy", Plan: migration.Plan{CategorySnapshotCaptured: true, ID: "plan", SnapshotID: "input"}}
		if validateState(legacy, db, "store", "policy") == nil {
			t.Fatalf("old checkpoint v%d accepted", version)
		}
	}
}

func TestCheckpointIsDurableObjectInventoryAcrossInterruptedPublication(t *testing.T) {
	source := migration.Resource{Plugin: model.Plugin{ID: "source", Name: "source", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySystem, ListingState: model.PluginListingStatePublished, Status: 1, UpdatedAt: time.Unix(100, 0), Manifest: json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"source","plugin_type":"skill","name":"source","description":"test","labels":[],"examples":[]}`)}, Files: []migration.File{{Path: "SKILL.md", MIME: "text/markdown", Data: []byte("instructions")}, {Path: "image.bin", MIME: "application/octet-stream", Data: []byte{0, 1, 255}}}}
	source.Plugin.Package = json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"instructions"},{"path":"image.bin","content_type":"raw","mime_type":"application/octet-stream","raw_content":"binary"}]}`)
	plan, err := migration.Build(migration.Input{SnapshotID: "snapshot", Resources: []migration.Resource{source}}, migration.Policy{})
	if err != nil {
		t.Fatalf("build: %v %#v", err, plan.Issues)
	}
	stateFile := filepath.Join(t.TempDir(), "run.state")
	if err = saveState(stateFile, runState{Version: 5, Plan: plan}); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewLocal(t.TempDir(), "http://local")
	entries, err := migration.InspectPlanObjects(context.Background(), state.Plan, store)
	if err != nil || len(entries) != 1 || entries[0].Status != "missing" || entries[0].Size != 3 || entries[0].Hash != digest([]byte{0, 1, 255}) {
		t.Fatalf("durable inventory lost: %v %#v", err, entries)
	}
	if err = migration.PublishObjects(context.Background(), state.Plan, store); err != nil {
		t.Fatal(err)
	}
	// Load the original immutable checkpoint again, with no progress flags to go stale.
	state, err = loadState(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	entries, err = migration.InspectPlanObjects(context.Background(), state.Plan, store)
	if err != nil || len(entries) != 1 || entries[0].Status != "verified" {
		t.Fatalf("object reconciliation failed: %v %#v", err, entries)
	}
}
