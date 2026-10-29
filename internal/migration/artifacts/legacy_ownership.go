package artifacts

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// legacyID is the original backfill's UUIDv8 mapping, not a new migration ID.
func legacyID(family, source string) string {
	h := sha256.Sum256([]byte(family + "\x00" + source))
	h[6], h[8] = (h[6]&15)|0x80, (h[8]&63)|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// Legacy directories are globally keyed by record identity. A directory name
// alone is not authority: bind it to this row's identity or its original
// deterministic backfill identity. Unknown provenance is refused before reads.
func legacyKeyOwned(key string, p *model.Plugin) bool {
	if p == nil || p.ID == "" {
		return false
	}
	parts := strings.Split(key, "/")
	if len(parts) < 3 || parts[1] == "" {
		return false
	}
	family := ""
	switch parts[0] {
	case "skills":
		if p.Type == model.PluginTypeSkill {
			family = "skill"
		}
	case "experts":
		if p.Type == model.PluginTypeExpert && parts[2] != "skills" {
			family = "expert"
		}
	case "squads":
		if p.Type == model.PluginTypeExpertTeam && parts[2] != "members" {
			family = "expertteam"
		}
	}
	if family != "" && (p.ID == parts[1] || p.ID == legacyID(family, parts[1])) {
		return true
	}
	if p.Type != model.PluginTypeSkill {
		return false
	}

	// Backfilled embedded skills have their own UUID, derived from the legacy
	// parent and skill identity. Prove the exact skill directory, not merely the
	// expert/team directory which is shared with sibling skills.
	var parent, skillKey, prefix string
	switch {
	case len(parts) >= 5 && parts[0] == "experts" && parts[2] == "skills":
		parent, skillKey = legacyID("expert", parts[1]), parts[3]
		prefix = strings.Join(parts[:4], "/") + "/"
	case len(parts) >= 7 && parts[0] == "squads" && parts[2] == "members" && parts[4] == "skills":
		if parts[3] == "" {
			return false
		}
		team := legacyID("expertteam", parts[1])
		// Duplicate member keys cannot be represented by the new team contract.
		parent = legacyID("snapshotmember", team+":member_key:"+parts[3]+":000000")
		skillKey, prefix = parts[5], strings.Join(parts[:6], "/")+"/"
	default:
		return false
	}
	if skillKey == "" {
		return false
	}
	var doc struct {
		Attachments []attachment `json:"attachments"`
	}
	if json.Unmarshal(p.Package, &doc) != nil {
		return false
	}
	for _, a := range doc.Attachments {
		if a.Path != "skill/ref.json" || a.Type != "raw" {
			continue
		}
		var r struct {
			Object string `json:"object_key"`
			Zip    string `json:"zip_object_key"`
			URL    string `json:"file_url"`
		}
		if model.ValidateUniqueJSONFields([]byte(a.Raw)) != nil || json.Unmarshal([]byte(a.Raw), &r) != nil {
			return false
		}
		for _, ref := range []string{r.Object, r.Zip, r.URL} {
			if ref != "" && (!validKey(ref) || !strings.HasPrefix(ref, prefix)) {
				return false
			}
		}
		identities := []string{"skill_key:" + skillKey}
		if r.Object != "" {
			identities = append(identities, "object_key:"+r.Object)
		}
		if r.Zip != "" {
			identities = append(identities, "zip_object_key:"+r.Zip)
		}
		for _, identity := range identities {
			// The original source distinguishes repeated occurrences. Bound the
			// search by the converter's resource budget instead of accepting an
			// unverified caller-provided occurrence or parent ID.
			for occurrence := 0; occurrence < 4096; occurrence++ {
				if p.ID == legacyID("snapshotskill", fmt.Sprintf("%s:%s:%06d", parent, identity, occurrence)) {
					return true
				}
			}
		}
	}
	return false
}
