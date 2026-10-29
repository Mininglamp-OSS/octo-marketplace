package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/artifactscope"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/artifacts"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

var errObjects = errors.New("migration object verification failed")

var errSourceObjects = errors.New("source_object_verification_failed")

// VerifySourceObjects verifies the bytes captured when the source was read.
// It never writes objects and reads each distinct source key at most once.
func VerifySourceObjects(ctx context.Context, evidence map[string]SourceEvidence, read artifacts.Reader) error {
	ids := make([]string, 0, len(evidence))
	for id := range evidence {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	expected := map[string]SourceObject{}
	var objects []SourceObject
	for _, id := range ids {
		refs := append([]SourceObject(nil), evidence[id].Objects...)
		sort.Slice(refs, func(i, j int) bool { return refs[i].Key < refs[j].Key })
		for _, ref := range refs {
			if ref.Key == "" || ref.Size < 0 || len(ref.Hash) != 64 || strings.ToLower(ref.Hash) != ref.Hash {
				return errSourceObjects
			}
			if _, err := hex.DecodeString(ref.Hash); err != nil {
				return errSourceObjects
			}
			if prev, ok := expected[ref.Key]; ok {
				if prev.Hash != ref.Hash || prev.Size != ref.Size {
					return errSourceObjects
				}
				continue
			}
			expected[ref.Key] = ref
			objects = append(objects, ref)
		}
	}
	if len(objects) > 0 && read == nil {
		return errSourceObjects
	}
	for _, ref := range objects {
		data, err := read(ctx, ref.Key, ref.Size)
		if err != nil || int64(len(data)) != ref.Size || digest(data) != ref.Hash {
			return errSourceObjects
		}
	}
	return nil
}

// PublishObjects creates only the new plan's immutable objects in the current
// store. Existing historical references are left in that same store unchanged.
func PublishObjects(ctx context.Context, p Plan, store storage.Storage) error {
	files, err := plannedObjects(p)
	if err != nil {
		return err
	}
	// Icons remain in their existing globally uploaded namespace. Verify but
	// never overwrite them or republish them through the version-object writer.
	for _, icon := range retainedIcons(p) {
		if err = verifyObject(ctx, store, icon); err != nil {
			return err
		}
	}
	if len(files) == 0 {
		return nil
	}
	writer, ok := store.(storage.ImmutableWriter)
	if !ok {
		return errors.New("migration requires immutable object storage")
	}
	for _, f := range files {
		if _, err = writer.PutImmutable(ctx, f.ObjectKey, bytes.NewReader(f.Data), int64(len(f.Data)), f.MIME); err != nil {
			return errObjects
		}
		if err = verifyObject(ctx, store, f); err != nil {
			return err
		}
	}
	return nil
}

// VerifyObjects reads the new plan's objects without creating or modifying any.
func VerifyObjects(ctx context.Context, p Plan, store storage.Storage) error {
	files, err := plannedObjects(p)
	if err != nil {
		return err
	}
	files = append(files, retainedIcons(p)...)
	for _, f := range files {
		if err = verifyObject(ctx, store, f); err != nil {
			return err
		}
	}
	return nil
}

// ObjectInventoryEntry describes a planned destination object without its body.
type ObjectInventoryEntry struct {
	Key    string `json:"object_key"`
	Hash   string `json:"sha256"`
	Size   int64  `json:"size"`
	Status string `json:"status"`
}

// InspectPlanObjects recomputes the current destination inventory from the
// durable plan. It performs no writes and does not rely on progress journaling.
func InspectPlanObjects(ctx context.Context, p Plan, store storage.Storage) ([]ObjectInventoryEntry, error) {
	files, err := plannedObjects(p)
	if err != nil {
		return nil, errObjects
	}
	files = append(files, retainedIcons(p)...)
	byKey := make(map[string]File, len(files))
	for _, f := range files {
		byKey[f.ObjectKey] = f
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]ObjectInventoryEntry, 0, len(keys))
	for _, key := range keys {
		f := byKey[key]
		entry := ObjectInventoryEntry{Key: key, Hash: digest(f.Data), Size: int64(len(f.Data)), Status: "verification_failed"}
		if store != nil {
			_, statErr := store.StatObject(ctx, key)
			if errors.Is(statErr, storage.ErrObjectNotFound) {
				entry.Status = "missing"
			} else if statErr == nil && verifyObject(ctx, store, f) == nil {
				entry.Status = "verified"
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// Validate every object before the first write, including the metadata consumed
// by version downloads. The destination is derived from the final resource,
// never from a source object's historical key.
func plannedObjects(p Plan) ([]File, error) {
	if len(validateFinalContracts(p)) > 0 {
		return nil, ErrIssues
	}
	if len(p.Issues) > 0 {
		return nil, ErrIssues
	}
	var files []File
	for _, n := range p.Nodes {
		if len(n.Files) == 0 {
			continue
		}
		var artifacts []struct {
			Path   string `json:"path"`
			Type   string `json:"content_type"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
			Key    string `json:"object_key"`
		}
		var side map[string]string
		var pkg struct {
			Attachments []struct {
				Path string `json:"path"`
				Type string `json:"content_type"`
				Size int64  `json:"content_size"`
				Hash string `json:"content_hash"`
			} `json:"attachments"`
		}
		if json.Unmarshal(n.Artifacts, &artifacts) != nil || json.Unmarshal(n.Plugin.AttachmentKeys, &side) != nil || json.Unmarshal(n.Plugin.Package, &pkg) != nil {
			return nil, errObjects
		}
		artifactByPath := map[string]int{}
		for i, a := range artifacts {
			if _, ok := artifactByPath[a.Path]; ok {
				return nil, errObjects
			}
			artifactByPath[a.Path] = i
		}
		attachmentByPath := map[string]int{}
		for i, a := range pkg.Attachments {
			if _, ok := attachmentByPath[a.Path]; ok {
				return nil, errObjects
			}
			attachmentByPath[a.Path] = i
		}
		seen := map[string]bool{}
		stored := map[string]bool{}
		for _, f := range n.Files {
			if seen[f.Path] {
				return nil, errObjects
			}
			seen[f.Path] = true
			if f.ObjectKey == "" {
				continue
			}
			h := digest(f.Data)
			prefix := artifactscope.Prefix(&n.Plugin)
			if prefix == "" || f.ObjectKey != prefix+"versions/"+h || !artifactscope.Allows(&n.Plugin, f.ObjectKey) || !storage.IsImmutableArtifactKey(f.ObjectKey) {
				return nil, errObjects
			}
			ai, ok := artifactByPath[f.Path]
			if !ok {
				return nil, errObjects
			}
			a := artifacts[ai]
			pi, ok := attachmentByPath[f.Path]
			if !ok {
				return nil, errObjects
			}
			att := pkg.Attachments[pi]
			if a.Type != "storage" || a.Key != f.ObjectKey || a.Size != int64(len(f.Data)) || a.Digest != "sha256:"+h || side[f.Path] != f.ObjectKey || att.Type != "storage" || att.Size != int64(len(f.Data)) || att.Hash != "sha256:"+h {
				return nil, errObjects
			}
			stored[f.Path] = true
			files = append(files, f)
		}
		// Extra stored entries would otherwise make version metadata claim objects
		// which this plan never publishes or verifies.
		for _, a := range artifacts {
			if a.Type == "storage" && !stored[a.Path] {
				return nil, errObjects
			}
		}
	}
	return files, nil
}
func verifyObject(ctx context.Context, store storage.Storage, f File) error {
	if store == nil {
		return errObjects
	}
	body, err := store.GetObject(ctx, f.ObjectKey)
	if err != nil {
		return errObjects
	}
	h := sha256.New()
	size, readErr := io.Copy(h, io.LimitReader(body, int64(len(f.Data))+1))
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || size != int64(len(f.Data)) || hex.EncodeToString(h.Sum(nil)) != digest(f.Data) {
		return errObjects
	}
	return nil
}

// VerifyPlanSourceObjects checks all pending checkpoint source assertions before
// any immutable destination objects are published. Repeated references must agree.
func VerifyPlanSourceObjects(ctx context.Context, p Plan, read artifacts.Reader) error {
	all := map[string]SourceEvidence{}
	for _, n := range p.Nodes {
		own, ok := n.Evidence[n.SourceID]
		if !ok || own.RowHash == "" || own.RowHash != n.SourceRowHash || len(own.Tables) != len(sourceTables) {
			return errSourceObjects
		}
		for id, evidence := range n.Evidence {
			all[n.Plugin.ID+":"+id] = evidence
		}
	}
	return VerifySourceObjects(ctx, all, read)
}

func retainedIcons(p Plan) []File {
	var out []File
	seen := map[string]bool{}
	for _, n := range p.Nodes {
		if f := n.IconFile; f != nil && !seen[f.ObjectKey] {
			seen[f.ObjectKey] = true
			out = append(out, *f)
		}
	}
	return out
}
