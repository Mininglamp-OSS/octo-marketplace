package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/artifacts"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/snapshot"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceRowPreservesStateAndNulls(t *testing.T) {
	row := map[string][]byte{"plugin_id": []byte("skill-1"), "plugin_type": []byte("skill"), "plugin_name": []byte("x"), "category_id": []byte("category-a"), "icon": []byte("icon-key"), "visibility": []byte("private"), "owner_uid": []byte("owner"), "space_id": []byte("space-a"), "status": []byte("0"), "is_embedded": []byte("1"), "updated_at": []byte("2026-10-28 12:34:56.123"), "created_at": []byte("2026-10-27 00:00:00"), "manifest_json": []byte(`{}`), "plugin_json": []byte(`{}`), "tags_json": []byte(`[]`), "listing_state": []byte("delisted")}
	r, e := sourceResource(row)
	if e != nil {
		t.Fatal(e)
	}
	if r.Plugin.CategoryID == nil || *r.Plugin.CategoryID != "category-a" || r.Plugin.Icon != "icon-key" || r.Plugin.Status != 0 || !r.Plugin.IsEmbedded || r.Plugin.DeletedAt != nil || r.Plugin.SpaceID == nil || *r.Plugin.SpaceID != "space-a" || r.Plugin.UpdatedAt != time.Date(2026, 10, 28, 12, 34, 56, 123000000, time.UTC) {
		t.Fatalf("incorrect source %v", r.Plugin)
	}
	row["updated_at"] = []byte("not a date")
	if _, e = sourceResource(row); e == nil {
		t.Fatal("invalid timestamp accepted")
	}
}

func TestReplicaPreservesHistoricalStoredMCPConfiguration(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, raw []byte) snapshot.File {
		if e := os.WriteFile(filepath.Join(dir, name), raw, 0600); e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(raw)
		return snapshot.File{Path: name, SHA256: hex.EncodeToString(h[:]), Size: int64(len(raw))}
	}
	clean := []byte(`{"attachments":[{"path":"connector/mcp.json","content_type":"raw","raw_content":"{\"env\":{\"TOKEN\":\"${TEST_TOKEN}\"}}"}]}`)
	historical := []byte(`{"attachments":[{"path":"connector/mcp.json","content_type":"storage","storage_uri":"skills/old/mcp.json"}]}`)
	m := snapshot.Manifest{Format: 1, Source: snapshot.Source{DBName: "source"}}
	for _, v := range []struct {
		name string
		body []byte
	}{{"plugins", clean}, {"plugin_versions", historical}} {
		row, _ := json.Marshal([][]byte{[]byte("old"), v.body})
		row = append(row, '\n')
		m.Tables = append(m.Tables, snapshot.Table{Name: v.name, Columns: []string{"plugin_id", "plugin_json"}, Rows: 1, Schema: write(v.name+".schema.sql", []byte("CREATE TABLE `"+v.name+"` (`plugin_id` text,`plugin_json` JSON) ENGINE=InnoDB")), Data: write(v.name+".rows.jsonl", row)})
	}
	manifest, _ := json.Marshal(m)
	write("manifest.json", manifest)
	hash := sha256.Sum256(manifest)
	os.WriteFile(filepath.Join(dir, "manifest.sha256"), []byte(hex.EncodeToString(hash[:])), 0600)
	b, e := snapshot.Load(dir)
	if e != nil {
		t.Fatal(e)
	}
	b, e = artifacts.Collect(context.Background(), b, func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(`{"env":{"TOKEN":"historical-live-secret","file_url":"https://example.test/input","object_key":"relative/user-config"}}`)), nil
	}, artifacts.DefaultObjectLimits())
	if e != nil {
		t.Fatal(e)
	}
	e = ValidateSnapshotReferences(b)
	if e != nil {
		t.Fatalf("existing MCP configuration blocked: %v", e)
	}
}
