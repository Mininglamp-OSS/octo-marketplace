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

func TestPluginNameReservationKeys(t *testing.T) {
	for _, tc := range []struct {
		name, draft, published string
		want                   []string
		bad                    bool
	}{
		{"draft only", "Alpha", "", []string{"alpha"}, false},
		{"both", "Beta", "Alpha", []string{"beta", "alpha"}, false},
		{"same", "Alpha", " alpha ", []string{"alpha"}, false},
		{"blank draft with published", "   ", "Alpha", nil, true},
		{"blank published with draft", "Alpha", "   ", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PluginNameReservationKeys(tc.draft, tc.published)
			if tc.bad {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil || len(got) != len(tc.want) {
				t.Fatalf("got=%v err=%v want=%v", got, err, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got=%v want=%v", got, tc.want)
				}
			}
		})
	}
}
