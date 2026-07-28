package service

import (
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Rule 1 (must_be_empty on user-supplied) and rule 2 (public_secret_disallowed
// on secret-shaped shared keys) are both removed as of this revision (doc §5).
// Values are persisted verbatim on any visibility; non-owner blanking in
// detailForCaller (§5.3) is the sole guard.

func TestRedactSecretsPreservesValueOnAllVisibilities(t *testing.T) {
	// Regardless of visibility or user-supplied flag, a value passes through
	// verbatim. The only transform is the legacy sentinel → "" normalization.
	cases := []struct {
		name       string
		visibility model.Visibility
		userSup    []string
	}{
		{"private no user-supplied", model.VisibilityPrivate, nil},
		{"private with user-supplied", model.VisibilityPrivate, []string{"Authorization"}},
		{"public no user-supplied", model.VisibilityPublic, nil},
		{"public with user-supplied", model.VisibilityPublic, []string{"Authorization"}},
		{"system no user-supplied", model.VisibilitySystem, nil},
		{"space no user-supplied", model.VisibilitySpace, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := map[string]string{
				"Authorization": "Bearer real-token",
				"X-Trace":       "web",
			}
			out, leaks := redactSecrets(in, "headers", c.userSup, c.visibility)
			if len(leaks) != 0 {
				t.Fatalf("unexpected leaks: %#v", leaks)
			}
			if out["Authorization"] != "Bearer real-token" {
				t.Fatalf("Authorization altered: %q", out["Authorization"])
			}
			if out["X-Trace"] != "web" {
				t.Fatalf("X-Trace altered: %q", out["X-Trace"])
			}
		})
	}
}

func TestRedactSecretsSentinelNormalizedToEmpty(t *testing.T) {
	// Legacy clients still submit the sentinel for user-supplied keys. Accept
	// and normalize to "" so the DB doesn't accumulate the placeholder literal.
	in := map[string]string{
		"TOKEN":  model.SecretPlaceholderSentinel,
		"secret": "",
		"other":  "plain-value",
	}
	out, leaks := redactSecrets(
		in, "env", []string{"TOKEN", "secret"}, model.VisibilityPrivate,
	)
	if len(leaks) != 0 {
		t.Fatalf("unexpected leaks: %#v", leaks)
	}
	if out["TOKEN"] != "" {
		t.Fatalf("sentinel not normalized: %q", out["TOKEN"])
	}
	if out["secret"] != "" {
		t.Fatalf("empty altered: %q", out["secret"])
	}
	if out["other"] != "plain-value" {
		t.Fatalf("plain value altered: %q", out["other"])
	}
}

func TestRedactSecretsEmptyMapPassesThrough(t *testing.T) {
	// Nil / empty maps pass through untouched. Guarantees the caller doesn't
	// need to nil-check before invoking.
	out, leaks := redactSecrets(nil, "env", nil, model.VisibilityPublic)
	if len(leaks) != 0 || out != nil {
		t.Fatalf("nil should stay nil: out=%#v leaks=%#v", out, leaks)
	}
	out, leaks = redactSecrets(map[string]string{}, "env", nil, model.VisibilityPublic)
	if len(leaks) != 0 || len(out) != 0 {
		t.Fatalf("empty map should stay empty: out=%#v leaks=%#v", out, leaks)
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
