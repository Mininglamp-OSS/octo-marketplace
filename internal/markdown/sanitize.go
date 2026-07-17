package markdown

import (
	"bytes"
	"html"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

type replacement struct {
	start int
	stop  int
	text  string
}

// Sanitize removes dangerous raw HTML from markdown while preserving the rest
// of the markdown source unchanged.
func Sanitize(raw string) string {
	if raw == "" {
		return ""
	}

	source := []byte(raw)
	doc := goldmark.DefaultParser().Parse(text.NewReader(source))
	repls := collectHTMLReplacements(doc, source)
	if len(repls) == 0 {
		return raw
	}

	sort.Slice(repls, func(i, j int) bool {
		if repls[i].start == repls[j].start {
			return repls[i].stop < repls[j].stop
		}
		return repls[i].start < repls[j].start
	})

	var out bytes.Buffer
	last := 0
	for _, repl := range repls {
		if repl.start < last || repl.start >= repl.stop || repl.stop > len(source) {
			continue
		}
		out.Write(source[last:repl.start])
		out.WriteString(repl.text)
		last = repl.stop
	}
	out.Write(source[last:])
	return out.String()
}

func collectHTMLReplacements(doc gast.Node, source []byte) []replacement {
	repls := make([]replacement, 0)
	_ = gast.Walk(doc, func(node gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}

		switch n := node.(type) {
		case *gast.HTMLBlock:
			start, stop, raw := htmlBlockRange(n, source)
			repls = append(repls, replacement{
				start: start,
				stop:  stop,
				text:  sanitizeRawHTML(raw),
			})
		case *gast.RawHTML:
			start, stop, raw := rawHTMLRange(n, source)
			repls = append(repls, replacement{
				start: start,
				stop:  stop,
				text:  sanitizeRawHTML(raw),
			})
		}
		return gast.WalkContinue, nil
	})
	return repls
}

func htmlBlockRange(node *gast.HTMLBlock, source []byte) (int, int, string) {
	lines := node.Lines()
	if lines.Len() == 0 {
		return 0, 0, ""
	}
	start := lines.At(0).Start
	stop := lines.At(lines.Len() - 1).Stop
	if node.HasClosure() && node.ClosureLine.Stop > stop {
		stop = node.ClosureLine.Stop
	}
	return start, stop, string(source[start:stop])
}

func rawHTMLRange(node *gast.RawHTML, source []byte) (int, int, string) {
	if node.Segments.Len() == 0 {
		return 0, 0, ""
	}
	start := node.Segments.At(0).Start
	stop := node.Segments.At(node.Segments.Len() - 1).Stop
	return start, stop, string(source[start:stop])
}

func sanitizeRawHTML(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || isDangerousRawHTML(trimmed) {
		return ""
	}
	return html.EscapeString(raw)
}

func isDangerousRawHTML(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(lower, "<!--") {
		return true
	}
	for _, tag := range []string{
		"script", "style", "iframe", "object", "embed", "meta", "link", "base",
		"form", "input", "button", "select", "option", "textarea",
		"frame", "frameset", "applet",
	} {
		if hasHTMLTag(lower, tag) {
			return true
		}
	}
	return false
}

func hasHTMLTag(lowerRaw, tag string) bool {
	if len(lowerRaw) < len(tag)+1 || lowerRaw[0] != '<' {
		return false
	}
	i := 1
	for i < len(lowerRaw) && (lowerRaw[i] == ' ' || lowerRaw[i] == '\t' || lowerRaw[i] == '\n' || lowerRaw[i] == '\r' || lowerRaw[i] == '/') {
		i++
	}
	if !strings.HasPrefix(lowerRaw[i:], tag) {
		return false
	}
	end := i + len(tag)
	if end >= len(lowerRaw) {
		return true
	}
	switch lowerRaw[end] {
	case ' ', '\t', '\n', '\r', '>', '/':
		return true
	default:
		return false
	}
}
