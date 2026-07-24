package skill

import (
	"encoding/json"
	"reflect"
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

func TestNormalizeRawTagsWithLegacyAllowsUnchangedLongTag(t *testing.T) {
	raw, names, err := normalizeRawTagsWithLegacy(
		json.RawMessage(`["productivity","new"]`),
		[]string{"productivity"},
	)
	if err != nil {
		t.Fatalf("normalizeRawTagsWithLegacy() error = %v", err)
	}
	if got, want := string(raw), `["productivity","new"]`; got != want {
		t.Fatalf("raw = %s, want %s", got, want)
	}
	if !reflect.DeepEqual(names, []string{"productivity", "new"}) {
		t.Fatalf("names = %#v", names)
	}
}

func TestNormalizeRawTagsWithLegacyRejectsNewLongTag(t *testing.T) {
	_, _, err := normalizeRawTagsWithLegacy(
		json.RawMessage(`["development"]`),
		[]string{"productivity"},
	)
	if err != ErrInvalidTags {
		t.Fatalf("error = %v, want ErrInvalidTags", err)
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
