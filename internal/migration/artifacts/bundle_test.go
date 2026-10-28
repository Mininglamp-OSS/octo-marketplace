package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/snapshot"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBundle(t *testing.T) snapshot.Bundle {
	t.Helper()
	d := t.TempDir()
	schema := []byte("CREATE TABLE `items` (`object_key` text) ENGINE=InnoDB")
	rows, _ := json.Marshal([][]byte{[]byte("skills/test/blob")})
	rows = append(rows, '\n')
	file := func(n string, b []byte) snapshot.File {
		t.Helper()
		if e := os.WriteFile(filepath.Join(d, n), b, 0600); e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(b)
		return snapshot.File{Path: n, SHA256: hex.EncodeToString(h[:]), Size: int64(len(b))}
	}
	m := snapshot.Manifest{Format: 1, Source: snapshot.Source{DBName: "source"}, Tables: []snapshot.Table{{Name: "items", Columns: []string{"object_key"}, Rows: 1, Schema: file("items.schema.sql", schema), Data: file("items.rows.jsonl", rows)}}}
	raw, _ := json.Marshal(m)
	h := sha256.Sum256(raw)
	os.WriteFile(filepath.Join(d, "manifest.json"), raw, 0600)
	os.WriteFile(filepath.Join(d, "manifest.sha256"), []byte(hex.EncodeToString(h[:])), 0600)
	b, e := snapshot.Load(d)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestCollectBindsIndexAndBytesToSnapshot(t *testing.T) {
	b := testBundle(t)
	old := b.ID
	b, e := Collect(context.Background(), b, func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("\x00\xffdata")), nil
	}, ObjectLimits{MaxObjectBytes: 100, MaxTotalBytes: 100})
	if e != nil {
		t.Fatal(e)
	}
	if b.ID == old {
		t.Fatal("object inventory not bound")
	}
	read, e := BundleReader(b)
	if e != nil {
		t.Fatal(e)
	}
	data, e := read(context.Background(), "skills/test/blob")
	if e != nil || string(data) != "\x00\xffdata" {
		t.Fatal("binary mismatch")
	}
	if _, e = read(context.Background(), "other"); e == nil {
		t.Fatal("unknown key accepted")
	}
	target := t.TempDir()
	if e = CopyBundleObjects(context.Background(), b, target); e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(filepath.Join(target, "skills/test/blob"))
	if string(got) != string(data) {
		t.Fatal("copy mismatch")
	}
}
func TestCollectRejectsBudget(t *testing.T) {
	b := testBundle(t)
	_, e := Collect(context.Background(), b, func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("123456")), nil
	}, ObjectLimits{MaxObjectBytes: 5, MaxTotalBytes: 20})
	if e == nil {
		t.Fatal("budget accepted")
	}
}
func TestLocalObjectRejectsSymlinkAndTraversal(t *testing.T) {
	d := t.TempDir()
	os.Symlink("/etc/passwd", filepath.Join(d, "link"))
	for _, k := range []string{"../escape", "/etc/passwd", "link"} {
		if f, e := OpenLocalObject(d, k); e == nil {
			f.Close()
			t.Fatal("unsafe path accepted")
		}
	}
}
