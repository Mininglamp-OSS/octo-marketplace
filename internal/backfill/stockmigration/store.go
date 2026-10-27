package stockmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

var ErrRead = errors.New("stock migration evidence read failed")
var ErrConflict = errors.New("stock migration evidence changed or batch conflicts")
var ErrExceptions = errors.New("stock migration has unresolved exceptions; apply blocked")
var ErrWrite = errors.New("stock migration write failed")

// ErrNameConflict means an extracted Expert/Skill would collide with an active
// marketplace display name in the same Space. The operator must rename or
// explicitly reuse the existing asset; migration never auto-suffixes a name.
var ErrNameConflict = errors.New("migration target name conflicts with an active marketplace plugin")
var ErrG4 = errors.New("stock migration requires explicit confirmed G4 exception policy")

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
	d := Dataset{Resources: []Resource{}, Versions: []model.PluginVersion{}, Releases: []model.PluginVersionRelease{}}
	rows, e := tx.QueryContext(ctx, `SELECT p.plugin_id,p.plugin_name,p.plugin_type,p.is_embedded,p.tags_json,p.owner_uid,p.space_id,p.visibility,p.listing_state,p.manifest_json,p.plugin_json,p.attachment_keys_json,p.manifest_hash,p.plugin_hash,p.current_version_id,p.published_version_id,p.current_version,p.status,p.deleted_at,EXISTS(SELECT 1 FROM plugin_review_requests rr WHERE rr.plugin_id=p.plugin_id AND rr.status='pending' AND rr.deleted_at IS NULL),COALESCE(JSON_UNQUOTE(JSON_EXTRACT(pv.manifest_json,'$.plugin_name')),'') FROM plugins p LEFT JOIN plugin_versions pv ON pv.plugin_id=p.plugin_id AND pv.version_id=p.published_version_id ORDER BY p.plugin_id`+suffix, limit+1)
	if e != nil {
		return d, ErrRead
	}
	for rows.Next() {
		var r Resource
		var space, current, pub, label sql.NullString
		var deleted sql.NullTime
		var tags, manifest, pkg, keys []byte
		p := &r.Plugin
		if rows.Scan(&p.ID, &p.Name, &p.Type, &p.IsEmbedded, &tags, &p.OwnerUID, &space, &p.Visibility, &p.ListingState, &manifest, &pkg, &keys, &p.ManifestHash, &p.PluginHash, &current, &pub, &label, &p.Status, &deleted, &r.PendingReview, &r.PublishedName) != nil {
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
	return d, nil
}

// checkPlannedNames blocks any clone that would introduce a duplicate active
// Expert/Skill display name. The check uses the caller's full bounded inventory,
// not just the rows that happened to be cloned, so a name occupied by an
// unrelated active plugin is caught too. Soft-deleted rows are ignored; delisted
// rows still reserve the name.
func checkPlannedNames(unit *Unit, resources []Resource) error {
	type key struct{ scope, typ, name string }
	occupied := map[key]string{}
	planned := map[key]string{}
	for _, r := range resources {
		if r.Plugin.DeletedAt != nil || r.Plugin.IsEmbedded {
			continue
		}
		if r.Plugin.Type != model.PluginTypeExpert && r.Plugin.Type != model.PluginTypeSkill {
			continue
		}
		displayName := r.Plugin.Name
		if r.PublishedName != "" {
			displayName = r.PublishedName
		}
		name, err := model.NormalizePluginName(displayName)
		if err != nil {
			continue
		}
		scope := ""
		if r.Plugin.SpaceID != nil {
			scope = *r.Plugin.SpaceID
		}
		occupied[key{scope, string(r.Plugin.Type), name}] = r.Plugin.ID
	}
	for _, n := range unit.Nodes {
		if !n.Clone || (n.Type != model.PluginTypeExpert && n.Type != model.PluginTypeSkill) {
			continue
		}
		name, err := model.NormalizePluginName(n.Plugin.Name)
		if err != nil {
			return ErrWrite
		}
		scope := ""
		if n.Plugin.SpaceID != nil {
			scope = *n.Plugin.SpaceID
		}
		k := key{scope, string(n.Type), name}
		if existing := occupied[k]; existing != "" && existing != n.TargetID {
			return fmt.Errorf("%w: %s %q already belongs to %s", ErrNameConflict, n.Type, n.Plugin.Name, existing)
		}
		if previous := planned[k]; previous != "" && previous != n.TargetID {
			return fmt.Errorf("%w: %s %q appears twice in this migration unit", ErrNameConflict, n.Type, n.Plugin.Name)
		}
		planned[k] = n.TargetID
	}
	return nil
}

// ApplyRoot is one atomic migration unit. A completed mapping wins on repeat,
// while another root can be previewed and applied after interruption. All rows
// and publication evidence are locked before comparing the preview digest.
func ApplyRoot(ctx context.Context, db *sql.DB, limit int, batch, root, digest string, g4 bool) (int, error) {
	return ApplyRootWithResolver(ctx, db, limit, batch, root, digest, g4, nil)
}
func ApplyRootWithResolver(ctx context.Context, db *sql.DB, limit int, batch, root, digest string, g4 bool, resolver SkillResolver) (int, error) {
	if !g4 {
		return 0, ErrG4
	}
	if !validID(batch) || !validID(root) || len(digest) != 64 {
		return 0, ErrConflict
	}
	// Resolve and freeze external bytes before acquiring inventory locks. The
	// locked re-read must exactly match the source evidence used by the resolver.
	var prepared Dataset
	var sourceDigest string
	if resolver != nil {
		var err error
		prepared, err = Read(ctx, db, limit)
		if err != nil {
			return 0, err
		}
		sourceDigest = hash(prepared)
		prepared = Hydrate(ctx, prepared, resolver)
	}
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if e != nil {
		return 0, ErrWrite
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_stock_migration_batches(batch_id,created_at) VALUES (?,?) ON DUPLICATE KEY UPDATE batch_id=batch_id`, batch, time.Now().UTC()); e != nil {
		return 0, ErrWrite
	}
	var state string
	if tx.QueryRowContext(ctx, `SELECT state FROM plugin_stock_migration_batches WHERE batch_id=? FOR UPDATE`, batch).Scan(&state) != nil {
		return 0, ErrRead
	}
	if state != "active" {
		return 0, ErrConflict
	}
	var exists int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_stock_migration_roots WHERE batch_id=? AND root_plugin_id=?`, batch, root).Scan(&exists); e != nil {
		return 0, ErrRead
	}
	if exists > 0 {
		return 0, tx.Commit()
	}
	d, e := read(ctx, tx, limit, true)
	if e != nil {
		return 0, e
	}
	if resolver != nil {
		if hash(d) != sourceDigest {
			return 0, ErrConflict
		}
		d.ResolvedSkillFiles = prepared.ResolvedSkillFiles
		d.FileExceptions = prepared.FileExceptions
	}
	plan := Build(d)
	if plan.Digest != digest {
		return 0, ErrConflict
	}
	if len(plan.Exceptions) > 0 {
		return 0, ErrExceptions
	}
	var unit *Unit
	var before Resource
	for i := range plan.Units {
		if plan.Units[i].RootID == root {
			unit = &plan.Units[i]
		}
	}
	for _, r := range d.Resources {
		if r.Plugin.ID == root {
			before = r
		}
	}
	if unit == nil {
		return 0, ErrConflict
	}
	if err := checkPlannedNames(unit, d.Resources); err != nil {
		return 0, err
	}
	localVersions := map[string]bool{}
	for _, n := range unit.Nodes {
		if n.Release {
			localVersions[n.TargetID+"\x00"+n.VersionID] = true
		}
	}
	for _, n := range unit.Nodes {
		for _, edge := range n.Relations {
			if edge.TargetVersionID != nil && !localVersions[edge.TargetPluginID+"\x00"+*edge.TargetVersionID] && !released(d, edge.TargetPluginID, *edge.TargetVersionID) {
				return 0, ErrConflict
			}
		}
	}
	now := time.Now().UTC()
	for _, node := range unit.Nodes {
		if e = writeNode(ctx, tx, node, root, now); e != nil {
			return 0, e
		}
	}
	var after Resource
	for _, n := range unit.Nodes {
		if n.TargetID == root {
			after.Plugin = n.Plugin
			after.PublishedVersionID = n.PreviousPublished
			after.Plugin.CurrentVersionID = &n.VersionID
			after.Plugin.CurrentVersion = &n.Label
			if n.Release {
				after.PublishedVersionID = &n.VersionID
			}
		}
	}
	prior, _ := json.Marshal(before)
	next, _ := json.Marshal(after)
	mapping, _ := json.Marshal(unit.Nodes)
	output, _ := json.Marshal(unitSnapshotStates(*unit))
	if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_stock_migration_roots(batch_id,root_plugin_id,source_digest,mappings_json,before_json,after_json,node_states_json,applied_at) VALUES (?,?,?,?,?,?,?,?)`, batch, root, digest, string(mapping), string(prior), string(next), string(output), now); e != nil {
		return 0, ErrWrite
	}
	if tx.Commit() != nil {
		return 0, ErrWrite
	}
	return len(unit.Nodes), nil
}
func validID(v string) bool {
	return v != "" && len(v) <= 64 && strings.TrimSpace(v) == v && !strings.ContainsAny(v, "\x00\r\n")
}
func writeNode(ctx context.Context, tx *sql.Tx, n Node, root string, now time.Time) error {
	p := n.Plugin
	if n.Clone {
		source := n.SourceID
		if n.Type == model.PluginTypeConnector {
			source = strings.TrimSuffix(source, ":mcp")
		}
		nameKey, nameScope := "", ""
		if p.Type == model.PluginTypeExpert || p.Type == model.PluginTypeSkill {
			key, err := model.NormalizePluginName(p.Name)
			if err != nil {
				return ErrWrite
			}
			nameKey = key
			if p.SpaceID != nil {
				nameScope = *p.SpaceID
			}
		}
		// Copy provenance and placement metadata; authoritative ownership/audience is
		// explicitly inherited from the container, rather than from caller identity.
		// name_key/name_scope are computed by the same normalizer as the API write
		// path so migration-created Experts/Skills cannot bypass the active-name index.
		result, e := tx.ExecContext(ctx, `INSERT INTO plugins(plugin_id,plugin_name,plugin_type,is_embedded,category_id,tags_json,publisher,owner_uid,space_id,visibility,listing_state,creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,icon,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,status,created_at,updated_at,name_key,name_scope) SELECT ?,?,?,0,NULL,?,publisher,?,?,?,?,creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,icon,0,?,?,NULL,?,?,1,?,?,?,? FROM plugins WHERE plugin_id=?`, p.ID, p.Name, p.Type, string(p.Tags), p.OwnerUID, p.SpaceID, p.Visibility, p.ListingState, string(p.Manifest), string(p.Package), p.ManifestHash, p.PluginHash, now, now, nameKey, nameScope, source)
		if e != nil {
			return ErrWrite
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return ErrWrite
		}
	} else {
		if _, e := tx.ExecContext(ctx, `UPDATE plugins SET manifest_json=?,plugin_json=?,attachment_keys_json=NULL,manifest_hash=?,plugin_hash=?,visibility=? WHERE plugin_id=?`, string(p.Manifest), string(p.Package), p.ManifestHash, p.PluginHash, p.Visibility, p.ID); e != nil {
			return ErrWrite
		}
	}
	if _, e := tx.ExecContext(ctx, `UPDATE plugin_relations SET deleted_at=?,updated_at=? WHERE source_plugin_id=? AND deleted_at IS NULL`, now, now, p.ID); e != nil {
		return ErrWrite
	}
	for _, r := range n.Relations {
		var data any
		if len(r.Data) > 0 {
			data = string(r.Data)
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO plugin_relations(relation_id,source_plugin_id,target_plugin_id,target_version_id,relation_type,sort_order,relation_json,status,created_by,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, r.ID, p.ID, r.TargetPluginID, r.TargetVersionID, r.Type, r.SortOrder, data, 1, "stock-migration", now, now); e != nil {
			return ErrWrite
		}
	}
	if n.Type == model.PluginTypeConnector {
		return nil
	}
	raw, e := model.EncodeSnapshotRelations(n.Relations)
	if e != nil {
		return ErrWrite
	}
	var count int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, p.ID).Scan(&count) != nil {
		return ErrRead
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_versions(version_id,plugin_id,version,declared_version,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,relations_json,changelog,created_by,created_at,snapshot_schema_version,artifacts_json) VALUES (?,?,?,?,?,?,NULL,?,?,?,?,?,?,?,?)`, n.VersionID, p.ID, count+1, n.Label, string(p.Manifest), string(p.Package), p.ManifestHash, p.PluginHash, string(raw), "Independent reference stock migration", "stock-migration", now, model.PluginSnapshotSchemaVersion, string(n.Artifacts)); e != nil {
		return ErrWrite
	}
	for _, r := range n.Relations {
		if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_version_relation_refs(source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id) VALUES (?,?,?,?,?,?)`, p.ID, n.VersionID, r.ID, r.Type, r.TargetPluginID, r.TargetVersionID); e != nil {
			return ErrWrite
		}
	}
	var storedRelations []byte
	if tx.QueryRowContext(ctx, `SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`, p.ID, n.VersionID).Scan(&storedRelations) != nil {
		return ErrRead
	}
	sum := sha256.Sum256(storedRelations)
	if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_version_reference_index_state(source_plugin_id,source_version_id,relation_count,snapshot_digest,verified_at) VALUES (?,?,?,?,?)`, p.ID, n.VersionID, len(n.Relations), hex.EncodeToString(sum[:]), now); e != nil {
		return ErrWrite
	}
	if n.Release {
		if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,?,?,?)`, p.ID, n.VersionID, n.Label, "stock-migration", now); e != nil {
			return ErrWrite
		}
	}
	if n.Release {
		publishedKey := any(nil)
		if p.Type == model.PluginTypeExpert || p.Type == model.PluginTypeSkill {
			key, err := model.NormalizePluginName(p.Name)
			if err != nil {
				return ErrWrite
			}
			publishedKey = key
		}
		if _, e = tx.ExecContext(ctx, `UPDATE plugins SET current_version_id=?,current_version=?,published_version_id=?,published_name_key=?,updated_at=? WHERE plugin_id=?`, n.VersionID, n.Label, published(n), publishedKey, now, p.ID); e != nil {
			return ErrWrite
		}
	} else if _, e = tx.ExecContext(ctx, `UPDATE plugins SET current_version_id=?,current_version=?,published_version_id=?,updated_at=? WHERE plugin_id=?`, n.VersionID, n.Label, published(n), now, p.ID); e != nil {
		return ErrWrite
	}
	return nil
}
func published(n Node) any {
	if n.Release {
		return n.VersionID
	}
	return n.PreviousPublished
}

// Rollback restores root pointers and the original editable topology. New rows,
// versions and files survive; extracted resources are unlisted. A retired batch
// cannot apply again. Concurrent edits to the migrated root block the operation.
func Rollback(ctx context.Context, db *sql.DB, batch string) error {
	if !validID(batch) {
		return ErrConflict
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return ErrWrite
	}
	defer tx.Rollback()
	var state string
	if tx.QueryRowContext(ctx, `SELECT state FROM plugin_stock_migration_batches WHERE batch_id=? FOR UPDATE`, batch).Scan(&state) != nil {
		return ErrRead
	}
	if state == "rolled_back" {
		return tx.Commit()
	}
	rows, e := tx.QueryContext(ctx, `SELECT before_json,after_json,mappings_json,node_states_json FROM plugin_stock_migration_roots WHERE batch_id=? ORDER BY applied_at DESC,root_plugin_id DESC FOR UPDATE`, batch)
	if e != nil {
		return ErrRead
	}
	type saved struct {
		before   Resource
		after    Resource
		mappings []Mapping
		nodes    []nodeState
	}
	items := []saved{}
	for rows.Next() {
		var b, a, m, ns []byte
		if rows.Scan(&b, &a, &m, &ns) != nil {
			rows.Close()
			return ErrRead
		}
		var item saved
		if json.Unmarshal(b, &item.before) != nil || json.Unmarshal(a, &item.after) != nil || json.Unmarshal(m, &item.mappings) != nil || json.Unmarshal(ns, &item.nodes) != nil {
			rows.Close()
			return ErrRead
		}
		items = append(items, item)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return ErrRead
	}
	for _, item := range items {
		p := item.before.Plugin
		var current, pub sql.NullString
		var mh, ph string
		var visibility, listing string
		var status int
		var deleted sql.NullTime
		if tx.QueryRowContext(ctx, `SELECT current_version_id,published_version_id,manifest_hash,plugin_hash,visibility,listing_state,status,deleted_at FROM plugins WHERE plugin_id=? FOR UPDATE`, p.ID).Scan(&current, &pub, &mh, &ph, &visibility, &listing, &status, &deleted) != nil {
			return ErrRead
		}
		expectedPub := ""
		if item.after.PublishedVersionID != nil {
			expectedPub = *item.after.PublishedVersionID
		}
		if pub.String != expectedPub || status != 1 || deleted.Valid || visibility != string(item.after.Plugin.Visibility) || listing != string(item.after.Plugin.ListingState) || item.after.Plugin.CurrentVersionID == nil || current.String != *item.after.Plugin.CurrentVersionID || mh != item.after.Plugin.ManifestHash || ph != item.after.Plugin.PluginHash {
			return ErrConflict
		}
		if _, e = tx.ExecContext(ctx, `UPDATE plugins SET manifest_json=?,plugin_json=?,attachment_keys_json=?,manifest_hash=?,plugin_hash=?,current_version_id=?,published_version_id=?,current_version=?,visibility=?,listing_state=?,updated_at=? WHERE plugin_id=?`, string(p.Manifest), string(p.Package), nullableJSON(p.AttachmentKeys), p.ManifestHash, p.PluginHash, p.CurrentVersionID, item.before.PublishedVersionID, p.CurrentVersion, p.Visibility, p.ListingState, time.Now().UTC(), p.ID); e != nil {
			return ErrWrite
		}
		if _, e = tx.ExecContext(ctx, `UPDATE plugin_relations SET deleted_at=NOW(3) WHERE source_plugin_id=? AND deleted_at IS NULL`, p.ID); e != nil {
			return ErrWrite
		}
		for _, r := range item.before.Relations {
			if _, e = tx.ExecContext(ctx, `UPDATE plugin_relations SET deleted_at=NULL WHERE source_plugin_id=? AND relation_id=?`, p.ID, r.ID); e != nil {
				return ErrWrite
			}
		}
		for _, n := range item.nodes {
			if n.ID == p.ID {
				continue
			}
			var current, pub sql.NullString
			var mh, ph, visibility, listing string
			var status int
			var deleted sql.NullTime
			if tx.QueryRowContext(ctx, `SELECT current_version_id,published_version_id,manifest_hash,plugin_hash,visibility,listing_state,status,deleted_at FROM plugins WHERE plugin_id=? FOR UPDATE`, n.ID).Scan(&current, &pub, &mh, &ph, &visibility, &listing, &status, &deleted) != nil {
				return ErrRead
			}
			if current.String != n.Current || pub.String != n.Published || mh != n.ManifestHash || ph != n.PluginHash || visibility != string(n.Visibility) || listing != string(n.Listing) || status != 1 || deleted.Valid {
				return ErrConflict
			}
			targetListing := "draft"
			if n.Released || n.Listing != "draft" {
				targetListing = "delisted"
			}
			if _, e = tx.ExecContext(ctx, `UPDATE plugins SET listing_state=?,updated_at=NOW(3) WHERE plugin_id=?`, targetListing, n.ID); e != nil {
				return ErrWrite
			}
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE plugin_stock_migration_batches SET state='rolled_back' WHERE batch_id=?`, batch); e != nil {
		return ErrWrite
	}
	if tx.Commit() != nil {
		return ErrWrite
	}
	return nil
}
func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

type nodeState struct {
	ID, Current, Published, ManifestHash, PluginHash string
	Visibility                                       model.PluginVisibility
	Listing                                          model.PluginListingState
	Released                                         bool
}

func unitSnapshotStates(u Unit) []nodeState {
	result := []nodeState{}
	for _, n := range u.Nodes {
		state := nodeState{ID: n.TargetID, ManifestHash: n.Plugin.ManifestHash, PluginHash: n.Plugin.PluginHash, Visibility: n.Plugin.Visibility, Listing: n.Plugin.ListingState, Released: n.Release}
		if n.Type != model.PluginTypeConnector {
			state.Current = n.VersionID
		}
		if n.Release {
			state.Published = n.VersionID
		} else if n.PreviousPublished != nil {
			state.Published = *n.PreviousPublished
		}
		result = append(result, state)
	}
	return result
}
