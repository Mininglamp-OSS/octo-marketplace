package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// PluginDraftRevision is an opaque optimistic-concurrency token, not an access
// credential. CurrentVersionID identifies immutable content/relations/notes;
// the remaining persisted fields cover metadata and audience changes that do
// not create a version. Exclude derived URLs, metrics and review projections.
func PluginDraftRevision(p *Plugin) string {
	state := struct {
		ID, Name, Publisher, Icon                             string
		Type                                                  PluginType
		CategoryID, SpaceID, CurrentVersionID, CurrentVersion *string
		OwnerUID                                              string
		Visibility                                            PluginVisibility
		ListingState                                          PluginListingState
		ManifestHash, PluginHash, Tags, AttachmentKeys        string
		Status                                                int
	}{p.ID, p.Name, p.Publisher, p.Icon, p.Type, p.CategoryID, p.SpaceID,
		p.CurrentVersionID, p.CurrentVersion, p.OwnerUID, p.Visibility, p.ListingState,
		p.ManifestHash, p.PluginHash, draftRevisionJSON(p.Tags), draftRevisionJSON(p.AttachmentKeys), p.Status}
	raw, _ := json.Marshal(state) // Only strings, integers and pointers to strings.
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// MySQL JSON formatting differs from request JSON; revisions compare values.
func draftRevisionJSON(raw []byte) string {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	canonical, _ := json.Marshal(value)
	return string(canonical)
}
