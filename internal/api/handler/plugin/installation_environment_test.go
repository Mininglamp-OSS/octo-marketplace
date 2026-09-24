package plugin

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

func FuzzInstallationEnvValue(f *testing.F) {
	for _, seed := range []string{`null`, `false`, `42`, `{}`, `[]`, `""`, `"\ud800"`, `"\udfff"`, `"\ud800\udc00"`, `"\uDBFF\uDFFF"`, `"\\ud800"`, `"\ufffd"`, "\"\xff\""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		var got installationEnvValue
		if err := json.Unmarshal([]byte(raw), &got); err == nil {
			var standard *string
			if !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &standard) != nil || standard == nil || string(got) != *standard {
				t.Fatal("accepted a non-string or changed the decoded value")
			}
		}
		if utf8.ValidString(raw) {
			// Every valid Unicode string must survive a normal JSON round trip,
			// including literal replacement characters and escaped backslashes.
			encoded, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			if json.Unmarshal(encoded, &got) != nil || string(got) != raw {
				t.Fatal("valid Unicode string did not round trip")
			}
		}
	})
}
