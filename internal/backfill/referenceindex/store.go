package referenceindex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
)

var (
	ErrRead     = errors.New("reference index evidence read failed")
	ErrLimit    = errors.New("reference index evidence exceeds limit")
	ErrConflict = errors.New("reference index evidence or existing facts conflict")
	ErrWrite    = errors.New("reference index write failed")
)

// Read uses a consistent, bounded, read-only view and never commits.
func Read(ctx context.Context, db *sql.DB, limit int) (Dataset, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Dataset{}, ErrRead
	}
	defer tx.Rollback()
	return read(ctx, tx, limit, false)
}

func read(ctx context.Context, tx *sql.Tx, limit int, lock bool) (Dataset, error) {
	if limit < 1 || limit > 10000 {
		return Dataset{}, ErrLimit
	}
	suffix := " LIMIT ?"
	if lock {
		suffix += " FOR SHARE"
	}
	d := Dataset{}
	rows, err := tx.QueryContext(ctx, "SELECT plugin_id,plugin_type,is_embedded FROM plugins ORDER BY plugin_id"+suffix, limit+1)
	if err != nil {
		return d, ErrRead
	}
	for rows.Next() {
		var r Resource
		if rows.Scan(&r.ID, &r.Type, &r.Embedded) != nil {
			rows.Close()
			return Dataset{}, ErrRead
		}
		d.Resources = append(d.Resources, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Dataset{}, ErrRead
	}
	if len(d.Resources) > limit {
		return Dataset{}, ErrLimit
	}
	rows, err = tx.QueryContext(ctx, "SELECT plugin_id,version_id,relations_json FROM plugin_versions ORDER BY version_id"+suffix, limit+1)
	if err != nil {
		return Dataset{}, ErrRead
	}
	for rows.Next() {
		var s Snapshot
		var raw []byte
		if rows.Scan(&s.PluginID, &s.VersionID, &raw) != nil {
			rows.Close()
			return Dataset{}, ErrRead
		}
		s.Relations = json.RawMessage(raw)
		d.Snapshots = append(d.Snapshots, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Dataset{}, ErrRead
	}
	if len(d.Snapshots) > limit {
		return Dataset{}, ErrLimit
	}
	rows, err = tx.QueryContext(ctx, "SELECT plugin_id,version_id FROM plugin_version_releases ORDER BY version_id"+suffix, limit+1)
	if err != nil {
		return Dataset{}, ErrRead
	}
	for rows.Next() {
		var r Release
		if rows.Scan(&r.PluginID, &r.VersionID) != nil {
			rows.Close()
			return Dataset{}, ErrRead
		}
		d.Releases = append(d.Releases, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Dataset{}, ErrRead
	}
	if len(d.Releases) > limit {
		return Dataset{}, ErrLimit
	}
	return d, nil
}

// ApplyVersion requires the digest from a prior preview. It locks and rebuilds
// evidence before inserting any index row. Existing matching rows are retained;
// conflicting or extra facts fail the entire snapshot. No source data is edited.
// Transactions commit one version at a time so rerunning after interruption is
// resumable through the index's immutable primary key, without a mutable cursor.
func ApplyVersion(ctx context.Context, db *sql.DB, limit int, versionID, expectedDigest string) (int, error) {
	if versionID == "" || len(expectedDigest) != 64 {
		return 0, ErrConflict
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return 0, ErrWrite
	}
	defer tx.Rollback()
	d, err := read(ctx, tx, limit, true)
	if err != nil {
		return 0, err
	}
	var selected *Plan
	for _, p := range Build(d) {
		if p.VersionID == versionID {
			copy := p
			selected = &copy
		}
	}
	if selected == nil || !selected.Complete || selected.SnapshotDigest != expectedDigest {
		return 0, ErrConflict
	}
	wanted := map[string]Edge{}
	for _, e := range selected.Edges {
		wanted[e.RelationID] = e
	}
	rows, err := tx.QueryContext(ctx, `SELECT source_plugin_id,relation_id,relation_type,target_plugin_id,target_version_id FROM plugin_version_relation_refs WHERE source_version_id=? ORDER BY relation_id FOR UPDATE`, versionID)
	if err != nil {
		return 0, ErrRead
	}
	for rows.Next() {
		var owner string
		var e Edge
		var pin sql.NullString
		if rows.Scan(&owner, &e.RelationID, &e.Type, &e.TargetPluginID, &pin) != nil {
			rows.Close()
			return 0, ErrRead
		}
		if pin.Valid {
			e.TargetVersionID = &pin.String
		}
		w, ok := wanted[e.RelationID]
		if owner != selected.PluginID || !ok || !reflect.DeepEqual(w, e) {
			rows.Close()
			return 0, ErrConflict
		}
		delete(wanted, e.RelationID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, ErrRead
	}
	inserted := 0
	for _, e := range selected.Edges {
		if _, ok := wanted[e.RelationID]; !ok {
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO plugin_version_relation_refs (source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id) VALUES (?,?,?,?,?,?)`, selected.PluginID, versionID, e.RelationID, e.Type, e.TargetPluginID, e.TargetVersionID)
		if err != nil {
			return 0, ErrWrite
		}
		inserted++
	}
	if tx.Commit() != nil {
		return 0, ErrWrite
	}
	return inserted, nil
}
