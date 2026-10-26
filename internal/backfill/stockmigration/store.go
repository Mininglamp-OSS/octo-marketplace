package stockmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

var ErrRead = errors.New("stock migration evidence read failed")
var ErrConflict = errors.New("stock migration evidence changed or batch conflicts")
var ErrExceptions = errors.New("stock migration has unresolved exceptions; apply blocked")
var ErrWrite = errors.New("stock migration write failed")

// Read is operator-only, bounded, and never starts application migrations.
func Read(ctx context.Context, db *sql.DB, limit int) (Dataset, error) {
	tx, e := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		return Dataset{}, ErrRead
	}
	defer tx.Rollback()
	return read(ctx, tx, limit, false)
}
func read(ctx context.Context, tx *sql.Tx, limit int, lock bool) (Dataset, error) {
	if limit < 1 || limit > 10000 {
		return Dataset{}, ErrRead
	}
	suffix := " LIMIT ?"
	if lock {
		suffix += " FOR UPDATE"
	}
	d := Dataset{Resources: []Resource{}, Versions: []model.PluginVersion{}, Releases: []model.PluginVersionRelease{}, Effective: map[string]ConnectorEffective{}}
	revisionColumn := 0
	if e := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='plugins' AND column_name='edit_revision'`).Scan(&revisionColumn); e != nil {
		return Dataset{}, ErrRead
	}
	revisionExpr := "1"
	if revisionColumn > 0 {
		revisionExpr = "edit_revision"
	}
	rows, e := tx.QueryContext(ctx, fmt.Sprintf(`SELECT plugin_id,plugin_name,plugin_type,is_embedded,tags_json,owner_uid,space_id,visibility,listing_state,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version_id,published_version_id,current_version,status,deleted_at,%s,EXISTS(SELECT 1 FROM plugin_review_requests rr WHERE rr.plugin_id=plugins.plugin_id AND rr.status='pending' AND rr.deleted_at IS NULL) FROM plugins ORDER BY plugin_id`, revisionExpr)+suffix, limit+1)
	if e != nil {
		return d, ErrRead
	}
	for rows.Next() {
		var r Resource
		var space, current, pub, label sql.NullString
		var deleted sql.NullTime
		var tags, manifest, pkg, keys []byte
		p := &r.Plugin
		if rows.Scan(&p.ID, &p.Name, &p.Type, &p.IsEmbedded, &tags, &p.OwnerUID, &space, &p.Visibility, &p.ListingState, &manifest, &pkg, &keys, &p.ManifestHash, &p.PluginHash, &current, &pub, &label, &p.Status, &deleted, &p.EditRevision, &r.PendingReview) != nil {
			rows.Close()
			return Dataset{}, ErrRead
		}
		p.Tags = tags
		p.Manifest = manifest
		p.Package = pkg
		p.AttachmentKeys = keys
		if space.Valid {
			p.SpaceID = &space.String
		}
		if current.Valid {
			p.CurrentVersionID = &current.String
		}
		if pub.Valid {
			r.PublishedVersionID = &pub.String
		}
		if label.Valid {
			p.CurrentVersion = &label.String
		}
		if deleted.Valid {
			p.DeletedAt = &deleted.Time
		}
		r.Relations = []model.PluginRelation{}
		d.Resources = append(d.Resources, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil || len(d.Resources) > limit {
		return Dataset{}, ErrRead
	}
	lookup := map[string]int{}
	for i, r := range d.Resources {
		lookup[r.Plugin.ID] = i
	}
	rows, e = tx.QueryContext(ctx, `SELECT relation_id,source_plugin_id,target_plugin_id,target_version_id,relation_type,sort_order,relation_json,status,deleted_at FROM plugin_relations WHERE deleted_at IS NULL ORDER BY relation_id`+suffix, limit+1)
	if e != nil {
		return Dataset{}, ErrRead
	}
	count := 0
	for rows.Next() {
		count++
		var r model.PluginRelation
		var pin sql.NullString
		var raw []byte
		var deleted sql.NullTime
		if rows.Scan(&r.ID, &r.SourcePluginID, &r.TargetPluginID, &pin, &r.Type, &r.SortOrder, &raw, &r.Status, &deleted) != nil {
			rows.Close()
			return Dataset{}, ErrRead
		}
		r.Data = raw
		if pin.Valid {
			r.TargetVersionID = &pin.String
		}
		if deleted.Valid {
			r.DeletedAt = &deleted.Time
		}
		if i, ok := lookup[r.SourcePluginID]; ok {
			r.SourcePluginType = d.Resources[i].Plugin.Type
			d.Resources[i].Relations = append(d.Resources[i].Relations, r)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil || count > limit {
		return Dataset{}, ErrRead
	}
	rows, e = tx.QueryContext(ctx, `SELECT version_id,plugin_id,declared_version,manifest_hash,plugin_hash FROM plugin_versions ORDER BY version_id`+suffix, limit+1)
	if e != nil {
		return Dataset{}, ErrRead
	}
	for rows.Next() {
		var v model.PluginVersion
		var label sql.NullString
		if rows.Scan(&v.ID, &v.PluginID, &label, &v.ManifestHash, &v.PluginHash) != nil {
			rows.Close()
			return Dataset{}, ErrRead
		}
		if label.Valid {
			v.DeclaredVersion = &label.String
		}
		d.Versions = append(d.Versions, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil || len(d.Versions) > limit {
		return Dataset{}, ErrRead
	}
	rows, e = tx.QueryContext(ctx, `SELECT plugin_id,version_id,version_label FROM plugin_version_releases ORDER BY version_id`+suffix, limit+1)
	if e != nil {
		return Dataset{}, ErrRead
	}
	for rows.Next() {
		var r model.PluginVersionRelease
		if rows.Scan(&r.PluginID, &r.VersionID, &r.VersionLabel) != nil {
			rows.Close()
			return Dataset{}, ErrRead
		}
		d.Releases = append(d.Releases, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil || len(d.Releases) > limit {
		return Dataset{}, ErrRead
	}
	rows, e = tx.QueryContext(ctx, `SELECT m.mappings_json FROM plugin_stock_migration_roots m JOIN plugin_stock_migration_batches b ON b.batch_id=m.batch_id WHERE b.state='active' ORDER BY m.batch_id,m.root_plugin_id`+suffix, limit+1)
	if e != nil {
		return Dataset{}, ErrRead
	}
	count = 0
	for rows.Next() {
		count++
		var raw []byte
		if rows.Scan(&raw) != nil {
			rows.Close()
			return Dataset{}, ErrRead
		}
		var mappings []Mapping
		if json.Unmarshal(raw, &mappings) != nil {
			rows.Close()
			return Dataset{}, ErrRead
		}
		for _, m := range mappings {
			d.MigratedSourceIDs = append(d.MigratedSourceIDs, m.SourceID)
			d.ExistingMappings = append(d.ExistingMappings, m)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil || count > limit {
		return Dataset{}, ErrRead
	}
	sort.Strings(d.MigratedSourceIDs)
	// Connector effective is the B-model resolution input for reuse resources.
	// Older upgrade fixtures do not have the additive table yet.
	var effectiveTable int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='plugin_connector_effective'`).Scan(&effectiveTable); e != nil {
		return Dataset{}, ErrRead
	}
	if effectiveTable > 0 {
		rows, e = tx.QueryContext(ctx, `SELECT plugin_id,effective_revision,effective_digest FROM plugin_connector_effective ORDER BY plugin_id`+suffix, limit+1)
		if e != nil {
			return Dataset{}, ErrRead
		}
		count = 0
		for rows.Next() {
			count++
			var id, digest string
			var revision uint64
			if rows.Scan(&id, &revision, &digest) != nil {
				rows.Close()
				return Dataset{}, ErrRead
			}
			d.Effective[id] = ConnectorEffective{Revision: revision, Digest: digest}
		}
		e = rows.Err()
		rows.Close()
		if e != nil || count > limit {
			return Dataset{}, ErrRead
		}
	}
	return d, nil
}
