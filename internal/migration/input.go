package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/artifacts"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

func sourceTime(raw []byte) (time.Time, error) {
	if len(raw) == 0 {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999", "2006-01-02"} {
		if t, e := time.ParseInLocation(layout, string(raw), time.UTC); e == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("source_timestamp_invalid")
}
func ptr(raw []byte) *string {
	if raw == nil {
		return nil
	}
	s := string(raw)
	return &s
}
func sourceInt(raw []byte) (int, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, fmt.Errorf("source_integer_invalid")
	}
	return n, nil
}

// sourceRowDigest binds every SQL column, including future columns copied by
// cloneRow. Preserve NULL/empty distinctions; normalize only driver timestamp
// representations and JSON serialization, without losing integer precision.
func sourceRowDigest(row map[string][]byte) (string, error) {
	normalized := make(map[string][]byte, len(row))
	for col, raw := range row {
		if raw == nil {
			normalized[col] = nil
			continue
		}
		value := append([]byte{}, raw...)
		switch col {
		case "created_at", "updated_at", "deleted_at", "released_at", "requested_at", "reviewed_at", "submitted_at", "resolved_at", "verified_at":
			at, err := sourceTime(raw)
			if err != nil {
				return "", err
			}
			value = []byte(at.UTC().Format(time.RFC3339Nano))
		default:
			if strings.HasSuffix(col, "_json") {
				var err error
				value, err = libplugin.CanonicalJSON(raw)
				if err != nil {
					return "", fmt.Errorf("source_json_invalid")
				}
			}
		}
		normalized[col] = value
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return digest(data), nil
}

func sourceResource(row map[string][]byte) (Resource, error) {
	var r Resource
	var rowErr error
	r.SourceRowHash, rowErr = sourceRowDigest(row)
	if rowErr != nil {
		return r, rowErr
	}
	p := &r.Plugin
	p.ID = string(row["plugin_id"])
	p.Name = string(row["plugin_name"])
	p.Type = model.PluginType(row["plugin_type"])
	p.OwnerUID = string(row["owner_uid"])
	p.SpaceID = ptr(row["space_id"])
	p.Visibility = model.PluginVisibility(row["visibility"])
	p.ListingState = model.PluginListingState(row["listing_state"])
	p.IsEmbedded = string(row["is_embedded"]) == "1"
	var err error
	p.Status, err = sourceInt(row["status"])
	if err != nil {
		return r, err
	}
	p.Tags = row["tags_json"]
	p.Manifest = row["manifest_json"]
	p.Package = row["plugin_json"]
	p.AttachmentKeys = row["attachment_keys_json"]
	p.ManifestHash = string(row["manifest_hash"])
	p.PluginHash = string(row["plugin_hash"])
	p.CurrentVersionID = ptr(row["current_version_id"])
	p.CurrentVersion = ptr(row["current_version"])
	r.PublishedVersionID = ptr(row["published_version_id"])
	p.CategoryID = ptr(row["category_id"])
	p.Icon = string(row["icon"])
	p.ToolCount, err = sourceInt(row["tool_count"])
	if err != nil {
		return r, err
	}
	if raw := row["rating"]; raw != nil {
		n, err := sourceInt(raw)
		if err != nil {
			return r, err
		}
		p.Rating = &n
	}
	p.CreatedByBotUID = ptr(row["created_by_bot_uid"])
	p.CreatedByBotName = ptr(row["created_by_bot_name"])
	p.Publisher = string(row["publisher"])
	p.CreatorName = string(row["creator_name"])
	p.CreatedByType = string(row["created_by_type"])
	var e error
	p.CreatedAt, e = sourceTime(row["created_at"])
	if e != nil {
		return r, e
	}
	p.UpdatedAt, e = sourceTime(row["updated_at"])
	if e != nil {
		return r, e
	}
	if row["deleted_at"] != nil {
		t, e := sourceTime(row["deleted_at"])
		if e != nil {
			return r, e
		}
		p.DeletedAt = &t
	}
	return r, nil
}

type databaseQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Explicit projections validate every column the conversion writes. LIMIT 0
// validates availability without copying or inspecting immutable history.
var databaseSchema = []struct{ table, columns string }{
	{"plugins", "plugin_id,plugin_name,plugin_type,category_id,tags_json,publisher,owner_uid,space_id,visibility,creator_name,created_by_type,created_by_bot_uid,created_by_bot_name,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version_id,current_version,published_version_id,status,is_embedded,listing_state,icon,tool_count,rating,created_at,updated_at,deleted_at"},
	{"plugin_relations", "relation_id,source_plugin_id,target_plugin_id,target_version_id,relation_type,sort_order,relation_json,status,created_by,created_at,updated_at,deleted_at"},
	{"plugin_versions", "version_id,plugin_id,version,declared_version,snapshot_schema_version,artifacts_json,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,relations_json,changelog,created_by,created_at"},
	{"plugin_version_releases", "plugin_id,version_id,version_label,released_by,released_at"},
	{"plugin_review_requests", "review_id,plugin_id,status,deleted_at"},
	{"plugin_version_relation_refs", "source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id"},
	{"plugin_version_reference_index_state", "source_plugin_id,source_version_id,relation_count,snapshot_digest,verified_at"},
	{"plugin_placements", "placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at"},
	{"plugin_audit_logs", "audit_log_id,plugin_id,action,operator_id,operator_name,request_id,before_hash,after_hash,manifest_snapshot_json,plugin_snapshot_json,remark,created_at"},
}

// CheckDatabaseSchema refuses an incomplete version/reference schema. It never
// upgrades schema; operators must deploy the matching schema separately.
func CheckDatabaseSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("database_required")
	}
	return checkDatabaseSchema(ctx, db)
}
func checkDatabaseSchema(ctx context.Context, q databaseQueryer) error {
	for _, s := range databaseSchema {
		rows, err := q.QueryContext(ctx, "SELECT "+s.columns+" FROM "+s.table+" LIMIT 0")
		if err != nil {
			return fmt.Errorf("database_schema_incomplete: %s: %w", s.table, err)
		}
		err = rows.Close()
		if err != nil {
			return fmt.Errorf("database_schema_incomplete: %s: %w", s.table, err)
		}
	}
	return nil
}

func readDatabaseRows(ctx context.Context, q databaseQueryer, query string, visit func(map[string][]byte) error, args ...any) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		values := make([][]byte, len(cols))
		args := make([]any, len(cols))
		for i := range args {
			args[i] = &values[i]
		}
		if err := rows.Scan(args...); err != nil {
			return err
		}
		row := make(map[string][]byte, len(cols))
		for i, k := range cols {
			if values[i] == nil {
				row[k] = nil
			} else {
				row[k] = append([]byte{}, values[i]...)
			}
		}
		if err := visit(row); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return rows.Close()
}

// InputFromDatabase captures source metadata in a consistent read-only SQL
// transaction, then binds source object bytes separately. Both are revalidated.
func InputFromDatabase(ctx context.Context, db *sql.DB, read artifacts.Reader) (Input, []Issue, error) {
	in := Input{}
	if db == nil {
		return in, nil, fmt.Errorf("database_required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return in, nil, err
	}
	defer tx.Rollback()
	if err := checkDatabaseSchema(ctx, tx); err != nil {
		return in, nil, err
	}
	var issues []Issue
	lookup := map[string]int{}
	e := readDatabaseRows(ctx, tx, "SELECT * FROM plugins ORDER BY plugin_id", func(row map[string][]byte) error {
		r, e := sourceResource(row)
		if e != nil {
			return e
		}
		lookup[r.Plugin.ID] = len(in.Resources)
		in.Resources = append(in.Resources, r)
		return nil
	})
	if e != nil {
		return in, nil, e
	}
	e = readDatabaseRows(ctx, tx, "SELECT * FROM plugin_relations ORDER BY relation_id", func(row map[string][]byte) error {
		var r model.PluginRelation
		r.ID = string(row["relation_id"])
		r.SourcePluginID = string(row["source_plugin_id"])
		r.TargetPluginID = string(row["target_plugin_id"])
		r.TargetVersionID = ptr(row["target_version_id"])
		r.Type = string(row["relation_type"])
		var err error
		r.SortOrder, err = sourceInt(row["sort_order"])
		if err != nil {
			return err
		}
		r.Status, err = sourceInt(row["status"])
		if err != nil {
			return err
		}
		r.Data = row["relation_json"]
		r.CreatedBy = string(row["created_by"])
		r.CreatedAt, err = sourceTime(row["created_at"])
		if err != nil {
			return err
		}
		r.UpdatedAt, err = sourceTime(row["updated_at"])
		if err != nil {
			return err
		}
		if row["deleted_at"] != nil {
			t, err := sourceTime(row["deleted_at"])
			if err != nil {
				return err
			}
			r.DeletedAt = &t
		}
		i, ok := lookup[r.SourcePluginID]
		if !ok {
			issues = append(issues, Issue{r.SourcePluginID, "relation_source_missing"})
			return nil
		}
		r.SourcePluginType = in.Resources[i].Plugin.Type
		if j, ok := lookup[r.TargetPluginID]; ok {
			r.TargetPluginType = in.Resources[j].Plugin.Type
		}
		in.Resources[i].Relations = append(in.Resources[i].Relations, r)
		return nil
	})
	if e != nil {
		return in, nil, e
	}
	e = readDatabaseRows(ctx, tx, "SELECT * FROM plugin_versions ORDER BY version_id", func(row map[string][]byte) error {
		v := model.PluginVersion{ID: string(row["version_id"]), PluginID: string(row["plugin_id"]), Version: string(row["version"]), DeclaredVersion: ptr(row["declared_version"]), Manifest: row["manifest_json"], Package: row["plugin_json"], AttachmentKeys: row["attachment_keys_json"], ManifestHash: string(row["manifest_hash"]), PluginHash: string(row["plugin_hash"]), Relations: row["relations_json"]}

		v.Artifacts = row["artifacts_json"]
		v.Changelog = ptr(row["changelog"])
		v.CreatedBy = string(row["created_by"])
		var err error
		v.CreatedAt, err = sourceTime(row["created_at"])
		if err != nil {
			return err
		}
		if raw := row["snapshot_schema_version"]; raw != nil {
			n, err := strconv.Atoi(string(raw))
			if err != nil {
				return fmt.Errorf("source_schema_version_invalid")
			}
			v.SnapshotSchemaVersion = &n
		}
		if i, ok := lookup[v.PluginID]; ok {
			v.PluginType = in.Resources[i].Plugin.Type
		}
		in.Versions = append(in.Versions, v)
		return nil
	})
	if e != nil {
		return in, nil, e
	}
	e = readDatabaseRows(ctx, tx, "SELECT * FROM plugin_version_releases ORDER BY plugin_id,version_id", func(row map[string][]byte) error {

		released, err := sourceTime(row["released_at"])
		if err != nil {
			return err
		}
		in.Releases = append(in.Releases, model.PluginVersionRelease{PluginID: string(row["plugin_id"]), VersionID: string(row["version_id"]), VersionLabel: string(row["version_label"]), ReleasedBy: string(row["released_by"]), ReleasedAt: released})
		return nil
	})
	if e != nil {
		return in, nil, e
	}
	e = readDatabaseRows(ctx, tx, "SELECT * FROM plugin_review_requests ORDER BY review_id", func(row map[string][]byte) error {
		if string(row["status"]) == "pending" && row["deleted_at"] == nil {
			if i, ok := lookup[string(row["plugin_id"])]; ok {
				in.Resources[i].PendingReview = true
			}
		}
		return nil
	})
	if e != nil {
		return in, nil, e
	}
	for i := range in.Resources {
		r := &in.Resources[i]
		r.Evidence.RowHash = r.SourceRowHash
		r.Evidence.Tables, err = captureSourceTables(ctx, tx, r.Plugin.ID, false)
		if err != nil {
			return in, nil, err
		}
	}
	in.CategorySnapshot, err = captureCategorySnapshot(ctx, tx, false)
	if err != nil {
		return in, nil, err
	}
	in.CategorySnapshotCaptured = true
	if err := tx.Commit(); err != nil {
		return in, nil, err
	}
	for i := range in.Resources {
		r := &in.Resources[i]
		if r.Plugin.DeletedAt != nil {
			continue
		}
		// Resolve may read one source key more than once. Bind every observed byte
		// sequence and fail if the source changes even during this initial read.
		observed := map[string]SourceObject{}
		sourceRead := func(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
			if read == nil {
				return nil, fmt.Errorf("object_reader_required")
			}
			data, err := read(ctx, key, maxBytes)
			if err != nil {
				return nil, err
			}
			object := SourceObject{Key: key, Hash: digest(data), Size: int64(len(data))}
			if previous, ok := observed[key]; ok && previous != object {
				return nil, fmt.Errorf("source_object_changed")
			}
			observed[key] = object
			return data, nil
		}
		files, icon, e := artifacts.ResolveWithIcon(ctx, &r.Plugin, sourceRead, artifacts.DefaultLimits())
		if e != nil {
			issues = append(issues, Issue{r.Plugin.ID, e.Error()})
			continue
		}
		if icon != nil {
			r.IconFile = &File{Path: icon.Path, MIME: icon.MIME, ObjectKey: icon.ObjectKey, Data: append([]byte(nil), icon.Data...)}
		}
		keys := make([]string, 0, len(observed))
		for key := range observed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			r.Evidence.Objects = append(r.Evidence.Objects, observed[key])
		}
		for _, f := range files {
			r.Files = append(r.Files, File{Path: f.Path, MIME: f.MIME, Data: f.Data})
		}
	}
	id, err := databaseInputID(in)
	if err != nil {
		return in, issues, err
	}
	in.SnapshotID = id
	return in, issues, nil
}

// File.Data is deliberately omitted by File's normal JSON marshaler, so
// include bytes explicitly in this identity. Canonical JSON normalizes object
// key order and whitespace; stable SQL/file ordering normalizes slices.
func databaseInputID(in Input) (string, error) {
	in.SnapshotID = ""
	type identityFile struct {
		Path, MIME, ObjectKey string
		Data                  []byte
	}
	files := make([][]identityFile, len(in.Resources))
	for i, r := range in.Resources {
		for _, f := range r.Files {
			files[i] = append(files[i], identityFile{f.Path, f.MIME, f.ObjectKey, f.Data})
		}
		if f := r.IconFile; f != nil {
			files[i] = append(files[i], identityFile{"@icon:" + f.Path, f.MIME, f.ObjectKey, f.Data})
		}
	}
	raw, err := json.Marshal(struct {
		Input Input
		Files [][]identityFile
	}{in, files})
	if err != nil {
		return "", fmt.Errorf("source_json_invalid: %w", err)
	}
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		return "", err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("source_json_invalid")
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}

// Read complete owned row sets, including soft-deleted and historical records.
// Sorting row digests preserves duplicates while ignoring query/driver ordering.
var sourceTables = []struct{ table, owner, order string }{
	{"plugin_relations", "source_plugin_id", "relation_id"},
	{"plugin_versions", "plugin_id", "version_id"},
	{"plugin_version_releases", "plugin_id", "version_id"},
	{"plugin_review_requests", "plugin_id", "review_id"},
	{"plugin_placements", "plugin_id", "placement_id"},
	{"plugin_version_relation_refs", "source_plugin_id", "source_version_id,relation_id"},
	{"plugin_version_reference_index_state", "source_plugin_id", "source_version_id"},
	{"plugin_audit_logs", "plugin_id", "audit_log_id"},
}

func captureSourceTables(ctx context.Context, q databaseQueryer, id string, lock bool) (map[string]string, error) {
	out := make(map[string]string, len(sourceTables))
	for _, spec := range sourceTables {
		query := "SELECT * FROM " + spec.table + " WHERE " + spec.owner + "=? ORDER BY " + spec.order
		if lock {
			query += " FOR UPDATE"
		}
		hashes := []string{}
		err := readDatabaseRows(ctx, q, query, func(row map[string][]byte) error {
			hash, err := sourceRowDigest(row)
			if err != nil {
				return err
			}
			hashes = append(hashes, hash)
			return nil
		}, id)
		if err != nil {
			return nil, err
		}
		sort.Strings(hashes)
		raw, err := json.Marshal(hashes)
		if err != nil {
			return nil, err
		}
		out[spec.table] = digest(raw)
	}
	return out, nil
}
