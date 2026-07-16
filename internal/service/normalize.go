package service

import (
	"regexp"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// slugRE enforces the ASCII identifier shape used as the JSON key in generated
// mcpServers snippets (mcp-v1.md §3, "服务标识"). Lowercase letters, digits,
// and hyphens only; 1..64 chars. Frontend runs the same pattern before submit;
// the server re-validates as defense-in-depth (never trust client input).
var slugRE = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// slugifyName derives an ASCII slug from a display name. Mirrors the frontend
// slugifyServerName (octo-web/packages/dmworkmcp/src/utils/constants.ts):
// lower-case, replace non-[a-z0-9] runs with a single hyphen, trim leading /
// trailing hyphens, cap at 64 chars. Returns "" when the input reduces to
// nothing (e.g., all-CJK name); the caller must treat that as slug_required.
func slugifyName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	// Replace any non-slug char with a hyphen, then collapse runs.
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen && b.Len() > 0 {
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 64 {
		out = strings.TrimRight(out[:64], "-")
	}
	return out
}

// normalizeTags trims each tag, drops blanks, and de-duplicates while
// preserving first-seen order (doc §3.1: "entries de-duplicated and trimmed").
func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// normalizeStringList trims and drops empty entries (doc §3.1: usageExamples /
// notes "Empty entries filtered out").
func normalizeStringList(items []string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// normalizeFAQs drops entries with an empty question (doc §3.1: "entries with
// an empty question are filtered out").
func normalizeFAQs(faqs []model.FAQ) []model.FAQ {
	out := make([]model.FAQ, 0, len(faqs))
	for _, f := range faqs {
		if strings.TrimSpace(f.Question) == "" {
			continue
		}
		out = append(out, f)
	}
	return out
}
