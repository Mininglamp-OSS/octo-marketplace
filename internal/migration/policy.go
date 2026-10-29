package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"sort"
	"strings"
)

func normalized(s string) string { return cases.Fold().String(norm.NFKC.String(strings.TrimSpace(s))) }
func sharedActive(p model.Plugin) bool {
	return p.Status == 1 && p.ListingState == model.PluginListingStatePublished && p.DeletedAt == nil
}
func lifecycle(p model.Plugin) string { return fmt.Sprintf("%d:%s", p.Status, p.ListingState) }
func scope(p model.Plugin) string {
	v := model.NormalizeLegacyVisibility(p.Visibility)
	if v == model.PluginVisibilitySystem {
		if sharedActive(p) {
			return "system"
		}
		return "system:owner:" + p.OwnerUID + ":" + lifecycle(p)
	}
	s := ""
	if p.SpaceID != nil {
		s = *p.SpaceID
	}
	if v == model.PluginVisibilityPrivate {
		return "private:" + s + ":" + p.OwnerUID + ":" + lifecycle(p)
	}
	if sharedActive(p) {
		return "space:" + s
	}
	return "space:" + s + ":owner:" + p.OwnerUID + ":" + lifecycle(p)
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func stable(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	h[6] = (h[6] & 15) | 0x80
	h[8] = (h[8] & 63) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
func fingerprint(r Resource) string {
	fs := append([]File(nil), r.Files...)
	sort.Slice(fs, func(i, j int) bool { return fs[i].Path < fs[j].Path })
	parts := []string{string(r.Plugin.Type)}
	var manifest any
	if json.Unmarshal(r.Plugin.Manifest, &manifest) == nil {
		b, _ := json.Marshal(manifest)
		parts = append(parts, string(b))
	} else {
		parts = append(parts, string(r.Plugin.Manifest))
	}
	for _, f := range fs {
		parts = append(parts, f.Path, f.MIME, digest(f.Data))
	}
	if len(fs) == 0 {
		parts = append(parts, string(r.Plugin.Package))
	}
	return digest([]byte(strings.Join(parts, "\x00")))
}
func variant(name string) bool {
	return name == "fullstack-dev" || name == "wechat-article-search" || name == "online-search"
}

// nextLabel leaves immutable historical labels spent, including unpublished saves.
func nextLabel(p model.Plugin, in Input) string {
	highest := [3]uint64{}
	found := false
	valid := true
	include := func(label string) {
		parts, ok := model.ParseVersion(label)
		if !ok {
			return
		}
		for _, part := range strings.Split(strings.TrimSpace(label), ".") {
			if len(part) > 9 || strings.Trim(part, "0123456789") != "" {
				valid = false
				return
			}
		}
		greater := !found
		for i := range parts {
			if parts[i] != highest[i] {
				greater = parts[i] > highest[i]
				break
			}
		}
		if greater {
			highest = parts
		}
		found = true
	}
	if p.CurrentVersion != nil {
		include(*p.CurrentVersion)
	}
	for _, v := range in.Versions {
		if v.PluginID == p.ID {
			include(v.Version)
			if v.DeclaredVersion != nil {
				include(*v.DeclaredVersion)
			}
		}
	}
	for _, r := range in.Releases {
		if r.PluginID == p.ID {
			include(r.VersionLabel)
		}
	}
	if !valid {
		return ""
	}
	if !found {
		return "1.0.0"
	}
	for i := 2; i >= 0; i-- {
		if highest[i] == 999999999 {
			highest[i] = 0
			continue
		}
		highest[i]++
		if highest == [3]uint64{999999999, 999999999, 999999999} {
			return ""
		}
		return fmt.Sprintf("%d.%d.%d", highest[0], highest[1], highest[2])
	}
	return ""
}

// snapshotMatches compares immutable content, not row IDs, actors or timestamps.
// Attachment locations participate: equal bodies backed by different object
// identities must never silently replace a previously published snapshot.
func snapshotMatches(r Resource, v model.PluginVersion) bool {
	if !sameJSON(r.Plugin.Manifest, v.Manifest) || !sameJSON(r.Plugin.Package, v.Package) || !sameJSONMap(r.Plugin.AttachmentKeys, v.AttachmentKeys) {
		return false
	}
	old, err := snapshotRelations(v.Relations)
	if err != nil {
		return false
	}
	return relationContent(r.Relations) == relationContent(old)
}
func sameJSON(a, b json.RawMessage) bool {
	if model.ValidateUniqueJSONFields(a) != nil || model.ValidateUniqueJSONFields(b) != nil {
		return false
	}
	var aa, bb any
	if json.Unmarshal(a, &aa) != nil || json.Unmarshal(b, &bb) != nil {
		return false
	}
	x, _ := json.Marshal(aa)
	y, _ := json.Marshal(bb)
	return string(x) == string(y)
}
func sameJSONMap(a, b json.RawMessage) bool {
	normalize := func(v json.RawMessage) json.RawMessage {
		if len(v) == 0 || string(v) == "null" {
			return json.RawMessage(`{}`)
		}
		return v
	}
	return sameJSON(normalize(a), normalize(b))
}
func snapshotRelations(raw json.RawMessage) ([]model.PluginRelation, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	return model.DecodeSnapshotRelations(raw)
}
func relationContent(rels []model.PluginRelation) string {
	rows := []string{}
	for _, r := range rels {
		if r.DeletedAt != nil || r.Status != 1 {
			continue
		}
		var data any
		if len(r.Data) > 0 && string(r.Data) != "null" {
			if json.Unmarshal(r.Data, &data) != nil {
				return "invalid:" + string(r.Data)
			}
		}
		if data == nil {
			data = map[string]any{}
		}
		pin := ""
		if r.TargetVersionID != nil {
			pin = *r.TargetVersionID
		}
		raw, _ := json.Marshal([]any{r.Type, r.TargetPluginID, r.TargetPluginType, pin, r.SortOrder, data, r.Status})
		rows = append(rows, string(raw))
	}
	sort.Strings(rows)
	out, _ := json.Marshal(rows)
	return string(out)
}
