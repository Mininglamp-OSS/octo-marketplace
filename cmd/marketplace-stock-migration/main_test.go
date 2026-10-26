package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFixturePreviewIsOfflineDeterministicAndUnchanged(t *testing.T) {
	t.Setenv("MARKETPLACE_STOCK_MIGRATION_DSN", "invalid")
	p := filepath.Join(t.TempDir(), "fixture.json")
	original := []byte(`{"resources":[],"versions":[],"releases":[]}`)
	if err := os.WriteFile(p, original, 0400); err != nil {
		t.Fatal(err)
	}
	var first, again bytes.Buffer
	for _, out := range []*bytes.Buffer{&first, &again} {
		if err := run([]string{"--fixture", p}, out); err != nil {
			t.Fatal(err)
		}
	}
	if first.String() != again.String() {
		t.Fatal("nondeterministic preview")
	}
	var report map[string]any
	if json.Unmarshal(first.Bytes(), &report) != nil || report["input_resources"] != float64(0) {
		t.Fatal("invalid report")
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(original, after) {
		t.Fatal("fixture mutated")
	}
}
func TestWriteNeedsExplicitOperationAndFixtureCannotApply(t *testing.T) {
	for _, args := range [][]string{{}, {"--apply", "--rollback"}, {"--fixture", "ignored", "--apply"}, {"--fixture", "ignored", "--rollback"}, {"--fixture", "ignored", "--all-spaces"}} {
		var out bytes.Buffer
		if err := run(args, &out); err == nil || out.Len() != 0 {
			t.Fatalf("unsafe args accepted: %v", args)
		}
	}
}
func TestFixtureRejectsTrailingOrOversizedData(t *testing.T) {
	for _, body := range [][]byte{[]byte(`{} {}`), bytes.Repeat([]byte{' '}, (32<<20)+1)} {
		p := filepath.Join(t.TempDir(), "fixture.json")
		if err := os.WriteFile(p, body, 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := run([]string{"--fixture", p}, &out); err == nil || out.Len() != 0 {
			t.Fatal("invalid fixture accepted")
		}
	}
}
