package plugin

import (
	"encoding/json"
	"errors"
	"strconv"
	"unicode/utf8"
)

// Environment values can contain credentials. Reject null and malformed Unicode
// instead of letting encoding/json silently turn them into empty/replaced text.
// This type is local to the new route; legacy request decoding is unchanged.
type installationEnvValue string

func (v *installationEnvValue) UnmarshalJSON(raw []byte) error {
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	invalid := errors.New("invalid installation environment value")
	if value == nil || !utf8.Valid(raw) {
		return invalid
	}
	// Unmarshal has validated JSON syntax, so every Unicode escape has four
	// hex digits. Check pairs in the original bytes before replacement is lost.
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		r, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		i += 4
		switch {
		case r >= 0xd800 && r <= 0xdbff:
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return invalid
			}
			low, _ := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if low < 0xdc00 || low > 0xdfff {
				return invalid
			}
			i += 6
		case r >= 0xdc00 && r <= 0xdfff:
			return invalid
		}
	}
	*v = installationEnvValue(*value)
	return nil
}
