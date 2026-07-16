package id

import (
	"testing"
	"time"
)

func TestNewShapeAndAlphabet(t *testing.T) {
	const valid = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		v := New()
		if len(v) != 26 {
			t.Fatalf("length = %d, want 26 (%q)", len(v), v)
		}
		for _, c := range v {
			if !containsRune(valid, c) {
				t.Fatalf("char %q not in Crockford alphabet (%q)", c, v)
			}
		}
		if _, dup := seen[v]; dup {
			t.Fatalf("duplicate id generated: %q", v)
		}
		seen[v] = struct{}{}
	}
}

func TestNewIsTimeSortable(t *testing.T) {
	earlier := newAt(time.UnixMilli(1_000_000))
	later := newAt(time.UnixMilli(2_000_000))
	if earlier >= later {
		t.Fatalf("expected earlier id %q < later id %q", earlier, later)
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
