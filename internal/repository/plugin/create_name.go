package plugin

import (
	"context"
	"database/sql"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// rejectPluginNameCollision covers legacy rows whose normalized keys have not
// been backfilled, and global listings outside a tenant's unique-index scope.
// Tenant rows in other Spaces never participate. Locking reads serialize
// against concurrent catalog changes; the per-Space and global name indexes
// provide the final concurrent-write guards. Create, update and graph rebuilds
// use this inside their write transaction before writing a standalone name.
func rejectPluginNameCollision(ctx context.Context, tx *sql.Tx, p model.Plugin, wanted, scope string) error {
	rows, err := tx.QueryContext(ctx, `SELECT p.plugin_name,
 COALESCE(JSON_UNQUOTE(JSON_EXTRACT(v.manifest_json,'$.plugin_name')),''),p.name_key,p.published_name_key,p.space_id
 FROM plugins p LEFT JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND v.version_id=p.published_version_id
 WHERE p.plugin_type=? AND p.is_embedded=0 AND p.deleted_at IS NULL AND p.plugin_id<>?
 AND (COALESCE(p.space_id,'')=? OR
 (p.status=1 AND p.visibility IN ('public','system') AND p.listing_state='published')) FOR UPDATE`, p.Type, p.ID, scope)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var draft, published string
		var draftKey, publishedKey, existingScope sql.NullString
		if err := rows.Scan(&draft, &published, &draftKey, &publishedKey, &existingScope); err != nil {
			return err
		}
		if existingScope.String != scope {
			// Across scopes, only the published market name is public. An
			// unreleased global draft name must not be exposed by this check.
			draftKey = publishedKey
			if published != "" {
				draft = published
			}
		}
		if (draftKey.Valid && draftKey.String == wanted) || (publishedKey.Valid && publishedKey.String == wanted) {
			return ErrNameTaken
		}
		for _, name := range []string{draft, published} {
			if key, err := model.NormalizePluginName(name); err == nil && key == wanted {
				return ErrNameTaken
			}
		}
	}
	return rows.Err()
}
