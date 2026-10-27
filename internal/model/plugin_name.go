package model

import (
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// PluginNameMaxLength is the maximum length of the display name stored in
// plugins.plugin_name. The normalized key uses the same limit.
const PluginNameMaxLength = 160

var (
	// ErrPluginNameEmpty means the display name is empty after Unicode
	// normalization and trimming.
	ErrPluginNameEmpty = errors.New("plugin name is empty")
	// ErrPluginNameTooLong means the normalized display name exceeds the schema
	// limit.
	ErrPluginNameTooLong = errors.New("plugin name is too long")
)

// NormalizePluginName produces the cross-surface comparison key for a
// marketplace display name. The same function MUST be used by writes, the
// migration preflight and the backfill command; otherwise case/full-width or
// Unicode-equivalent names can still collide in production.
//
// It deliberately does not collapse internal whitespace: the product rule only
// trims leading/trailing spaces, so "a b" and "ab" remain distinct.
func NormalizePluginName(name string) (string, error) {
	// NFKC maps full-width forms to their compatibility equivalents and gives
	// canonically equivalent strings one representation.
	normalized := norm.NFKC.String(name)
	normalized = strings.TrimSpace(normalized)
	// cases.Fold is a Unicode case folding, not just an ASCII lowercase.
	normalized = cases.Fold().String(normalized)
	if normalized == "" {
		return "", ErrPluginNameEmpty
	}
	if utf8.RuneCountInString(normalized) > PluginNameMaxLength {
		return "", ErrPluginNameTooLong
	}
	return normalized, nil
}

// PluginNameReservationKeys returns the unique normalized names a Plugin must
// reserve: its editable draft name and, when version management has released
// one, its published display name. Callers group/report by every key; using only
// the published name would miss a draft collision and vice versa.
func PluginNameReservationKeys(draft, published string) ([]string, error) {
	keys := make([]string, 0, 2)
	seen := map[string]struct{}{}
	add := func(name string) error {
		if strings.TrimSpace(name) == "" {
			return nil
		}
		key, err := NormalizePluginName(name)
		if err != nil {
			return err
		}
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
		return nil
	}
	if err := add(draft); err != nil {
		return nil, err
	}
	if err := add(published); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, ErrPluginNameEmpty
	}
	return keys, nil
}
