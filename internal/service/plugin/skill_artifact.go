// Skill artifact reads: SKILL.md text and the packaged zip of a visible skill
// Plugin. Import-created Plugins carry both natively (raw SKILL.md attachment
// + managed-prefix package.zip); backfilled Plugins only carry legacy object
// pointers inside skill/ref.json, which the managed-prefix attachment readers
// cannot serve — these endpoints bridge both shapes behind authentication
// instead of handing out presigned URLs.

package plugin

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	"go.uber.org/zap"
)

// fixedZipModTime stamps every reconstructed zip entry so download output is
// byte-stable (archive/zip requires a MS-DOS time no earlier than 1980).
var fixedZipModTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// maxSkillZipEntries bounds how many attachments a reconstructed download zip
// may contain. plugin_json is already capped at 1 MiB (~7-9k minimal storage
// entries), but an explicit entry cap plus the aggregate-byte cap below closes
// the single-request amplification where thousands of attachments each stream up
// to maxAttachmentBytes from object storage.
const maxSkillZipEntries = 4096

// SkillPackageStream is an authorized skill package emitted lazily: a download
// filename plus a callback that writes the zip bytes. The zip is either a stored
// object copied through or reconstructed from the attachment tree, so its total
// size is unknown ahead of time (no Content-Length).
type SkillPackageStream struct {
	FileName string
	Write    func(io.Writer) error
}

// attachmentView is the read projection of one package attachment. content_size
// and content_hash are stamped at write time for storage attachments (skill_tree
// buildSkillAttachmentTree) so read paths can verify the fetched bytes against
// the recorded digest rather than serving whatever the object store returns.
type attachmentView struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	RawContent  string `json:"raw_content"`
	StorageURI  string `json:"storage_uri"`
	ContentSize int64  `json:"content_size"`
	ContentHash string `json:"content_hash"`
}

// decodePackageAttachments returns every attachment of a package document.
func decodePackageAttachments(pkg json.RawMessage) []attachmentView {
	var doc struct {
		Attachments []attachmentView `json:"attachments"`
	}
	if json.Unmarshal(pkg, &doc) != nil {
		return nil
	}
	return doc.Attachments
}

// skillRefDocument is the persisted skill/ref.json shape shared by backfill
// and import. Top-level backfilled skills carry the legacy zip pointer in
// file_url; embedded snapshot skills carry object_key/zip_object_key.
type skillRefDocument struct {
	FileName     string `json:"file_name"`
	FileSize     int64  `json:"file_size"`
	FileURL      string `json:"file_url"`
	ObjectKey    string `json:"object_key"`
	ZipObjectKey string `json:"zip_object_key"`
}

func (r skillRefDocument) zipKey() string {
	if r.ZipObjectKey != "" {
		return r.ZipObjectKey
	}
	return r.FileURL
}

func (s *Service) skillRef(p *model.Plugin) skillRefDocument {
	var ref skillRefDocument
	if raw, ok := rawAttachmentContent(p.Package, "skill/ref.json"); ok {
		_ = json.Unmarshal([]byte(raw), &ref)
	}
	return ref
}

// SkillMarkdown returns the SKILL.md text of a visible skill Plugin, resolved
// under the caller's tenant scope.
func (s *Service) SkillMarkdown(ctx context.Context, caller Caller, pluginID string) (string, error) {
	if validateCaller(caller) != nil {
		return "", ErrInvalidRequest
	}
	return s.skillMarkdown(ctx, scope(caller), pluginID)
}

// AdminSkillMarkdown returns the SKILL.md text of any skill Plugin by id, resolved
// cross-Space under the admin scope (no X-Space-Id). It reads the same unified
// plugins.plugin_json attachment tree as the tenant read, so an embedded skill's
// SKILL.md is legitimately viewable through this admin surface. Callers reach it
// only through the admin-gated route, so the route gate is the authorization.
func (s *Service) AdminSkillMarkdown(ctx context.Context, caller Caller, pluginID string) (string, error) {
	if strings.TrimSpace(caller.UID) == "" {
		return "", ErrInvalidRequest
	}
	return s.skillMarkdown(ctx, adminScope(caller), pluginID)
}

// skillMarkdown reads a skill Plugin's SKILL.md under the given scope. The
// authoritative document is the object referenced by skill/ref.json when it
// exists (snapshot skills inline only a stub entry file); the inlined raw
// attachment is the fallback for import-created and inline-only packages.
func (s *Service) skillMarkdown(ctx context.Context, sc pluginrepo.Scope, pluginID string) (string, error) {
	storageID, err := parseStorageID(pluginID)
	if err != nil {
		return "", err
	}
	p, err := s.publishedPlugin(ctx, sc, storageID)
	if err != nil {
		return "", mapStoreError(err)
	}
	if p.Type != model.PluginTypeSkill {
		return "", ErrInvalidRequest
	}
	ref := s.skillRef(p)
	if s.storage != nil && trustedArtifactKey(ref.ObjectKey, p.SpaceID) {
		body, err := s.storage.GetObject(ctx, ref.ObjectKey)
		if err == nil {
			defer body.Close()
			// Read limit+1 and fail on overflow rather than silently truncating an
			// oversized object into a partial SKILL.md (B2).
			data, err := io.ReadAll(io.LimitReader(body, s.maxAttachmentBytes+1))
			if err != nil {
				return "", fmt.Errorf("read skill markdown: %w", err)
			}
			if int64(len(data)) > s.maxAttachmentBytes {
				return "", ErrTooLarge
			}
			return string(data), nil
		}
	}
	if raw, ok := rawAttachmentContent(p.Package, "SKILL.md"); ok {
		return raw, nil
	}
	return "", ErrNotFound
}

// OpenSkillPackage resolves an authorized, lazily-streamed skill package under
// the caller's tenant scope.
func (s *Service) OpenSkillPackage(ctx context.Context, caller Caller, pluginID string) (*SkillPackageStream, error) {
	if validateCaller(caller) != nil || s.storage == nil {
		return nil, ErrInvalidRequest
	}
	return s.openSkillPackage(ctx, scope(caller), pluginID)
}

// AdminOpenSkillPackage resolves any skill Plugin's package by id, cross-Space
// under the admin scope (no X-Space-Id). It reconstructs the zip from the same
// unified plugins.plugin_json attachment tree the tenant read serves, so an
// embedded skill's package is legitimately downloadable through this admin
// surface. Callers reach it only through the admin-gated route.
func (s *Service) AdminOpenSkillPackage(ctx context.Context, caller Caller, pluginID string) (*SkillPackageStream, error) {
	if strings.TrimSpace(caller.UID) == "" || s.storage == nil {
		return nil, ErrInvalidRequest
	}
	return s.openSkillPackage(ctx, adminScope(caller), pluginID)
}

// openSkillPackage reconstructs or copies through a skill Plugin's package under
// the given scope. New and expanded skills carry a flat attachment tree with no
// stored zip, so the package is reconstructed on the fly from the attachments;
// legacy backfilled skills that still carry a skill/ref.json or skill/package.zip
// pointer stream the stored object instead. Either way the total size is unknown
// in advance, so the caller streams SkillPackageStream.Write without a
// Content-Length.
func (s *Service) openSkillPackage(ctx context.Context, sc pluginrepo.Scope, pluginID string) (*SkillPackageStream, error) {
	storageID, err := parseStorageID(pluginID)
	if err != nil {
		return nil, err
	}
	p, err := s.publishedPlugin(ctx, sc, storageID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if p.Type != model.PluginTypeSkill {
		return nil, ErrInvalidRequest
	}
	ref := s.skillRef(p)

	// Legacy shape: a resolvable stored zip pointer streams the stored object.
	if key, ok := s.legacyZipKey(p, ref); ok {
		info, err := s.storage.StatObject(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("stat skill package: %w", err)
		}
		if info.Size <= 0 || info.Size > s.maxArchiveBytes {
			return nil, ErrTooLarge
		}
		fileName := strings.TrimSpace(ref.FileName)
		if fileName == "" {
			fileName = p.Name + ".zip"
		}
		s.trackDownload(ctx, p.ID)
		return &SkillPackageStream{FileName: fileName, Write: func(w io.Writer) error {
			body, err := s.storage.GetObject(ctx, key)
			if err != nil {
				return err
			}
			defer body.Close()
			_, err = io.CopyN(w, body, info.Size)
			return err
		}}, nil
	}

	// Tree shape: reconstruct the zip from the attachment tree. Legacy skills
	// whose only attachments are unresolved pointers have no real files to serve.
	attachments := decodePackageAttachments(p.Package)
	real := 0
	for _, a := range attachments {
		if a.Path != "skill/ref.json" && a.Path != "skill/package.zip" {
			real++
		}
	}
	if real == 0 {
		return nil, ErrNotFound
	}
	s.trackDownload(ctx, p.ID)
	return &SkillPackageStream{FileName: p.Name + ".zip", Write: func(w io.Writer) error {
		return s.writeSkillZip(ctx, p, attachments, w)
	}}, nil
}

// legacyZipKey returns the stored-zip object key for a pre-expansion skill: the
// managed skill/package.zip storage attachment, else the legacy pointer in
// skill/ref.json. Expanded and freshly imported skills carry neither and fall
// through to attachment-tree reconstruction.
func (s *Service) legacyZipKey(p *model.Plugin, ref skillRefDocument) (string, bool) {
	if managed, ok := storageAttachmentKey(p, "skill/package.zip"); ok && p.SpaceID != nil && validReferencedObjectKey(managed, *p.SpaceID) {
		return managed, true
	}
	if trustedArtifactKey(ref.zipKey(), p.SpaceID) {
		return ref.zipKey(), true
	}
	return "", false
}

// migrationZipKey mirrors legacyZipKey but additionally trusts the read-only
// legacy roots. It is used ONLY by ExpandSkillPackage (the server-driven
// expand-skills migration), never on a caller-facing request.
func (s *Service) migrationZipKey(p *model.Plugin, ref skillRefDocument) (string, bool) {
	if managed, ok := storageAttachmentKey(p, "skill/package.zip"); ok && p.SpaceID != nil && validReferencedObjectKey(managed, *p.SpaceID) {
		return managed, true
	}
	if migrationArtifactKey(ref.zipKey(), p.SpaceID) {
		return ref.zipKey(), true
	}
	return "", false
}

// writeSkillZip assembles a zip from the attachment tree in sorted-path order
// with a fixed modification time (stable output). Raw attachments write their
// inline bytes; storage attachments stream the referenced object, but only when
// the key sits in this plugin's own managed prefix. Any lingering legacy pointer
// attachments are skipped. Both the entry count (maxSkillZipEntries) and the
// aggregate reconstructed size (maxArchiveBytes) are bounded so a single
// download cannot amplify into an unbounded object-storage read.
func (s *Service) writeSkillZip(ctx context.Context, p *model.Plugin, attachments []attachmentView, w io.Writer) error {
	sorted := make([]attachmentView, len(attachments))
	copy(sorted, attachments)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	zw := zip.NewWriter(w)
	entries := 0
	var total int64
	// Storage attachments carry their object key in the row's sidecar map (the
	// 2.0 package no longer holds storage_uri inline), resolved here by path. A
	// row not yet migrated to the sidecar falls back to the inline storage_uri.
	keys := attachmentKeyMap(p.AttachmentKeys)
	for _, a := range sorted {
		if a.Path == "skill/ref.json" || a.Path == "skill/package.zip" {
			continue
		}
		storageKey := keys[a.Path]
		if storageKey == "" {
			storageKey = a.StorageURI
		}
		// Validate a storage key BEFORE creating the entry (Q11'): an untrusted or
		// cross-Space reference is skipped entirely rather than left as a silently
		// empty file in the delivered archive.
		if a.ContentType != "raw" && (p.SpaceID == nil || !validReferencedObjectKey(storageKey, *p.SpaceID)) {
			continue
		}
		if entries >= maxSkillZipEntries {
			return ErrTooLarge
		}
		entries++
		header := &zip.FileHeader{Name: a.Path, Method: zip.Deflate, Modified: fixedZipModTime}
		fw, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		if a.ContentType == "raw" {
			total += int64(len(a.RawContent))
			if total > s.maxArchiveBytes {
				return ErrTooLarge
			}
			if _, err := io.WriteString(fw, a.RawContent); err != nil {
				return err
			}
			continue
		}
		// Bound each object read by both the per-attachment cap and the remaining
		// aggregate budget, so the running total can never exceed maxArchiveBytes.
		limit := s.maxAttachmentBytes
		if remaining := s.maxArchiveBytes - total; remaining < limit {
			limit = remaining
		}
		body, err := s.storage.GetObject(ctx, storageKey)
		if err != nil {
			return err
		}
		// Hash the streamed bytes as they are written so the reconstructed entry can
		// be checked against the digest recorded at publish time (B2): a corrupted
		// or replaced object must not be served under an immutable version.
		hasher := sha256.New()
		n, err := io.CopyN(io.MultiWriter(fw, hasher), body, limit+1)
		body.Close()
		if err != nil && err != io.EOF {
			return err
		}
		total += n
		if total > s.maxArchiveBytes || n > s.maxAttachmentBytes {
			return ErrTooLarge
		}
		if a.ContentSize > 0 && a.ContentSize != n {
			return ErrIntegrity
		}
		if a.ContentHash != "" && a.ContentHash != "sha256:"+hex.EncodeToString(hasher.Sum(nil)) {
			return ErrIntegrity
		}
	}
	return zw.Close()
}

// trackDownload bumps the plugin download counter; best-effort and detached
// from the request context, mirroring trackInstall.
func (s *Service) trackDownload(ctx context.Context, pluginID string) {
	if s.metrics == nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), trackTimeout)
	defer cancel()
	if err := s.metrics.TrackDownload(cctx, "plugin", pluginID); err != nil {
		logging.Warn("download_metric_track_failed",
			zap.String("operation", "plugin.download.track"),
			zap.String("resource_id", pluginID),
			logging.ErrorField(err),
		)
	}
}

// artifactKeyShape rejects an object key on the syntactic grounds shared by
// every trust check: empty/oversized, backslash/NUL/?/# , a scheme (`://`), a
// leading slash, or a non-idempotent path.Clean. It says nothing about which
// namespace the key is allowed in — the caller-facing and migration resolvers
// add that.
func artifactKeyShape(key string) bool {
	if key == "" || len(key) > 512 || strings.ContainsAny(key, "\\\x00?#") || strings.Contains(key, "://") || strings.HasPrefix(key, "/") {
		return false
	}
	clean := path.Clean(key)
	return clean == key && clean != "." && !strings.HasPrefix(clean, "../")
}

// trustedArtifactKey validates an object key read from persisted plugin_json
// before a CALLER-FACING fetch (skill_md, download, install). skill/ref.json is
// caller-writable through upsert, so a pointer is honored only when it sits in
// the plugin's OWN managed Space prefix — every other key, including another
// Space's managed prefix and the shared legacy roots, fails closed. Legacy
// backfilled skills are served only after `-phase=expand-skills` rewrites them
// into own-Space attachment trees; until then their legacy-root pointer is not
// trusted (skill_md falls back to the inline SKILL.md).
func trustedArtifactKey(key string, spaceID *string) bool {
	if !artifactKeyShape(key) {
		return false
	}
	return spaceID != nil && safeObjectSegment.MatchString(*spaceID) && strings.HasPrefix(key, approvedAttachmentPrefix(*spaceID))
}

// migrationArtifactKey is the trust check for the one-time, server-driven
// expand-skills migration ONLY. In addition to the plugin's own managed prefix
// it trusts the read-only legacy roots the deterministic backfill migrated
// (skills/, experts/, squads/), because expanding a legacy skill means reading
// exactly those objects. It is never used on a caller-facing request path, so a
// forged legacy-root pointer cannot be fetched through skill_md/download/install.
func migrationArtifactKey(key string, spaceID *string) bool {
	if trustedArtifactKey(key, spaceID) {
		return true
	}
	if !artifactKeyShape(key) || strings.HasPrefix(key, "plugins/") {
		return false
	}
	return strings.HasPrefix(key, "skills/") || strings.HasPrefix(key, "experts/") || strings.HasPrefix(key, "squads/")
}
