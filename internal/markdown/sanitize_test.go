package markdown

import (
	"strings"
	"testing"
)

func TestSanitize_RemovesDangerousHTMLAndPreservesMarkdown(t *testing.T) {
	raw := strings.Join([]string{
		"# Demo",
		"",
		`<script>alert("xss")</script>`,
		`<div onclick="evil()">hello</div>`,
		"",
		"```html",
		`<script>keep-as-code()</script>`,
		"```",
		"",
		"<https://example.com>",
	}, "\n")

	got := Sanitize(raw)

	if strings.Contains(got, `<script>alert("xss")</script>`) {
		t.Fatalf("dangerous script tag should be removed, got %q", got)
	}
	if !strings.Contains(got, `&lt;div onclick=&#34;evil()&#34;&gt;hello&lt;/div&gt;`) {
		t.Fatalf("raw html should be escaped, got %q", got)
	}
	if !strings.Contains(got, `<script>keep-as-code()</script>`) {
		t.Fatalf("code fence content should stay untouched, got %q", got)
	}
	if !strings.Contains(got, "<https://example.com>") {
		t.Fatalf("autolink markdown should stay untouched, got %q", got)
	}
}
