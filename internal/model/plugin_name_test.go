package model

import "testing"

func TestNormalizePluginName(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
		bad  bool
	}{
		{"case", "  Alpha  ", "alpha", false},
		{"full width", "ＡＬＰＨＡ", "alpha", false},
		{"unicode composed", "Café", "café", false},
		{"unicode decomposed", "Cafe\u0301", "café", false},
		{"internal spaces preserved", "a b", "a b", false},
		{"empty", " \t ", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizePluginName(tc.in)
			if tc.bad {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("NormalizePluginName(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}
