package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

// freezeVersionArtifacts detaches saved bytes from mutable caller upload keys.
// It completes before the DB transaction. Version objects are never reclaimed
// on failure: another concurrent save may already reference the same content.
// Unreferenced objects can be audited later; this PR introduces no physical GC.
func (s *Service) freezeVersionArtifacts(ctx context.Context, typ model.PluginType, docs *CanonicalDocuments, spaceID string) error {
	if typ == model.PluginTypeConnector {
		return nil
	}
	keys := attachmentKeyMap(docs.AttachmentKeys)
	var total int64
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(docs.Package, &doc); err != nil {
		return err
	}
	var attachments []map[string]json.RawMessage
	if raw, ok := doc["attachments"]; ok {
		if err := json.Unmarshal(raw, &attachments); err != nil {
			return err
		}
	}
	changed := false
	for _, att := range attachments {
		var a attachmentView
		raw, _ := json.Marshal(att)
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if a.ContentType == "storage" {
			frozen, size, digest, err := s.freezeArtifact(ctx, spaceID, keys[a.Path], s.maxAttachmentBytes, &total)
			if err != nil {
				return err
			}
			if a.ContentSize != size || a.ContentHash != digest {
				return ErrIntegrity
			}
			keys[a.Path] = frozen
		}
		// Older native packages keep the real markdown/archive in a JSON pointer.
		// Freeze those bytes too; preserving only this inline descriptor would leave
		// its historical files mutable. Unknown legacy roots still fail Space checks.
		if a.ContentType == "raw" && a.Path == "skill/ref.json" {
			var ref map[string]json.RawMessage
			if err := json.Unmarshal([]byte(a.RawContent), &ref); err != nil {
				return ErrInvalidRequest
			}
			for _, field := range []string{"object_key", "zip_object_key", "file_url"} {
				var key string
				if value, ok := ref[field]; ok {
					if json.Unmarshal(value, &key) != nil {
						return ErrInvalidRequest
					}
				}
				if key == "" {
					continue
				}
				frozen, size, digest, err := s.freezeArtifact(ctx, spaceID, key, s.maxArchiveBytes, &total)
				if err != nil {
					return err
				}

				var expectedHash string
				var expectedSize int64
				if value, ok := ref[field+"_hash"]; ok {
					if json.Unmarshal(value, &expectedHash) != nil || expectedHash != digest {
						return ErrIntegrity
					}
				}
				if value, ok := ref[field+"_size"]; ok {
					if json.Unmarshal(value, &expectedSize) != nil || expectedSize != size {
						return ErrIntegrity
					}
				}
				ref[field], _ = json.Marshal(frozen)
				ref[field+"_size"], _ = json.Marshal(size)
				ref[field+"_hash"], _ = json.Marshal(digest)
				changed = true
			}
			if changed {
				value, _ := json.Marshal(ref)
				att["raw_content"], _ = json.Marshal(string(value))
			}
		}
	}
	if len(keys) > 0 {
		raw, err := json.Marshal(keys)
		if err != nil {
			return err
		}
		docs.AttachmentKeys = raw
	}
	if changed {
		doc["attachments"], _ = json.Marshal(attachments)
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		docs.Package, err = libplugin.CanonicalJSON(raw)
		if err != nil {
			return err
		}
		docs.PluginHash, err = libplugin.ComputePluginHash(docs.Manifest, docs.Package)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) freezeArtifact(ctx context.Context, spaceID, key string, limit int64, total *int64) (string, int64, string, error) {
	if !safeObjectSegment.MatchString(spaceID) || !validReferencedObjectKey(key, spaceID) || s.storage == nil {
		return "", 0, "", ErrInvalidRequest
	}
	writer, ok := s.storage.(storage.ImmutableWriter)
	if !ok {
		return "", 0, "", fmt.Errorf("storage does not support immutable artifacts")
	}
	if remaining := s.maxArchiveBytes - *total; remaining < limit {
		limit = remaining
	}
	data, err := s.readArtifactBytes(ctx, key, limit)
	if err != nil {
		return "", 0, "", err
	}
	size := int64(len(data))
	*total += size
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	digest := "sha256:" + hash
	frozen := approvedAttachmentPrefix(spaceID) + "versions/" + hash
	if key != frozen {
		if _, err = writer.PutImmutable(ctx, frozen, bytes.NewReader(data), size, "application/octet-stream"); err != nil {
			return "", 0, "", err
		}
		existing, err := s.readArtifactBytes(ctx, frozen, limit)
		if err != nil {
			return "", 0, "", err
		}
		if !bytes.Equal(existing, data) {
			return "", 0, "", ErrIntegrity
		}
	}
	return frozen, size, digest, nil
}

func (s *Service) readArtifactBytes(ctx context.Context, key string, limit int64) ([]byte, error) {
	body, err := s.storage.GetObject(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read version artifact: %w", err)
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}
