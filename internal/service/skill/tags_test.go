package skill

import (
	"encoding/json"
	"testing"
)

func TestNormalizeRawTags(t *testing.T) {
	raw, names, err := normalizeRawTags(json.RawMessage(`[" ai ","dev","ai","","dev"]`))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `["ai","dev"]` {
		t.Fatalf("raw = %s", raw)
	}
	if len(names) != 2 || names[0] != "ai" || names[1] != "dev" {
		t.Fatalf("names = %#v", names)
	}
}

func TestNormalizeRawTagsRejectsNonStringArray(t *testing.T) {
	if _, _, err := normalizeRawTags(json.RawMessage(`{"tag":"ai"}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseTagFilters(t *testing.T) {
	got := ParseTagFilters("ai, dev", "ai", " ops ")
	want := []string{"ai", "dev", "ops"}
	if len(got) != len(want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v want %#v", got, want)
		}
	}
}
