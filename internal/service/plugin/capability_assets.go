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

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
)

func (b *capabilityBuilder) text(value string) error {
	return b.textAt("definition", "", "", value)
}

func (b *capabilityBuilder) textAt(field, skillID, filePath, value string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return installationInvalid(field, "text_only")
	}
	if int64(len(value)) > b.remaining {
		return installationContentTooLarge(field, skillID, filePath)
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
		if err := b.textAt("definition.skills.files", p.ID, clean, content); err != nil {
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
		data, err := b.object(ctx, p, key, b.service.maxArchiveBytes, wantSize, wantHash)
		if err != nil {
			if err == ErrTooLarge {
				return nil, ErrIntegrity
			}
			return nil, err
		}
		entries, code, _ := parse.ExtractArchive(
			bytes.NewReader(data), int64(len(data)), b.service.maxArchiveBytes,
			int64(fleet.MaxCapabilityRequestBytes), int64(fleet.MaxCapabilityRequestBytes), -1,
		)
		if code == "FILE_TOO_LARGE" {
			return nil, installationContentTooLarge("definition.skills.files", p.ID, "skill/package.zip")
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
			data, err := b.object(ctx, p, ref.ObjectKey, b.remaining, 0, "")
			if err != nil {
				if err == ErrTooLarge {
					return nil, installationContentTooLarge("definition.skills.files", p.ID, "SKILL.md")
				}
				return nil, err
			}
			if err := b.textAt("definition.skills.files", p.ID, "SKILL.md", string(data)); err != nil {
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
		data, err := b.object(ctx, p, ref.ObjectKey, b.remaining, 0, "")
		if err != nil {
			if err == ErrTooLarge {
				return nil, installationContentTooLarge("definition.skills.files", p.ID, "SKILL.md")
			}
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
			data, err := b.object(ctx, p, key, b.remaining, attachment.ContentSize, attachment.ContentHash)
			if err != nil {
				if err == ErrTooLarge {
					return nil, installationContentTooLarge("definition.skills.files", p.ID, attachment.Path)
				}
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
	if b.service.storage == nil || !trustedPluginArtifactKey(key, p) {
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
