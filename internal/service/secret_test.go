package service

import (
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestIsSecretKey(t *testing.T) {
	secret := []string{
		"Authorization", "authorization", "token", "TOKEN",
		"GITHUB_TOKEN", "access_token", "api_key", "API-KEY", "apikey",
		"my_secret", "password", "PWD", "openai_key",
	}
	for _, k := range secret {
		if !isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = false, want true", k)
		}
	}
	notSecret := []string{
		"region", "url", "X-Trace", "serverName", "keyboard_layout",
		// "keyboard_layout" ends in "layout" not "key"; ensure the anchored
		// pattern does not over-match a word merely containing "key".
		"donkey_count",
	}
	for _, k := range notSecret {
		if isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = true, want false", k)
		}
	}
}

func TestRedactSecrets(t *testing.T) {
	in := map[string]string{
		"TOKEN":  model.SecretPlaceholderSentinel,
		"API":    "", // not a secret key, empty is fine
		"secret": "",
		"region": "eu-west-1",
	}
	out, leaks := redactSecrets(in, "env")
	if len(leaks) != 0 {
		t.Fatalf("unexpected leaks: %#v", leaks)
	}
	if out["TOKEN"] != "" {
		t.Fatalf("TOKEN not blanked: %q", out["TOKEN"])
	}
	if out["region"] != "eu-west-1" {
		t.Fatalf("region altered: %q", out["region"])
	}
}

func TestRedactSecretsRejectsPlaintext(t *testing.T) {
	in := map[string]string{"my_secret": "hunter2"}
	_, leaks := redactSecrets(in, "headers")
	if len(leaks) != 1 || leaks[0].Field != "headers.my_secret" || leaks[0].Reason != "non_empty" {
		t.Fatalf("expected one leak headers.my_secret/non_empty, got %#v", leaks)
	}
}

func TestNormalizeHelpers(t *testing.T) {
	tags := normalizeTags([]string{" a ", "a", "b", "", "  "})
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("normalizeTags = %#v", tags)
	}
	list := normalizeStringList([]string{"x", " ", "y"})
	if len(list) != 2 {
		t.Fatalf("normalizeStringList = %#v", list)
	}
	faqs := normalizeFAQs([]model.FAQ{{Question: "q"}, {Question: "  ", Answer: "a"}})
	if len(faqs) != 1 || faqs[0].Question != "q" {
		t.Fatalf("normalizeFAQs = %#v", faqs)
	}
}

func TestNormalizeAuthTypeDefaultsNone(t *testing.T) {
	if got := normalizeAuthType(""); got != "none" {
		t.Fatalf("normalizeAuthType(\"\") = %q, want none", got)
	}
	if got := normalizeAuthType("bearer"); got != "bearer" {
		t.Fatalf("normalizeAuthType(bearer) = %q", got)
	}
}
