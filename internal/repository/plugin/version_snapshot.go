package plugin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

// ErrLegacySnapshotArtifacts reports a snapshot whose external files were not
// frozen into this resource's immutable namespace. Approving such a request
// would either fail on missing inventory metadata or, worse, record a mutable
// upload key as historical content. Both are refused at the approval boundary;
// the applicant cancels and resubmits so the normal freeze path runs.
var ErrLegacySnapshotArtifacts = errors.New("legacy snapshot artifacts require resubmission")

// frozenArtifactKey is the approval-boundary invariant: every external file a
// version records must already live in the resource's own immutable namespace.
// It is intentionally stricter than IsImmutableArtifactKey — a key from another
// Space's versions prefix must not become historical content either.
func frozenArtifactKey(key string, spaceID *string) bool {
	if key == "" || spaceID == nil || *spaceID == "" {
		return false
	}
	return strings.HasPrefix(key, "plugins/"+*spaceID+"/attachments/versions/") && storage.IsImmutableArtifactKey(key)
}

// snapshotArtifacts describes bytes without copying inline content out of the
// immutable package. Every external file must already be frozen in this
// resource's namespace; a legacy or mutable key is refused instead of being
// recorded as historical content.
func snapshotArtifacts(p model.Plugin) (json.RawMessage, error) {
	var doc struct {
		Attachments []struct {
			Path        string `json:"path"`
			ContentType string `json:"content_type"`
			RawContent  string `json:"raw_content"`
			Size        int64  `json:"content_size"`
			Hash        string `json:"content_hash"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(p.Package, &doc); err != nil {
		return nil, err
	}
	keys := map[string]string{}
	if len(p.AttachmentKeys) > 0 {
		if err := json.Unmarshal(p.AttachmentKeys, &keys); err != nil {
			return nil, err
		}
	}
	out := make([]model.PluginVersionArtifact, 0, len(doc.Attachments))
	seen := map[string]bool{}
	for _, a := range doc.Attachments {
		if a.Path == "" || a.Path == "." || a.Path == ".." || path.Clean(a.Path) != a.Path || strings.HasPrefix(a.Path, "/") || strings.HasPrefix(a.Path, "../") || strings.ContainsAny(a.Path, "\\\x00") || seen[a.Path] {
			return nil, errors.New("invalid snapshot artifact path")
		}
		seen[a.Path] = true
		f := model.PluginVersionArtifact{Path: a.Path, ContentType: a.ContentType, Size: a.Size, Digest: a.Hash}
		switch a.ContentType {
		case "raw":
			sum := sha256.Sum256([]byte(a.RawContent))
			f.Size = int64(len(a.RawContent))
			f.Digest = "sha256:" + hex.EncodeToString(sum[:])
			if a.Path == "skill/ref.json" {
				var ref map[string]json.RawMessage
				if err := json.Unmarshal([]byte(a.RawContent), &ref); err != nil {
					return nil, err
				}
				for _, field := range []string{"object_key", "zip_object_key", "file_url"} {
					var key, digest string
					var size int64
					_ = json.Unmarshal(ref[field], &key)
					if key == "" {
						continue
					}
					_ = json.Unmarshal(ref[field+"_size"], &size)
					_ = json.Unmarshal(ref[field+"_hash"], &digest)
					if !frozenArtifactKey(key, p.SpaceID) || size < 0 || len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") {
						return nil, ErrLegacySnapshotArtifacts
					}
					out = append(out, model.PluginVersionArtifact{Path: a.Path + "/" + field, ContentType: "storage", ObjectKey: key, Size: size, Digest: digest})
				}
			}
		case "storage":
			f.ObjectKey = keys[a.Path]
			if !frozenArtifactKey(f.ObjectKey, p.SpaceID) || f.Size < 0 || len(f.Digest) != 71 || !strings.HasPrefix(f.Digest, "sha256:") {
				return nil, ErrLegacySnapshotArtifacts
			}
			if _, err := hex.DecodeString(strings.TrimPrefix(f.Digest, "sha256:")); err != nil {
				return nil, ErrLegacySnapshotArtifacts
			}
		default:
			return nil, errors.New("unsupported snapshot artifact type")
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return json.Marshal(out)
}

// GetVersionState reads publication metadata under the existing current access
// predicate. It deliberately does not infer releases from listing/current state.
func (r *Repo) GetVersionState(ctx context.Context, scope Scope, pluginID string) (*model.PluginVersionState, error) {
	where := visibilitySQL
	args := []any{pluginID}
	if scope.Admin {
		where = "1=1"
	} else {
		args = append(args, scope.SpaceID, scope.CallerUID)
	}
	var current, published sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT p.current_version_id,p.published_version_id FROM plugins p WHERE p.plugin_id=? AND p.status=1 AND p.deleted_at IS NULL AND `+where, args...).Scan(&current, &published)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &model.PluginVersionState{CurrentVersionID: nullString(current), PublishedVersionID: nullString(published)}, nil
}
