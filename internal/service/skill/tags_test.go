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

func TestNormalizeRawTagsRejectsTooManyTags(t *testing.T) {
	if _, _, err := normalizeRawTags(json.RawMessage(`["one","two","three","four","five","six","seven","eight","nine","ten","eleven"]`)); err != ErrInvalidTags {
		t.Fatalf("err = %v, want ErrInvalidTags", err)
	}
}

func TestNormalizeRawTagsRejectsLongUnicodeTag(t *testing.T) {
	if _, _, err := normalizeRawTags(json.RawMessage(`["这是超过十个字符的中文标签"]`)); err != ErrInvalidTags {
		t.Fatalf("err = %v, want ErrInvalidTags", err)
	}
}

func TestNormalizeRawTagsAcceptsBoundaryValues(t *testing.T) {
	raw, names, err := normalizeRawTags(json.RawMessage(`["1234567890","two","three","four","five","six","seven","eight","nine","ten"]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != MaxSkillTags || len(raw) == 0 {
		t.Fatalf("raw = %s, names = %#v", raw, names)
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
