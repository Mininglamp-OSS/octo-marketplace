package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
)

func (b *capabilityBuilder) text(value string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return installationInvalid("definition", "text_only")
	}
	if len(value) > 1<<20 || int64(len(value)) > b.remaining {
		return ErrTooLarge
	}
	b.remaining -= int64(len(value))
	return nil
}

func (b *capabilityBuilder) skillFiles(ctx context.Context, p *model.Plugin) (map[string]string, error) {
	files := map[string]string{}
	add := func(name, content string) error {
		clean, ok := normalizedArchivePath(name)
		if !ok || len(clean) > 512 || strings.ContainsRune(clean, ':') {
			return installationInvalid("definition.skills.files", "unsafe_path")
		}
		if clean != "SKILL.md" && strings.EqualFold(clean, "SKILL.md") {
			return installationInvalid("definition.skills.files", "reserved_path")
		}
		for _, segment := range strings.Split(clean, "/") {
			if strings.EqualFold(segment, ".git") {
				return installationInvalid("definition.skills.files", "reserved_path")
			}
		}
		if _, exists := files[clean]; exists {
			return installationInvalid("definition.skills.files", "duplicate_path")
		}
		if clean != "SKILL.md" {
			b.totalFiles++
			if b.totalFiles > 500 {
				return ErrTooLarge
			}
		}
		if len(files) >= 51 {
			return ErrTooLarge
		}
		if err := b.text(content); err != nil {
			return err
		}
		files[clean] = content
		return nil
	}
	ref := b.service.skillRef(p)
	attachments := decodePackageAttachments(p.Package)
	if key, ok := b.service.legacyZipKey(p, ref); ok {
		var wantSize int64
		var wantHash string
		if managedKey, managed := storageAttachmentKey(p, "skill/package.zip"); managed && managedKey == key {
			for _, attachment := range attachments {
				if attachment.Path == "skill/package.zip" {
					wantSize, wantHash = attachment.ContentSize, attachment.ContentHash
				}
			}
		}
		data, err := b.object(ctx, p, key, b.remaining, wantSize, wantHash)
		if err != nil {
			return nil, err
		}
		// Bound decompressed bytes while reading, not only after an entire ZIP
		// has been materialized. ExtractSkillTree applies its supplied size
		// limit only to compressed bytes, with a separate fixed expansion cap.
		entries, code, _ := parse.ExtractArchive(bytes.NewReader(data), int64(len(data)), b.remaining, 1<<20, 51)
		if code == "FILE_TOO_LARGE" {
			return nil, ErrTooLarge
		}
		if code != "" {
			return nil, installationInvalid("definition.skills.files", "invalid_archive")
		}
		dir := "."
		var paths []string
		skillMDCount := 0
		for entryPath := range entries {
			paths = append(paths, entryPath)
			if parse.IsSkillMDCandidate(entryPath) {
				dir = path.Dir(entryPath)
				skillMDCount++
			}
		}
		if skillMDCount != 1 {
			return nil, installationInvalid("definition.skills.files", "invalid_archive")
		}
		sort.Strings(paths)
		for _, entryPath := range paths {
			name := rootRelative(entryPath, dir)
			if parse.IsSkillMDCandidate(entryPath) {
				name = "SKILL.md"
			}
			if err := add(name, string(entries[entryPath])); err != nil {
				return nil, err
			}
		}
		// Snapshot packages may have a stub SKILL.md; the authorized legacy
		// object is the authoritative entry document when present.
		if ref.ObjectKey != "" {
			// The archive has already passed bounded extraction and validation.
			// Replace the entry document's charge instead of counting both copies.
			b.remaining += int64(len(files["SKILL.md"]))
			data, err := b.object(ctx, p, ref.ObjectKey, min(b.remaining, 1<<20), 0, "")
			if err != nil {
				return nil, err
			}
			if err := b.text(string(data)); err != nil {
				return nil, err
			}
			files["SKILL.md"] = string(data)
		}
		return files, nil
	}
	if ref.zipKey() != "" {
		return nil, installationInvalid("definition.skills.files", "unavailable_archive")
	}
	for _, attachment := range attachments {
		if attachment.Path == "skill/package.zip" {
			// A declared but unresolvable managed archive must not silently
			// collapse into its snapshot's stub SKILL.md.
			return nil, installationInvalid("definition.skills.files", "unavailable_archive")
		}
	}
	if ref.ObjectKey != "" {
		data, err := b.object(ctx, p, ref.ObjectKey, min(b.remaining, 1<<20), 0, "")
		if err != nil {
			return nil, err
		}
		if err := add("SKILL.md", string(data)); err != nil {
			return nil, err
		}
	}
	keys := attachmentKeyMap(p.AttachmentKeys)
	for _, attachment := range attachments {
		if attachment.Path == "skill/ref.json" || attachment.Path == "skill/package.zip" {
			continue
		}
		if attachment.Path == "SKILL.md" && ref.ObjectKey != "" {
			continue
		}
		content := attachment.RawContent
		switch attachment.ContentType {
		case "raw":
		case "storage":
			key := keys[attachment.Path]
			if key == "" {
				key = attachment.StorageURI
			}
			data, err := b.object(ctx, p, key, min(b.remaining, 1<<20), attachment.ContentSize, attachment.ContentHash)
			if err != nil {
				return nil, err
			}
			content = string(data)
		default:
			return nil, installationInvalid("definition.skills.files", "unsupported_source")
		}
		if err := add(attachment.Path, content); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// Unlike the legacy best-effort attachment reader, this path fails closed on
// missing/corrupt files so Fleet never commits an accidentally partial Skill.
func (b *capabilityBuilder) object(ctx context.Context, p *model.Plugin, key string, limit, wantSize int64, wantHash string) ([]byte, error) {
	if b.service.storage == nil || p.SpaceID == nil || !validReferencedObjectKey(key, *p.SpaceID) {
		return nil, installationInvalid("definition.skills.files", "unavailable_source")
	}
	if limit < 0 {
		return nil, ErrTooLarge
	}
	body, err := b.service.storage.GetObject(ctx, key)
	if err != nil {
		return nil, ErrIntegrity
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, ErrIntegrity
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	if wantSize > 0 && int64(len(data)) != wantSize {
		return nil, ErrIntegrity
	}
	if wantHash != "" {
		sum := sha256.Sum256(data)
		if wantHash != "sha256:"+hex.EncodeToString(sum[:]) {
			return nil, ErrIntegrity
		}
	}
	return data, nil
}
