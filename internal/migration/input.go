package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/artifacts"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/snapshot"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
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
func sourceResource(row map[string][]byte) (Resource, error) {
	var r Resource
	p := &r.Plugin
	p.ID = string(row["plugin_id"])
	p.Name = string(row["plugin_name"])
	p.Type = model.PluginType(row["plugin_type"])
	p.OwnerUID = string(row["owner_uid"])
	p.SpaceID = ptr(row["space_id"])
	p.Visibility = model.PluginVisibility(row["visibility"])
	p.ListingState = model.PluginListingState(row["listing_state"])
	p.IsEmbedded = string(row["is_embedded"]) == "1"
	p.Status, _ = strconv.Atoi(string(row["status"]))
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
	p.ToolCount, _ = strconv.Atoi(string(row["tool_count"]))
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
func visitTable(b snapshot.Bundle, name string, required bool, visit func(map[string][]byte) error) error {
	exists := false
	for _, t := range b.Manifest.Tables {
		if t.Name == name {
			exists = true
			break
		}
	}
	if !exists {
		if required {
			return fmt.Errorf("snapshot_table_missing: %s", name)
		}
		return nil
	}
	return snapshot.ReadTable(b, name, func(cols []string, values []*[]byte) error {
		if len(cols) != len(values) {
			return fmt.Errorf("snapshot_row_shape_invalid")
		}
		m := map[string][]byte{}
		for i, k := range cols {
			if values[i] != nil {
				m[k] = *values[i]
			} else {
				m[k] = nil
			}
		}
		return visit(m)
	})
}

// InputFromSnapshot plans against immutable archived rows, never mutated target heads.
func InputFromSnapshot(ctx context.Context, b snapshot.Bundle, read artifacts.Reader) (Input, []Issue, error) {
	in := Input{SnapshotID: b.ID}
	var issues []Issue
	lookup := map[string]int{}
	e := visitTable(b, "plugins", true, func(row map[string][]byte) error {
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
	e = visitTable(b, "plugin_relations", true, func(row map[string][]byte) error {
		var r model.PluginRelation
		r.ID = string(row["relation_id"])
		r.SourcePluginID = string(row["source_plugin_id"])
		r.TargetPluginID = string(row["target_plugin_id"])
		r.TargetVersionID = ptr(row["target_version_id"])
		r.Type = string(row["relation_type"])
		r.SortOrder, _ = strconv.Atoi(string(row["sort_order"]))
		r.Status, _ = strconv.Atoi(string(row["status"]))
		r.Data = row["relation_json"]
		r.CreatedBy = string(row["created_by"])
		var err error
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
	e = visitTable(b, "plugin_versions", true, func(row map[string][]byte) error {
		v := model.PluginVersion{ID: string(row["version_id"]), PluginID: string(row["plugin_id"]), Version: string(row["version"]), DeclaredVersion: ptr(row["declared_version"]), Manifest: row["manifest_json"], Package: row["plugin_json"], AttachmentKeys: row["attachment_keys_json"], ManifestHash: string(row["manifest_hash"]), PluginHash: string(row["plugin_hash"]), Relations: row["relations_json"]}
		in.Versions = append(in.Versions, v)
		return nil
	})
	if e != nil {
		return in, nil, e
	}
	e = visitTable(b, "plugin_version_releases", false, func(row map[string][]byte) error {
		in.Releases = append(in.Releases, model.PluginVersionRelease{PluginID: string(row["plugin_id"]), VersionID: string(row["version_id"]), VersionLabel: string(row["version_label"])})
		return nil
	})
	if e != nil {
		return in, nil, e
	}
	e = visitTable(b, "plugin_review_requests", false, func(row map[string][]byte) error {
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
		if r.Plugin.DeletedAt != nil {
			continue
		}
		files, e := artifacts.Resolve(ctx, &r.Plugin, read, artifacts.DefaultLimits())
		if e != nil {
			issues = append(issues, Issue{r.Plugin.ID, e.Error()})
			continue
		}
		for _, f := range files {
			r.Files = append(r.Files, File{Path: f.Path, MIME: f.MIME, Data: f.Data})
		}
	}
	return in, issues, nil
}

// ValidateSnapshotReferences checks immutable object identities across current
// and historical data. MCP configuration is preserved as authorized by the user.
func ValidateSnapshotReferences(b snapshot.Bundle) error {
	verified, err := snapshot.Load(b.Dir)
	if err != nil || verified.ID != b.ID {
		return fmt.Errorf("snapshot_integrity_failed")
	}
	var read artifacts.Reader
	refBlobs := map[string]bool{}
	if len(b.Manifest.Objects) > 0 {
		read, err = artifacts.BundleReader(b)
		if err != nil {
			return fmt.Errorf("object_inventory_invalid")
		}
		index, e := artifacts.SourceObjectIndex(b)
		if e != nil {
			return fmt.Errorf("object_inventory_invalid")
		}
		for key, file := range index.Entries {
			if key == "skill/ref.json" || strings.HasSuffix(key, "/skill/ref.json") {
				refBlobs[file.Path] = true
			}
		}
	}
	for _, t := range b.Manifest.Tables {
		e := visitTable(b, t.Name, true, func(row map[string][]byte) error {
			combined := map[string]any{}
			for col, raw := range row {
				if raw == nil {
					continue
				}
				var v any
				d := json.NewDecoder(bytes.NewReader(raw))
				d.UseNumber()
				if d.Decode(&v) != nil {
					v = string(raw)
				}
				combined[col] = v
				assertions, e := artifacts.ReferenceAssertions(col, raw, row["attachment_keys_json"])
				if e != nil {
					return fmt.Errorf("frozen_object_metadata_invalid")
				}
				if e = artifacts.VerifyReferenceAssertions(context.Background(), assertions, read); e != nil {
					return fmt.Errorf("frozen_object_metadata_mismatch")
				}
			}
			frozen, _ := json.Marshal(combined)
			assertions, e := artifacts.ReferenceAssertions("snapshot_row", frozen, row["attachment_keys_json"])
			if e != nil {
				return fmt.Errorf("frozen_object_metadata_invalid")
			}
			if e = artifacts.VerifyReferenceAssertions(context.Background(), assertions, read); e != nil {
				return fmt.Errorf("frozen_object_metadata_mismatch")
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	for _, f := range verified.Manifest.Objects {
		if f.Path == "object-index.json" {
			continue
		}
		r, e := artifacts.OpenLocalObject(b.Dir, f.Path)
		if e != nil {
			return fmt.Errorf("archived_object_read_failed")
		}
		if f.Size < 0 || f.Size > artifacts.DefaultLimits().MaxArchiveBytes {
			r.Close()
			return fmt.Errorf("archived_object_budget_exceeded")
		}
		raw, e := io.ReadAll(io.LimitReader(r, f.Size+1))
		r.Close()
		h := sha256.Sum256(raw)
		if e != nil || int64(len(raw)) != f.Size || hex.EncodeToString(h[:]) != f.SHA256 {
			return fmt.Errorf("archived_object_integrity_failed")
		}
		carrierKind := ""
		if refBlobs[f.Path] {
			carrierKind = "skill/ref.json"
		}
		carriers := map[string][]byte{carrierKind: raw}
		if len(raw) >= 4 && bytes.Equal(raw[:4], []byte{'P', 'K', 3, 4}) {
			entries, code, _ := parse.ExtractArchive(bytes.NewReader(raw), int64(len(raw)), artifacts.DefaultLimits().MaxTotalBytes, artifacts.DefaultLimits().MaxFileBytes, artifacts.DefaultLimits().MaxFiles)
			if code != "" {
				return fmt.Errorf("archived_config_zip_invalid")
			}
			carriers = map[string][]byte{}
			for name, content := range entries {
				// Keep the archive entry path as context; ordinary JSON files and
				// mcp.json remain opaque even when fields resemble pointer names.
				carriers[name] = content
			}
		}
		for name, content := range carriers {
			if json.Valid(content) {
				kind := ""
				if name == "skill/ref.json" || strings.HasSuffix(name, "/skill/ref.json") {
					kind = "skill/ref.json"
				}
				if kind == "" {
					continue
				}
				a, e := artifacts.ReferenceAssertions(kind, content, nil)
				if e != nil {
					return fmt.Errorf("frozen_object_metadata_invalid")
				}
				if e = artifacts.VerifyReferenceAssertions(context.Background(), a, read); e != nil {
					return fmt.Errorf("frozen_object_metadata_mismatch")
				}
			}
		}

	}
	return nil
}
