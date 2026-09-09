package notify

import (
	"fmt"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Bounded card text for the plugin-review approval card.
//
// octo-server owns the AdaptiveCard template and enforces its own ceilings
// (title ≤200 runes, description ≤300). We truncate conservatively BELOW those
// so the card never depends on server-side truncation, and we truncate by RUNE
// — a byte cut would slice a CJK character in half and render as U+FFFD.
//
// Everything here is plain text: no markdown and no URLs. The approval template
// body is two TextBlocks with no OpenUrl action, so a link would just be inert
// noise, and marketplace must not hand the template anything that its
// escapeMarkdown pass has to rescue.

const (
	// titlePrefix labels the card. Counted in RUNES below, not bytes.
	titlePrefix = "插件上架申请 · "
	// maxTitleRunes leaves headroom under octo-server's 200-rune title cap.
	maxTitleRunes = 180
	// maxDescRunes leaves headroom under octo-server's 300-rune cap.
	maxDescRunes = 280
	// maxChangelogRunes bounds the applicant-authored tail so it cannot crowd
	// out the type/applicant/version header an approver actually decides on.
	maxChangelogRunes = 120
)

// CardTitle builds approval_card.title for a plugin review request.
func CardTitle(pluginName string) string {
	budget := maxTitleRunes - runeLen(titlePrefix)
	return titlePrefix + trimRunes(oneLine(pluginName), budget)
}

// CardDescription builds approval_card.description for a plugin review request:
//
//	类型:{type} · 申请人:{applicant}
//	版本:{version}({新上架 | 当前 v{currentVersion}})
//	{changelog}
//
// The changelog line is omitted entirely when empty. currentVersion is only
// used for an upgrade; an upgrade with no known current version degrades to
// "新版本" rather than rendering a dangling "当前 v".
func CardDescription(pluginType model.PluginType, applicantName, version, currentVersion string, kind model.ReviewKind, changelog string) string {
	var versionNote string
	switch kind {
	case model.ReviewKindFirst:
		versionNote = "新上架"
	case model.ReviewKindUpgrade:
		if cur := oneLine(currentVersion); cur != "" {
			versionNote = "当前 v" + trimRunes(cur, 40)
		} else {
			versionNote = "新版本"
		}
	default:
		versionNote = oneLine(string(kind))
	}

	head := fmt.Sprintf("类型:%s · 申请人:%s\n版本:%s(%s)",
		pluginTypeLabel(pluginType),
		trimRunes(oneLine(applicantName), 40),
		trimRunes(oneLine(version), 40),
		versionNote,
	)
	if log := trimRunes(oneLine(changelog), maxChangelogRunes); log != "" {
		head += "\n" + log
	}
	return trimRunes(head, maxDescRunes)
}

// pluginTypeLabel maps a plugin type to its Chinese card label, falling back to
// the raw value so an unmapped future type still renders something truthful.
func pluginTypeLabel(t model.PluginType) string {
	switch t {
	case model.PluginTypeSkill:
		return "技能"
	case model.PluginTypeConnector:
		return "连接器"
	case model.PluginTypeExpert:
		return "专家"
	case model.PluginTypeExpertTeam:
		return "专家组"
	}
	return oneLine(string(t))
}

// oneLine flattens any interpolated value to a single trimmed line. Newlines
// are structural in the description layout, so an applicant-authored changelog
// or plugin name containing one could otherwise forge a header row.
func oneLine(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(s)
	return strings.TrimSpace(s)
}

func runeLen(s string) int { return len([]rune(s)) }

// trimRunes caps s at n RUNES (never bytes: a byte cut mangles CJK).
func trimRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
