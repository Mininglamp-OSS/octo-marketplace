package plugin

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// writeConnectorEffective requires the caller's plugin row lock. It never
// changes editable content, and never creates a plugin version or release.
func writeConnectorEffective(ctx context.Context, tx *sql.Tx, p model.Plugin, actor string, now interface{}) error {
	if p.Type != model.PluginTypeConnector {
		return ErrInvalidRelation
	}
	digest := model.EffectiveDigest(p)
	_, err := tx.ExecContext(ctx, `INSERT INTO plugin_connector_effective
 (plugin_id,effective_revision,effective_digest,plugin_name,tags_json,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version,visibility,updated_by,updated_at)
 VALUES (?,1,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON DUPLICATE KEY UPDATE effective_revision=IF(effective_digest=VALUES(effective_digest),effective_revision,effective_revision+1),effective_digest=VALUES(effective_digest),plugin_name=VALUES(plugin_name),tags_json=VALUES(tags_json),tool_count=VALUES(tool_count),manifest_json=VALUES(manifest_json),plugin_json=VALUES(plugin_json),attachment_keys_json=VALUES(attachment_keys_json),manifest_hash=VALUES(manifest_hash),plugin_hash=VALUES(plugin_hash),current_version=VALUES(current_version),visibility=VALUES(visibility),updated_by=VALUES(updated_by),updated_at=VALUES(updated_at)`,
		p.ID, digest, p.Name, string(p.Tags), p.ToolCount, string(p.Manifest), string(p.Package), jsonColumn(p.AttachmentKeys), p.ManifestHash, p.PluginHash, p.CurrentVersion, p.Visibility, actor, now)
	return err
}

// GetConnectorEffective retains current access/listing metadata but replaces
// mutable content with the last effective payload. Missing evidence fails closed.
func (r *Repo) GetConnectorEffective(ctx context.Context, sc Scope, id string) (*model.Plugin, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	where, args := visibilitySQL, []any{id, sc.SpaceID, sc.CallerUID}
	if sc.Admin {
		where = "1=1"
		args = []any{id}
	}
	p, err := scanPlugin(tx.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM plugins p WHERE p.plugin_id=? AND p.plugin_type='connector' AND p.status=1 AND p.deleted_at IS NULL AND `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = overlayConnectorEffective(ctx, tx, p); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}

func overlayConnectorEffective(ctx context.Context, tx *sql.Tx, p *model.Plugin) error {
	var manifest, pkg, tags, keys []byte
	var version sql.NullString
	var revision uint64
	var digest, visibility string
	err := tx.QueryRowContext(ctx, `SELECT effective_revision,effective_digest,plugin_name,tags_json,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version,visibility FROM plugin_connector_effective WHERE plugin_id=?`, p.ID).Scan(&revision, &digest, &p.Name, &tags, &p.ToolCount, &manifest, &pkg, &keys, &p.ManifestHash, &p.PluginHash, &version, &visibility)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrVersionBaselineUnavailable
	}
	if err != nil {
		return err
	}
	p.EffectiveRevision = &revision
	p.EffectiveDigest = &digest
	p.Manifest = cloneJSON(manifest)
	p.Package = cloneJSON(pkg)
	p.Tags = cloneJSON(tags)
	p.AttachmentKeys = cloneJSON(keys)
	p.CurrentVersion = nullString(version)
	p.CurrentVersionID = nil
	// The effective row owns the published audience; a later editing
	// visibility change must not leak into an effective projection.
	p.Visibility = model.PluginVisibility(visibility)
	return nil
}

func (r *Repo) approveConnectorReview(ctx context.Context, tx *sql.Tx, scope Scope, p ApproveReviewParams, current *model.Plugin, version string, manifest, pkg, keys []byte, manifestHash, pluginHash string) (*model.Plugin, error) {
	frozen := *current
	frozen.Manifest = cloneJSON(manifest)
	frozen.Package = cloneJSON(pkg)
	frozen.AttachmentKeys = cloneJSON(keys)
	frozen.ManifestHash = manifestHash
	frozen.PluginHash = pluginHash
	frozen.CurrentVersion = &version
	frozen.Name, frozen.Tags = manifestNameAndTags(manifest)
	frozen.ToolCount = model.ConnectorToolCount(pkg)
	frozen.Visibility = model.PluginVisibilitySpace
	frozen.ListingState = model.PluginListingStatePublished
	now := p.Now
	if now.IsZero() {
		now = r.now()
	}
	if err := writeConnectorEffective(ctx, tx, frozen, p.ReviewerUID, now); err != nil {
		return nil, err
	}
	// Approval never overwrites a newer editing draft or its revision.
	if _, err := tx.ExecContext(ctx, `UPDATE plugins SET visibility='space',listing_state='published',updated_at=? WHERE plugin_id=? AND space_id=? AND deleted_at IS NULL`, now, current.ID, scope.SpaceID); err != nil {
		return nil, err
	}
	if err := ensureVisibleDefaultPlacement(ctx, tx, r.id, now, current.ID, current.CategoryID); err != nil {
		return nil, err
	}
	source := p.DecisionSource
	if source == "" {
		source = model.ReviewDecisionSourceWeb
	}
	res, err := tx.ExecContext(ctx, `UPDATE plugin_review_requests SET status='approved',reviewer_uid=?,reviewer_name=?,decision_source=?,reviewed_at=?,updated_at=? WHERE review_id=? AND status='pending'`, p.ReviewerUID, p.ReviewerName, string(source), now, now, p.ReviewID)
	if err != nil {
		return nil, err
	}
	if err = mustAffect(res); err != nil {
		return nil, err
	}
	m := Mutation{OperatorID: p.ReviewerUID, OperatorName: p.ReviewerName, RequestID: p.RequestID}
	if err = insertAudit(ctx, tx, r.id(), now, frozen, "review_approve", m, current.PluginHash, pluginHash); err != nil {
		return nil, err
	}
	if err = overlayConnectorEffective(ctx, tx, &frozen); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &frozen, nil
}
