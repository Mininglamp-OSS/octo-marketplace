package plugin

import (
	"context"
	"database/sql"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Draft saves may retain an already-owned opaque relation after its target
// becomes unreadable. New pins still require normal validation. Publishing
// never uses this exception and validates the complete frozen graph.
func retainedDraftReferences(ctx context.Context, tx *sql.Tx, source model.Plugin, rels []model.PluginRelation, strict bool) ([]model.PluginRelation, error) {
	if source.ListingState == model.PluginListingStateDraft && len(rels) == 0 {
		return []model.PluginRelation{}, nil
	}
	if strict {
		if err := ValidateReferenceMetadata(source.Type, rels); err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT relation_type,target_plugin_id,target_version_id FROM plugin_relations WHERE source_plugin_id=? AND status=1 AND deleted_at IS NULL`, source.ID)
	if err != nil {
		return nil, err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var typ, id string
		var pin sql.NullString
		if err = rows.Scan(&typ, &id, &pin); err != nil {
			rows.Close()
			return nil, err
		}
		existing[typ+"\x00"+id+"\x00"+pin.String] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	remaining := make([]model.PluginRelation, 0, len(rels))
	for _, edge := range rels {
		pin := ""
		if edge.TargetVersionID != nil {
			pin = *edge.TargetVersionID
		}
		if !existing[edge.Type+"\x00"+edge.TargetPluginID+"\x00"+pin] {
			remaining = append(remaining, edge)
		}
	}
	return remaining, nil
}

func validateChangedDraftReferences(ctx context.Context, tx *sql.Tx, source model.Plugin, rels []model.PluginRelation) error {
	for i, edge := range rels {
		target, err := lockIndependentReferenceTarget(ctx, tx, source, source, edge.TargetPluginID)
		if err != nil {
			return err
		}
		if target.ListingState != model.PluginListingStatePublished {
			return referenceFieldError(i, 0, "target_plugin_id", "target_not_published")
		}
		if !validPersistedRelation(edge.Type, source.Type, target.Type) {
			return ErrInvalidRelation
		}
		if err = validateReleasedReference(ctx, tx, source, target, edge, map[string]bool{}, i, 0); err != nil {
			return err
		}
	}
	return nil
}
