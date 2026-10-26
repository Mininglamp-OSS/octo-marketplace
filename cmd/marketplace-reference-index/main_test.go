package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixturePreviewAndInvalidModes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(p, []byte(`{"resources":[{"id":"s","type":"skill"}],"snapshots":[{"plugin_id":"s","version_id":"v","relations":[]}],"releases":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"--fixture", p}, &out); err != nil || !strings.Contains(out.String(), `"complete":true`) {
		t.Fatalf("preview %s: %v", out.String(), err)
	}
	for _, args := range [][]string{{}, {"--fixture", p, "--apply"}, {"--all-spaces", "--version-id", "v"}, {"--unknown"}} {
		out.Reset()
		if err := run(args, &out); err == nil {
			t.Fatalf("accepted unsafe mode: %v", args)
		}
		if out.Len() != 0 {
			t.Fatal("wrote partial output")
		}
	}
	t.Setenv("MARKETPLACE_REFERENCE_INDEX_DSN", "owner:never-print-this@tcp(127.0.0.1:1)/test?multiStatements=true")
	if err := run([]string{"--all-spaces"}, &out); err == nil || strings.Contains(err.Error(), "never-print") {
		t.Fatalf("DSN validation/redaction: %v", err)
	}
}
