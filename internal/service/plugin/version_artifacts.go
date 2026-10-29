package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/artifactscope"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

// freezeVersionArtifacts detaches saved bytes from mutable caller upload keys.
// It completes before the DB transaction. Version objects are never reclaimed
// on failure: another concurrent save may already reference the same content.
// Caller-supplied mutable keys are retained unless the outer ingestion flow has
// tracked their creation; same-Space access does not prove exclusive ownership.
// Unreferenced objects can be audited later; this PR introduces no physical GC.
// The first optional resource is the already-authorized source row; when a
// visibility change supplies a second resource, that row determines destination
// ownership. Neither context is decoded from caller-provided JSON.
func (s *Service) freezeVersionArtifacts(ctx context.Context, typ model.PluginType, docs *CanonicalDocuments, spaceID string, resources ...*model.Plugin) error {
	if typ == model.PluginTypeConnector {
		return nil
	}
	keys := attachmentKeyMap(docs.AttachmentKeys)
	if keys == nil {
		keys = map[string]string{}
	}
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
			// Rows written during the pre-2.0 window may carry the key only in the
			// inline storage_uri field; the sidecar entry was added by the backfill.
			// Use the same fallback as the read path so such a row can be saved and
			// frozen instead of failing with a generic invalid request.
			key := keys[a.Path]
			if key == "" {
				key = a.StorageURI
			}
			frozen, size, digest, err := s.freezeArtifact(ctx, spaceID, key, s.maxAttachmentBytes, &total, resources...)
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
				frozen, size, digest, err := s.freezeArtifact(ctx, spaceID, key, s.maxArchiveBytes, &total, resources...)
				if err != nil {
					return err
				}

				// Missing legacy metadata establishes a baseline for this NEW
				// save only. It does not validate or rewrite historical releases.
				// Once present, digest and size are mandatory matching assertions.
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

func (s *Service) freezeArtifact(ctx context.Context, spaceID, key string, limit int64, total *int64, resources ...*model.Plugin) (string, int64, string, error) {
	if !artifactWriteScope(spaceID, resources...)(key) || s.storage == nil {
		return "", 0, "", ErrInvalidRequest
	}
	writer, ok := s.storage.(storage.ImmutableWriter)
	if !ok {
		return "", 0, "", fmt.Errorf("storage does not support immutable artifacts")
	}
	// The cumulative budget is checked before clamping: a negative limit would
	// reach io.LimitReader and silently turn into a zero-byte read.
	remaining := s.maxArchiveBytes - *total
	if remaining <= 0 {
		return "", 0, "", ErrTooLarge
	}
	if remaining < limit {
		limit = remaining
	}
	// StatObject classifies a genuinely absent source for both drivers; GetObject
	// wraps the driver error and cannot be relied on for errors.Is.
	if _, statErr := s.storage.StatObject(ctx, key); statErr != nil {
		if errors.Is(statErr, storage.ErrObjectNotFound) {
			return "", 0, "", fmt.Errorf("%w: source artifact is missing", ErrIntegrity)
		}
		return "", 0, "", fmt.Errorf("stat version artifact: %w", statErr)
	}
	body, err := s.storage.GetObject(ctx, key)
	if err != nil {
		return "", 0, "", fmt.Errorf("read version artifact: %w", err)
	}
	defer body.Close()

	// Spool to a temp file while hashing: a bounded artifact then needs one disk
	// copy instead of two full in-memory buffers (source plus frozen copy). Local
	// storage keeps the spool under its own root; remote storage falls back to a
	// 0600 system temp file.
	tmp, err := s.artifactTempFile()
	if err != nil {
		return "", 0, "", err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(body, limit+1))
	if err != nil {
		return "", 0, "", err
	}
	if n > limit {
		return "", 0, "", ErrTooLarge
	}
	*total += n
	hash := hex.EncodeToString(hasher.Sum(nil))
	digest := "sha256:" + hash
	prefix := approvedAttachmentPrefix(spaceID)
	if len(resources) > 0 && resources[0] != nil {
		prefix = artifactscope.Prefix(resources[len(resources)-1])
	}
	if prefix == "" {
		return "", 0, "", ErrInvalidRequest
	}
	frozen := prefix + "versions/" + hash
	if key == frozen {
		return frozen, n, digest, nil
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", 0, "", err
	}
	created, err := writer.PutImmutable(ctx, frozen, tmp, n, "application/octet-stream")
	if err != nil {
		return "", 0, "", err
	}
	// The interface contract makes created=false mean "already existed"; only then
	// is verification needed. A freshly created object is the bytes we just wrote.
	if !created {
		if err := s.verifyFrozenArtifact(ctx, frozen, n, hash); err != nil {
			return "", 0, "", err
		}
	}
	return frozen, n, digest, nil
}

// scratchDirProvider is implemented by storage backends that own a local
// filesystem root. Spooling there keeps tenant artifacts inside that protected
// tree instead of the shared system temp directory.
type scratchDirProvider interface {
	ScratchDir() string
}

func (s *Service) artifactTempFile() (*os.File, error) {
	if p, ok := s.storage.(scratchDirProvider); ok {
		if dir := p.ScratchDir(); dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
			return os.CreateTemp(dir, "artifact-*")
		}
	}
	// Remote storage has no local root; os.CreateTemp creates a 0600 file, but it
	// still lives in the operator's temp dir. Deployments can point TMPDIR at a
	// protected volume if that is not acceptable.
	return os.CreateTemp("", "octo-version-artifact-*")
}

// verifyFrozenArtifact compares an existing immutable object with the digest and
// size we just computed, streaming instead of buffering the whole object.
func (s *Service) verifyFrozenArtifact(ctx context.Context, key string, size int64, hash string) error {
	body, err := s.storage.GetObject(ctx, key)
	if err != nil {
		return fmt.Errorf("%w: frozen artifact is unreadable: %v", ErrIntegrity, err)
	}
	defer body.Close()
	hasher := sha256.New()
	n, err := io.Copy(hasher, io.LimitReader(body, size+1))
	if err != nil {
		return fmt.Errorf("%w: frozen artifact is unreadable: %v", ErrIntegrity, err)
	}
	if n != size || hex.EncodeToString(hasher.Sum(nil)) != hash {
		return ErrIntegrity
	}
	return nil
}

func (s *Service) readArtifactBytes(ctx context.Context, key string, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, ErrTooLarge
	}
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
