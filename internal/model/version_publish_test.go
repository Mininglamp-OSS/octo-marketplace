package model

import "testing"

func TestPublishedVersionMustAdvance(t *testing.T) {
	for _, next := range []string{"1.0.0", "1.0.3", "1.00.3"} {
		if VersionAdvanced("1.0.3", next) {
			t.Fatalf("accepted %s", next)
		}
	}
	if !VersionAdvanced("1.0.3", "1.0.4") || !VersionAdvanced("1.9.0", "1.10.0") {
		t.Fatal("newer release rejected")
	}
	if !VersionNotRegressed("1.0.3", "1.0.3") {
		t.Fatal("same-version draft save rejected")
	}
}
