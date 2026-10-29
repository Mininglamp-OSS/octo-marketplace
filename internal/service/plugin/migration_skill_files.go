package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

// migrationSkillReader translates only server-validated, read-only legacy keys.
// It is local to this offline helper; caller-facing installation never gains
// permission to fetch another Space or the shared legacy namespace.
type migrationSkillReader struct {
	storage.Storage
	keys map[string]string
}

func (r migrationSkillReader) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	original, ok := r.keys[key]
	if !ok || r.Storage == nil {
		return nil, ErrIntegrity
	}
	return r.Storage.GetObject(ctx, original)
}

// ResolveMigrationSkillFiles reads the current persisted Skill without changing
// its identity, package, attachment sidecar or version history. The caller must
// authorize the offline migration dataset; this helper is not an HTTP API.
//
// Managed keys must belong to the original Space. Only the existing trusted
// offline legacy roots (skills/, experts/, squads/) may additionally be read.
// URLs are never followed. Files use the exact installation path, UTF-8, digest,
// entry-count and size checks: <=51 files, <=1 MiB each, <=16 MiB total. Missing
// or corrupt sources fail closed instead of silently retaining a stub SKILL.md.
func ResolveMigrationSkillFiles(ctx context.Context, reader storage.Storage, p *model.Plugin) (map[string]string, error) {
	if p == nil || p.Type != model.PluginTypeSkill || len(p.Package) == 0 || len(p.Package) > maxJSONBytes || model.ValidateUniqueJSONFields(p.Package) != nil {
		return nil, ErrInvalidRequest
	}
	var doc struct {
		Attachments []attachmentView `json:"attachments"`
	}
	if json.Unmarshal(p.Package, &doc) != nil || len(doc.Attachments) == 0 || len(doc.Attachments) > 53 {
		return nil, ErrInvalidRequest
	}
	var sidecar map[string]string
	if len(p.AttachmentKeys) > 0 && string(p.AttachmentKeys) != "null" {
		if model.ValidateUniqueJSONFields(p.AttachmentKeys) != nil || json.Unmarshal(p.AttachmentKeys, &sidecar) != nil {
			return nil, ErrInvalidRequest
		}
	}
	// A private in-memory namespace lets the proven caller-facing file parser
	// perform its normal checks while the reader enforces the original key scope.
	// No virtual key is persisted or written to object storage.
	virtualSpace := "migration-preview"
	resolvedKeys := map[string]string{}
	authorize := func(key string) (string, error) {
		if !trustedPluginArtifactKey(key, p) && !migrationArtifactKey(key, p.SpaceID) {
			return "", ErrInvalidRequest
		}
		sum := sha256.Sum256([]byte(key))
		virtual := approvedAttachmentPrefix(virtualSpace) + hex.EncodeToString(sum[:])
		resolvedKeys[virtual] = key
		return virtual, nil
	}
	attachments := make([]attachmentView, len(doc.Attachments))
	copy(attachments, doc.Attachments)
	keys := map[string]string{}
	seen := map[string]bool{}
	for i := range attachments {
		a := &attachments[i]
		if seen[a.Path] {
			return nil, ErrInvalidRequest
		}
		seen[a.Path] = true
		if a.Path == "skill/ref.json" {
			if a.ContentType != "raw" || model.ValidateUniqueJSONFields([]byte(a.RawContent)) != nil {
				return nil, ErrInvalidRequest
			}
			var ref skillRefDocument
			if json.Unmarshal([]byte(a.RawContent), &ref) != nil {
				return nil, ErrInvalidRequest
			}
			if ref.ObjectKey == "" && ref.zipKey() == "" {
				hasArchive := false
				for _, candidate := range attachments {
					if candidate.Path == "skill/package.zip" && candidate.ContentType == "storage" {
						hasArchive = true
					}
				}
				if !hasArchive {
					return nil, ErrInvalidRequest
				}
			}
			for _, field := range []*string{&ref.ObjectKey, &ref.ZipObjectKey, &ref.FileURL} {
				if *field != "" {
					mapped, err := authorize(*field)
					if err != nil {
						return nil, err
					}
					*field = mapped
				}
			}
			raw, err := json.Marshal(ref)
			if err != nil {
				return nil, ErrInvalidRequest
			}
			a.RawContent = string(raw)
		}
		if a.ContentType == "storage" {
			original := sidecar[a.Path]
			if original == "" {
				original = a.StorageURI
			}
			mapped, err := authorize(original)
			if err != nil {
				return nil, err
			}
			keys[a.Path] = mapped
			a.StorageURI = ""
		}
	}
	raw, err := json.Marshal(struct {
		Attachments []attachmentView `json:"attachments"`
	}{attachments})
	if err != nil {
		return nil, ErrInvalidRequest
	}
	sidecarRaw, err := json.Marshal(keys)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	clone := *p
	clone.Package = raw
	clone.AttachmentKeys = sidecarRaw
	clone.SpaceID = &virtualSpace
	svc := New(nil, migrationSkillReader{Storage: reader, keys: resolvedKeys})
	builder := capabilityBuilder{service: svc, remaining: maxCapabilityBytes}
	files, err := builder.skillFiles(ctx, &clone)
	if err != nil {
		return nil, err
	}
	if files["SKILL.md"] == "" {
		return nil, ErrInvalidRequest
	}
	return files, nil
}
