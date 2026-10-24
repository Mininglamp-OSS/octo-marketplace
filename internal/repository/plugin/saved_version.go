package plugin

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// savedVersionSelect is the shared projection for the complete saved-snapshot
// read. The tenant variant appends the owner/Space fence as literal SQL so the
// placeholder order and the argument order are declared together and cannot
// drift across branches.
const savedVersionSelect = `SELECT v.version_id,v.plugin_id,p.plugin_type,v.version,v.manifest_json,v.plugin_json,v.attachment_keys_json,v.manifest_hash,v.plugin_hash,v.relations_json,v.changelog,v.created_by,v.created_at,v.declared_version,v.snapshot_schema_version,v.artifacts_json
 FROM plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id
 WHERE v.plugin_id=? AND v.version_id=? AND p.status=1 AND p.deleted_at IS NULL`

const savedVersionTenantSelect = savedVersionSelect + ` AND p.owner_uid=? AND p.space_id=?`

// GetSavedVersion is an internal author/admin read for a complete saved snapshot.
// Public release reads belong to the publication layer; this method must never
// use catalog visibility to expose somebody else's unreviewed historical save.
func (r *Repo) GetSavedVersion(ctx context.Context, scope Scope, pluginID, versionID string) (*model.PluginVersion, error) {
	query, args := savedVersionTenantSelect, []any{pluginID, versionID, scope.CallerUID, scope.SpaceID}
	if scope.Admin {
		query, args = savedVersionSelect, []any{pluginID, versionID}
	}
	var v model.PluginVersion
	var declared, changelog sql.NullString
	var schema sql.NullInt64
	var manifest, pkg, keys, relations, artifacts []byte
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&v.ID, &v.PluginID, &v.PluginType, &v.Version, &manifest, &pkg, &keys, &v.ManifestHash, &v.PluginHash, &relations, &changelog, &v.CreatedBy, &v.CreatedAt, &declared, &schema, &artifacts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.Manifest = cloneJSON(manifest)
	v.Package = cloneJSON(pkg)
	v.AttachmentKeys = cloneJSON(keys)
	v.Relations = cloneJSON(relations)
	v.Artifacts = cloneJSON(artifacts)
	v.Changelog = nullString(changelog)
	v.DeclaredVersion = nullString(declared)
	if schema.Valid {
		n := int(schema.Int64)
		v.SnapshotSchemaVersion = &n
	}
	return &v, nil
}
