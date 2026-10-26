// Package jcs implements the JSON Canonicalization Scheme (RFC 8785) for the
// subset of JSON values used by migration plans and authorization payloads.
//
// The package deliberately supports only data that encoding/json can decode
// into interface values: objects with string keys, arrays, strings, booleans,
// null, and JSON numbers. Non-finite and out-of-range numbers are rejected
// rather than silently rounded, because a canonical digest must not hide a
// corrupted plan.
package jcs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrUnsupported reports a value that cannot be represented in canonical JSON.
var ErrUnsupported = errors.New("jcs: unsupported value")

// Canonicalize returns the RFC 8785 canonical UTF-8 encoding of v. Structs and
// maps are first marshalled with encoding/json, then decoded with UseNumber so
// number spelling from the source object is not lost.
func Canonicalize(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return CanonicalizeJSON(raw)
}

// CanonicalizeJSON canonicalizes one JSON document. It rejects trailing data
// and uses json.Number so integers do not pass through float64.
func CanonicalizeJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := appendValue(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Digest returns the lowercase hex SHA-256 of the canonical encoding.
func Digest(v any) (string, error) {
	canonical, err := Canonicalize(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// DigestJSON is Digest for an already encoded JSON document.
func DigestJSON(raw []byte) (string, error) {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("%w: trailing JSON value", ErrUnsupported)
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func appendValue(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if v {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case string:
		return appendString(out, v)
	case json.Number:
		return appendNumber(out, v.String())
	case float64:
		return appendNumber(out, strconv.FormatFloat(v, 'g', -1, 64))
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := appendValue(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			if !utf8.ValidString(key) {
				return fmt.Errorf("%w: invalid object key", ErrUnsupported)
			}
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := appendString(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := appendValue(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("%w: %T", ErrUnsupported, value)
	}
	return nil
}

// lessUTF16 implements RFC 8785 §3.2.3 property ordering: unsigned UTF-16
// code-unit order, not Go's UTF-8 byte order. Supplementary characters sort
// after U+E000 because their high surrogate is 0xD800-range.
func lessUTF16(a, b string) bool {
	au, bu := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(au) && i < len(bu); i++ {
		if au[i] != bu[i] {
			return au[i] < bu[i]
		}
	}
	return len(au) < len(bu)
}

func appendString(out *bytes.Buffer, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: invalid UTF-8 string", ErrUnsupported)
	}
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
				continue
			}
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return nil
}

func appendNumber(out *bytes.Buffer, literal string) error {
	if literal == "" {
		return fmt.Errorf("%w: empty number", ErrUnsupported)
	}
	// Integer spelling keeps arbitrary uint64 values exact and matches the
	// ECMAScript serialization already emitted by encoding/json.
	if !strings.ContainsAny(literal, ".eE") {
		if literal == "-0" {
			out.WriteString("0")
			return nil
		}
		if _, err := strconv.ParseUint(literal, 10, 64); err == nil {
			out.WriteString(literal)
			return nil
		}
		if _, err := strconv.ParseInt(literal, 10, 64); err == nil {
			out.WriteString(literal)
			return nil
		}
		return fmt.Errorf("%w: integer %q out of range", ErrUnsupported, literal)
	}
	f, err := strconv.ParseFloat(literal, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return fmt.Errorf("%w: number %q", ErrUnsupported, literal)
	}
	if f == 0 {
		out.WriteString("0")
		return nil
	}
	// ECMAScript Number::toString uses fixed notation for 1e-6 <= |n| < 1e21
	// and exponent notation outside that range. Ruby-style RFC 8785 vectors use
	// lowercase "e" with no zero padding.
	abs := math.Abs(f)
	var formatted string
	if abs >= 1e-6 && abs < 1e21 {
		formatted = strconv.FormatFloat(f, 'f', -1, 64)
	} else {
		formatted = strconv.FormatFloat(f, 'e', -1, 64)
		formatted = normalizeExponent(formatted)
	}
	out.WriteString(formatted)
	return nil
}

func normalizeExponent(value string) string {
	index := strings.IndexByte(value, 'e')
	if index < 0 {
		return value
	}
	mantissa, exponent := value[:index], value[index+1:]
	sign := ""
	if strings.HasPrefix(exponent, "+") || strings.HasPrefix(exponent, "-") {
		sign, exponent = exponent[:1], exponent[1:]
	}
	exponent = strings.TrimLeft(exponent, "0")
	if exponent == "" {
		exponent = "0"
	}
	return mantissa + "e" + sign + exponent
}
