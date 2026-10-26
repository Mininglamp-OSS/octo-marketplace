package inventory

import (
	"context"
	"database/sql"
	"errors"
)

// Read uses an explicit read-only consistent snapshot and never invokes the
// application's startup/migration path. V1 schema must already be installed.
// Limit is per table; exceeding it fails, never emits a misleading partial report.
func Read(ctx context.Context, db *sql.DB, limit int) (Input, error) {
	var in Input
	if limit < 1 || limit > 1000000 {
		return in, errors.New("row limit must be between 1 and 1000000")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return in, errors.New("cannot start read-only inventory transaction")
	}
	defer tx.Rollback()
	scan := func(query string, fn func(*sql.Rows) error) error {
		rows, e := tx.QueryContext(ctx, query, limit+1)
		if e != nil {
			return errors.New("inventory SELECT failed; verify V1 schema and read permissions")
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			n++
			if n > limit {
				return errors.New("inventory row limit exceeded; no report produced")
			}
			if e = fn(rows); e != nil {
				return errors.New("inventory row decode failed")
			}
		}
		if rows.Err() != nil {
			return errors.New("inventory read interrupted")
		}
		return nil
	}
	err = scan(`SELECT plugin_id, plugin_name, plugin_type, owner_uid, COALESCE(space_id,''), visibility, listing_state, is_embedded, deleted_at IS NOT NULL, COALESCE(current_version_id,''), COALESCE(published_version_id,''), JSON_EXTRACT(plugin_json,'$.attachments'), attachment_keys_json FROM plugins ORDER BY plugin_id LIMIT ?`, func(rows *sql.Rows) error {
		var p Plugin
		var attachments, keys []byte
		e := rows.Scan(&p.ID, &p.Name, &p.Type, &p.Owner, &p.Space, &p.Visibility, &p.Listing, &p.Embedded, &p.Deleted, &p.Current, &p.Published, &attachments, &keys)
		p.Attachments, p.Keys = attachments, keys
		in.Plugins = append(in.Plugins, p)
		return e
	})
	if err != nil {
		return Input{}, err
	}
	err = scan(`SELECT version_id, plugin_id, COALESCE(snapshot_schema_version,0), JSON_EXTRACT(plugin_json,'$.attachments'), attachment_keys_json, artifacts_json, relations_json FROM plugin_versions ORDER BY version_id LIMIT ?`, func(rows *sql.Rows) error {
		var v Version
		var attachments, keys, artifacts, relations []byte
		e := rows.Scan(&v.ID, &v.PluginID, &v.Schema, &attachments, &keys, &artifacts, &relations)
		v.Attachments, v.Keys, v.Artifacts, v.Relations = attachments, keys, artifacts, relations
		in.Versions = append(in.Versions, v)
		return e
	})
	if err != nil {
		return Input{}, err
	}
	err = scan(`SELECT relation_id, source_plugin_id, target_plugin_id, relation_type FROM plugin_relations WHERE deleted_at IS NULL AND status=1 ORDER BY relation_id LIMIT ?`, func(rows *sql.Rows) error {
		var r Relation
		e := rows.Scan(&r.ID, &r.Source, &r.Target, &r.Type)
		in.Relations = append(in.Relations, r)
		return e
	})
	if err != nil {
		return Input{}, err
	}
	err = scan(`SELECT plugin_id, version_id FROM plugin_version_releases ORDER BY plugin_id,version_id LIMIT ?`, func(rows *sql.Rows) error {
		var r Release
		e := rows.Scan(&r.PluginID, &r.VersionID)
		in.Releases = append(in.Releases, r)
		return e
	})
	if err != nil {
		return Input{}, err
	}
	// Rollback is intentional even on success; no commit or mutation is needed.
	if err = tx.Rollback(); err != nil {
		return Input{}, errors.New("inventory transaction could not be closed")
	}
	return in, nil
}
