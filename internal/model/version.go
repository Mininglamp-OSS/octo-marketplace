package model

import (
	"strconv"
	"strings"
)

// ParseVersion splits a label into its three ordered parts. ok is false for
// anything the tightened `x.y.z` pattern would reject, including every label
// minted before it was tightened — those are unorderable by construction, not
// merely invalid.
//
// It lives in model (not service) because both the unlocked service pre-check
// and the locked repository re-check must apply the SAME ordering rule; a second
// copy would let the two paths drift and reintroduce the forward-only race the
// locked check exists to close.
func ParseVersion(v string) ([3]uint64, bool) {
	var out [3]uint64
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// VersionNotRegressed reports whether `next` may replace `current`.
//
// The rule is "up or unchanged". Unchanged passes FIRST and unconditionally:
// every save re-sends the stored label, so a plugin carrying a legacy
// unorderable one (v999, 1.0.0lll) has to stay editable — refusing it would make
// the tightened format retroactive and brick those rows.
//
// A current label that cannot be parsed cannot be compared either, so any
// well-formed next is accepted; the format check has already run on it.
func VersionNotRegressed(current, next string) bool {
	cur, nxt := strings.TrimSpace(current), strings.TrimSpace(next)
	if cur == nxt {
		return true
	}
	// A malformed NEXT is refused first, and unconditionally. Checking the current
	// label first would let one legacy value wave another through, which is how a
	// row carrying an unorderable label could keep acquiring new unorderable ones.
	nextParts, ok := ParseVersion(nxt)
	if !ok {
		return false
	}
	// An unorderable CURRENT cannot block anything — see the doc comment.
	currentParts, ok := ParseVersion(cur)
	if !ok {
		return true
	}
	for i := range currentParts {
		if nextParts[i] != currentParts[i] {
			return nextParts[i] > currentParts[i]
		}
	}
	return true
}
