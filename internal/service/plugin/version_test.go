package plugin

import "testing"

// The format is three numeric parts and nothing else. The old pattern accepted
// any identifier-ish string, which is how "v999", "1.0.0lll" and "oooo1.0.0"
// reached production — labels no two of which can be ordered against each other,
// making "the version may only go up" unanswerable.
func TestValidVersionAcceptsOnlyThreeNumericParts(t *testing.T) {
	for _, ok := range []string{"1.0.0", "1.0.1", "0.0.0", "10.20.30", "999.999.999"} {
		if !validVersion(ok) {
			t.Errorf("validVersion(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"1", "1.0", "1.0.0.0", "v1.0.0", "1.0.0lll", "oooo1.0.0", "999",
		"1.0.0-rc1", "1.0.0+build", "", " ", "a.b.c", "-1.0.0",
	} {
		if validVersion(bad) {
			t.Errorf("validVersion(%q) = true, want false", bad)
		}
	}
	// A surrounding space is trimmed before matching, so this one IS accepted.
	if !validVersion(" 1.2.3 ") {
		t.Error("a padded but well-formed label should pass after trimming")
	}
}

func TestVersionNotRegressed(t *testing.T) {
	for _, tc := range []struct {
		name          string
		current, next string
		want          bool
	}{
		{"unchanged", "1.2.3", "1.2.3", true},
		{"patch up", "1.2.3", "1.2.4", true},
		{"minor up", "1.2.3", "1.3.0", true},
		{"major up", "1.2.3", "2.0.0", true},
		{"patch down", "1.2.3", "1.2.2", false},
		{"minor down", "1.3.0", "1.2.9", false},
		{"major down", "2.0.0", "1.9.9", false},
		// Ordered numerically, not lexically: "10" > "9".
		{"double digits are not strings", "1.9.0", "1.10.0", true},
		{"and not the other way", "1.10.0", "1.9.0", false},
		// A legacy label cannot be ordered, so it cannot block an edit. Tightening
		// the format retroactively would otherwise brick every row carrying one:
		// each save re-sends the stored label.
		{"legacy current, unchanged", "v999", "v999", true},
		{"legacy current, well-formed next", "1.0.0lll", "2.0.0", true},
		{"legacy current, legacy next", "v999", "oooo1.0.0", false},
		{"well-formed current, malformed next", "1.2.3", "v9", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionNotRegressed(tc.current, tc.next); got != tc.want {
				t.Errorf("versionNotRegressed(%q, %q) = %v, want %v", tc.current, tc.next, got, tc.want)
			}
		})
	}
}
