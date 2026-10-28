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
		p.ManifestHash, p.PluginHash, string(p.Tags), string(p.AttachmentKeys), p.Status}
	raw, _ := json.Marshal(state) // Only strings, integers and pointers to strings.
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
