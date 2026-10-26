package plugin

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// pinBearingSource reports whether a plugin type can carry version-pinned
// Skill/Expert references. Connectors and skills never do.
func pinBearingSource(t model.PluginType) bool {
	return t == model.PluginTypeExpert || t == model.PluginTypeExpertTeam
}

// missingRelationPins reports whether any Skill/Expert edge still needs an
// authoritative pin. A blank explicit pin is treated exactly like an omitted
// one: the client cannot mint a meaningless value that way.
func missingRelationPins(source model.PluginType, rels []model.PluginRelation) bool {
	if !pinBearingSource(source) {
		return false
	}
	for i := range rels {
		switch rels[i].Type {
		case "expert_skill", "expert_team_expert":
			if rels[i].TargetVersionID == nil || strings.TrimSpace(*rels[i].TargetVersionID) == "" {
				return true
			}
		}
	}
	return false
}

// FreezeRelationPins resolves every Skill/Expert edge of a *published* source
// snapshot to an authoritative release identity.
//
// P model: a draft may omit target_version_id, but a released source snapshot
// may not. For each missing pin this loads the target row under the caller's
// write transaction (status/deleted/embedded/listing/audience are re-checked),
// takes the target's CURRENT formal release (published_version_id) and proves
// that release row still exists. An explicit pin is validated against the same
// target and release and preserved. Connector edges must stay unpinned.
//
// Callers MUST hold the write transaction and MUST have locked the target rows
// (lockReferences / validateReferenceTree) so the resolution and the snapshot
// write are atomic: a target release that lands concurrently either wins before
// this read (and becomes the pin) or waits for this transaction (and cannot
// move a pin that was already frozen). Team→Expert pins freeze the member
// Expert's published version; that Expert snapshot already carries its own
// frozen Skill pins from its own publication.
func FreezeRelationPins(ctx context.Context, tx *sql.Tx, source model.Plugin, rels []model.PluginRelation) error {
	if !pinBearingSource(source.Type) {
		return nil
	}
	for i := range rels {
		edge := &rels[i]
		if edge.Type == "expert_connector" {
			// A connector is resolved by ID + effective payload; a stored version
			// is corruption, never a hint.
			if edge.TargetVersionID != nil {
				return referenceFieldError(i, 0, "target_version_id", "connector_version_forbidden")
			}
			continue
		}
		if edge.Type != "expert_skill" && edge.Type != "expert_team_expert" {
			return ErrInvalidRelation
		}
		var (
			target    model.Plugin
			space     sql.NullString
			published sql.NullString
		)
		err := tx.QueryRowContext(ctx, `SELECT plugin_id,plugin_type,is_embedded,owner_uid,space_id,visibility,listing_state,published_version_id FROM plugins WHERE plugin_id=? AND status=1 AND deleted_at IS NULL FOR UPDATE`, edge.TargetPluginID).Scan(&target.ID, &target.Type, &target.IsEmbedded, &target.OwnerUID, &space, &target.Visibility, &target.ListingState, &published)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		target.SpaceID = nullString(space)
		// The source is the released root here, so its audience is the root
		// audience; nested members were audience-checked by their own publish.
		if target.IsEmbedded || !referenceAudience(source, target) {
			return ErrNotFound
		}
		if target.ListingState != model.PluginListingStatePublished {
			return referenceFieldError(i, 0, "target_plugin_id", "target_not_published")
		}
		if !published.Valid || strings.TrimSpace(published.String) == "" {
			return referenceFieldError(i, 0, "target_plugin_id", "released_version_required")
		}
		pin := strings.TrimSpace(published.String)
		if edge.TargetVersionID != nil && strings.TrimSpace(*edge.TargetVersionID) != "" {
			// Explicit pins are evidence the author selected that release. They are
			// validated and preserved even when the target has since released a
			// newer version.
			pin = strings.TrimSpace(*edge.TargetVersionID)
		}
		var found string
		err = tx.QueryRowContext(ctx, `SELECT version_id FROM plugin_version_releases WHERE plugin_id=? AND version_id=? FOR UPDATE`, target.ID, pin).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return referenceFieldError(i, 0, "target_version_id", "released_version_required")
		}
		if err != nil {
			return err
		}
		targetPin := pin
		edge.TargetVersionID = &targetPin
		edge.TargetPluginType = target.Type
	}
	return nil
}

// freezePublishedPins freezes pins only when this write is itself a publication:
// the admin surface stamps listing_state=published on create and preserves it on
// update, and only then does the same transaction record a formal release.
// Tenant draft saves stay free to omit pins (P model).
func (r *Repo) freezePublishedPins(ctx context.Context, tx *sql.Tx, scope Scope, p model.Plugin, rels []model.PluginRelation) error {
	if !r.references || !r.versionManagement || !scope.Admin || p.ListingState != model.PluginListingStatePublished || !versioned(&p) {
		return nil
	}
	return FreezeRelationPins(ctx, tx, p, rels)
}

// freezePublishedSnapshotForRelease is the publish/adoption path: the snapshot
// selected for release may have been saved as a draft with omitted pins. Since a
// released snapshot is immutable, a changed (frozen) graph is materialized as a
// NEW snapshot row with identical content; the draft row stays untouched. When
// every pin is already present the release keeps pointing at the original row.
func (r *Repo) freezePublishedSnapshotForRelease(ctx context.Context, tx *sql.Tx, newID func() string, current *model.Plugin, versionID *string, operator string) error {
	if !r.references || !versioned(current) || !pinBearingSource(current.Type) {
		return nil
	}
	v, err := scanManagedVersion(tx.QueryRowContext(ctx, `SELECT `+managedVersionColumns+` FROM plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id LEFT JOIN plugin_version_releases rel ON rel.plugin_id=v.plugin_id AND rel.version_id=v.version_id WHERE v.plugin_id=? AND v.version_id=? FOR UPDATE`, current.ID, *versionID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrVersionBaselineUnavailable
		}
		return err
	}
	rels, err := model.DecodeSnapshotRelations(v.Relations)
	if err != nil {
		return ErrInvalidRelation
	}
	if !missingRelationPins(current.Type, rels) {
		// Still validate explicit pins/connectors at the publication boundary; the
		// release transaction re-reads the stored snapshot and rejects drift.
		return FreezeRelationPins(ctx, tx, *current, rels)
	}
	if err = FreezeRelationPins(ctx, tx, *current, rels); err != nil {
		return err
	}
	// The frozen graph must stay attached to exactly the label/content the caller
	// selected; otherwise publication could silently promote a later draft.
	if v.DeclaredVersion == nil {
		return ErrVersionBaselineUnavailable
	}
	frozen := *current
	frozen.Manifest = cloneJSON(v.Manifest)
	frozen.Package = cloneJSON(v.Package)
	frozen.AttachmentKeys = cloneJSON(v.AttachmentKeys)
	frozen.ManifestHash = v.ManifestHash
	frozen.PluginHash = v.PluginHash
	frozen.CurrentVersion = v.DeclaredVersion
	frozenID, err := snapshotVersion(ctx, tx, newID, r.now(), frozen, rels, nil, operator, r.references)
	if err != nil {
		return err
	}
	*versionID = frozenID
	current.CurrentVersionID = &frozenID
	return nil
}
