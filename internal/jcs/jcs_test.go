package jcs

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCanonicalizeSortsKeysAndPreservesValueBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"key-order", `{"b":2,"a":1}`, `{"a":1,"b":2}`},
		{"nested", `{"z":["b",{"d":4,"c":3}],"a":null}`, `{"a":null,"z":["b",{"c":3,"d":4}]}`},
		{"utf8-bytes", `{"é":"é","a":"a"}`, `{"a":"a","é":"é"}`},
		{"no-html-escape", `{"x":"<>&"}`, `{"x":"<>&"}`},
		{"control", `{"x":"\u000f\n\t"}`, `{"x":"\u000f\n\t"}`},
		{"integer-zero", `-0`, `0`},
		{"large-uint", `18446744073709551615`, `18446744073709551615`},
		{"number-shapes", `[333333333.33333329,1E30,4.50,2e-3,0.000000000000000000000000001]`, `[333333333.3333333,1e+30,4.5,0.002,1e-27]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalizeJSON([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("canonicalize(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalizeStructMatchesJSONCanonicalization(t *testing.T) {
	type inner struct {
		B int    `json:"b"`
		A string `json:"a"`
	}
	value := struct {
		Z []inner        `json:"z"`
		M map[string]int `json:"m"`
	}{Z: []inner{{B: 2, A: "<x>"}}, M: map[string]int{"b": 2, "a": 1}}
	got, err := Canonicalize(value)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"m":{"a":1,"b":2},"z":[{"a":"<x>","b":2}]}`
	if string(got) != want {
		t.Fatalf("canonicalize = %s, want %s", got, want)
	}
}

func TestDigestIsStableAndCaseIndependentOfWhitespace(t *testing.T) {
	a, err := DigestJSON([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := DigestJSON([]byte("{\n  \"a\": 1,\n  \"b\": 2\n}"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a != "43258cff783fe7036d8a43033f830adfc60ec037382473548ac742b888292777" {
		t.Fatalf("digest mismatch a=%s b=%s", a, b)
	}
}

func TestCanonicalizeRejectsTrailingAndUnsupportedValues(t *testing.T) {
	if _, err := CanonicalizeJSON([]byte(`{"a":1} {"b":2}`)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("trailing value: %v", err)
	}
	if _, err := CanonicalizeJSON([]byte(`"\xff"`)); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := Canonicalize(json.Number("1e9999")); err == nil || !strings.Contains(err.Error(), "number") {
		t.Fatalf("infinite number: %v", err)
	}
}

func TestCanonicalJSONUTF16PropertyOrder(t *testing.T) {
	// RFC 8785 compares UTF-16 code units, so U+1F600 (surrogate pair 0xD83D
	// 0xDE00) sorts before U+E000 (0xE000), opposite of UTF-8 byte ordering.
	got, err := Canonicalize(map[string]any{"\U0001F600": 1, "\uE000": 2})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"\U0001F600\":1,\"\uE000\":2}"
	if string(got) != want {
		t.Fatalf("canonical=%s want=%s", got, want)
	}
}
