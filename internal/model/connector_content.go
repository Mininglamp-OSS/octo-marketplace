package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// ConnectorToolCount derives optional display metadata from frozen content.
// Approval must not copy the tool count from a newer editing revision.
func ConnectorToolCount(pkg json.RawMessage) int {
	var doc struct {
		Attachments []struct {
			Path string `json:"path"`
			Type string `json:"content_type"`
			Raw  string `json:"raw_content"`
		} `json:"attachments"`
	}
	if json.Unmarshal(pkg, &doc) != nil {
		return 0
	}
	for _, a := range doc.Attachments {
		if a.Path == "connector/tools.json" && a.Type == "raw" {
			var tools []json.RawMessage
			if json.Unmarshal([]byte(a.Raw), &tools) != nil {
				return 0
			}
			return len(tools)
		}
	}
	return 0
}

// EffectiveDigest covers the complete effective Connector payload, not only the
// immutable content hashes. Name, tags, tool count, current label and visibility
// are part of the effective review/read contract; changing any of them must
// produce a new revision even if manifest/package bytes are unchanged.
//
// The separator is ASCII Unit Separator. The leading version marker lets a
// future encoding change coexist with existing rows without ambiguity.
func EffectiveDigest(p Plugin) string {
	version := ""
	if p.CurrentVersion != nil {
		version = *p.CurrentVersion
	}
	// JSON columns are normalized by MySQL on write/read. Hashing their raw bytes
	// would make an ordinary write differ from the later read, so the effective
	// digest uses stable scalar identity plus the immutable content hashes. JSON
	// payload changes are already covered by ManifestHash/PluginHash.
	parts := []string{
		"v3",
		p.ManifestHash,
		p.PluginHash,
		p.Name,
		strconv.Itoa(p.ToolCount),
		version,
		string(p.Visibility),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return "sha256:" + hex.EncodeToString(sum[:])
}
