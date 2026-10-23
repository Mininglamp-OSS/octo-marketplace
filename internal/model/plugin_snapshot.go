package model

import (
	"encoding/json"
	"errors"
	"time"
)

// PluginSnapshotSchemaVersion identifies the host's immutable snapshot codec.
const PluginSnapshotSchemaVersion = 1

// PluginVersionArtifact describes original file bytes. Raw bytes live in Package;
// stored bytes live at ObjectKey, which is host-private and never a public DTO.
type PluginVersionArtifact struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	ObjectKey   string `json:"object_key,omitempty"`
	Size        int64  `json:"size"`
	Digest      string `json:"digest"`
}

// PluginVersionState keeps saved and formally published identities separate.
// A nil PublishedVersionID means unknown/unreleased, never "use current".
type PluginVersionState struct {
	CurrentVersionID   *string
	PublishedVersionID *string
}

// PluginVersionRelease is append-only publication metadata. Publication wiring
// is introduced separately; schema availability does not publish old snapshots.
type PluginVersionRelease struct {
	PluginID     string
	VersionID    string
	VersionLabel string
	ReleasedBy   string
	ReleasedAt   time.Time
}

// SnapshotRelation is a stable host-owned format, independent of live-row JSON.
// Fields are deliberately explicit so adding fields to PluginRelation cannot
// silently change stored version or review documents.
type SnapshotRelation struct {
	ID               string          `json:"relation_id"`
	SourcePluginID   string          `json:"source_plugin_id"`
	SourcePluginType PluginType      `json:"source_plugin_type"`
	TargetPluginID   string          `json:"target_plugin_id"`
	TargetPluginType PluginType      `json:"target_plugin_type"`
	Type             string          `json:"relation_type"`
	SortOrder        int             `json:"sort_order"`
	Data             json.RawMessage `json:"data"`
	Status           int             `json:"status"`
	CreatedBy        string          `json:"created_by"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	DeletedAt        *time.Time      `json:"deleted_at"`
}

func EncodeSnapshotRelations(relations []PluginRelation) (json.RawMessage, error) {
	out := make([]SnapshotRelation, 0, len(relations))
	for _, r := range relations {
		out = append(out, SnapshotRelation{r.ID, r.SourcePluginID, r.SourcePluginType, r.TargetPluginID, r.TargetPluginType, r.Type, r.SortOrder, r.Data, r.Status, r.CreatedBy, r.CreatedAt, r.UpdatedAt, r.DeletedAt})
	}
	return json.Marshal(out)
}

// DecodeSnapshotRelations accepts both immutable snake_case documents and old
// Go field names without ever rewriting historical JSON. A mixed document with
// conflicting spellings is refused rather than choosing an ambiguous target.
func DecodeSnapshotRelations(raw json.RawMessage) ([]PluginRelation, error) {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]PluginRelation, 0, len(rows))
	aliases := map[string]string{"ID": "relation_id", "SourcePluginID": "source_plugin_id", "SourcePluginType": "source_plugin_type", "TargetPluginID": "target_plugin_id", "TargetPluginType": "target_plugin_type", "Type": "relation_type", "SortOrder": "sort_order", "Data": "data", "Status": "status", "CreatedBy": "created_by", "CreatedAt": "created_at", "UpdatedAt": "updated_at", "DeletedAt": "deleted_at"}
	for _, row := range rows {
		if row == nil {
			return nil, errors.New("null snapshot relation")
		}
		for old, key := range aliases {
			if v, ok := row[old]; ok {
				if existing, ok := row[key]; ok && string(existing) != string(v) {
					return nil, errors.New("conflicting relation fields")
				}
				row[key] = v
				delete(row, old)
			}
		}
		b, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		var r SnapshotRelation
		if err = json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, PluginRelation{ID: r.ID, SourcePluginID: r.SourcePluginID, SourcePluginType: r.SourcePluginType, TargetPluginID: r.TargetPluginID, TargetPluginType: r.TargetPluginType, Type: r.Type, SortOrder: r.SortOrder, Data: r.Data, Status: r.Status, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, DeletedAt: r.DeletedAt})
	}
	return out, nil
}
