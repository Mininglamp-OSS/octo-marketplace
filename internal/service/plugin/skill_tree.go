package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

const (
	// skillInlineMaxBytes caps a single file that may be inlined as a raw
	// attachment; larger text files spill to object storage like binaries.
	skillInlineMaxBytes = 128 * 1024
	// skillInlineBudgetBytes bounds the total raw_content bytes inlined into
	// plugin_json, leaving headroom under maxJSONBytes for JSON structure and
	// escaping so the canonicalized package stays within the 1 MiB column cap.
	skillInlineBudgetBytes = 512 * 1024
)

// extMIME maps a lowercase file extension to a fixed media type. It is
// deliberately a static table rather than mime.TypeByExtension so the produced
// attachment bytes are byte-identical across machines (the OS mime database
// varies), which the deterministic backfill/expand invariant depends on.
var extMIME = map[string]string{
	".md": "text/markdown", ".markdown": "text/markdown",
	".json": "application/json", ".txt": "text/plain",
	".sh": "text/x-shellscript", ".py": "text/x-python",
	".js": "text/javascript", ".ts": "text/plain", ".tsx": "text/plain",
	".yaml": "text/yaml", ".yml": "text/yaml", ".toml": "text/plain",
	".html": "text/html", ".css": "text/css", ".csv": "text/csv",
	".xml": "text/xml", ".sql": "text/plain", ".env": "text/plain",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".svg": "image/svg+xml", ".webp": "image/webp",
	".pdf": "application/pdf", ".zip": "application/zip",
}

// buildSkillAttachmentTree turns a skill package zip into a flat attachment tree
// — one attachment per file, rooted at the SKILL.md directory. UTF-8 text small
// enough to fit the inline budget becomes a raw attachment; binary or oversized
// files are uploaded to this Space's managed prefix under a deterministic key
// and referenced as storage attachments. When skillMDOverride is non-nil it
// replaces the SKILL.md bytes (import injects rewritten frontmatter); otherwise
// the zip's own SKILL.md is used. Returns the attachment maps (ready for the
// package $schema wrapper), the storage keys written (for rollback), and the
// rooted paths that spilled to storage (for logging). On any error every object
// written so far is deleted.
//
// budget, when non-nil, is a shared remaining-decompressed-bytes allowance
// threaded across every skill expanded in one container import/reupload: this
// skill's uncompressed tree size is charged against it, and an expansion that
// would drive the container-wide aggregate over budget is rejected with
// ErrTooLarge before any object is uploaded. The per-skill maxArchiveBytes cap is
// unchanged; the budget bounds the aggregate so one small nested archive
// referenced by many skill refs cannot be expanded without limit.
func (s *Service) buildSkillAttachmentTree(ctx context.Context, spaceID, pluginID string, zipData, skillMDOverride []byte, containerBudget *int64) (attachments []map[string]any, uploaded, spilled []string, err error) {
	entries, code, _ := parse.ExtractSkillTree(bytes.NewReader(zipData), int64(len(zipData)), s.maxArchiveBytes, s.maxAttachmentBytes, 0)
	if code != "" {
		return nil, nil, nil, ErrInvalidRequest
	}
	// Charge this skill's decompressed size against the shared container budget
	// before building/uploading anything, so an over-budget expansion fails cleanly
	// without leaving orphan objects.
	if containerBudget != nil {
		var consumed int64
		for _, e := range entries {
			consumed += int64(len(e.Bytes))
		}
		if consumed > *containerBudget {
			return nil, nil, nil, ErrTooLarge
		}
		*containerBudget -= consumed
	}

	// Root every path at the SKILL.md directory so the package is anchored at a
	// top-level "SKILL.md" regardless of a wrapping folder in the source zip.
	skillDir := "."
	for _, e := range entries {
		if parse.IsSkillMDCandidate(e.Path) {
			skillDir = path.Dir(e.Path)
			break
		}
	}
	type item struct {
		path   string
		bytes  []byte
		isText bool
		isMD   bool
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		isMD := parse.IsSkillMDCandidate(e.Path)
		rp := rootRelative(e.Path, skillDir)
		if isMD {
			rp = "SKILL.md"
		}
		norm, ok := normalizedArchivePath(rp)
		if !ok {
			return nil, nil, nil, ErrInvalidRequest
		}
		b := e.Bytes
		if isMD && skillMDOverride != nil {
			b = skillMDOverride
		}
		if isMD && !utf8.Valid(b) {
			// SKILL.md is the required text entry; a non-UTF-8 SKILL.md cannot be
			// a valid raw attachment and would fail the contract downstream.
			return nil, nil, nil, ErrInvalidRequest
		}
		items = append(items, item{path: norm, bytes: b, isText: e.IsText || isMD, isMD: isMD})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].path < items[j].path })

	budget := 0
	for _, it := range items {
		size := len(it.bytes)
		// SKILL.md always inlines; other text inlines while within per-file and
		// aggregate budgets. Everything else is a storage attachment.
		inline := it.isText && (it.isMD || (size <= skillInlineMaxBytes && budget+size <= skillInlineBudgetBytes))
		sum := sha256.Sum256(it.bytes)
		att := map[string]any{
			"path":      it.path,
			"mime_type": attachmentMIME(it.path, it.isText),
		}
		if inline {
			// A raw attachment carries its bytes inline; the 2.0 contract forbids a
			// derived content_size/content_hash on raw content, so neither is set.
			att["content_type"] = "raw"
			att["raw_content"] = string(it.bytes)
			budget += size
		} else {
			// A storage attachment needs a valid managed prefix; an all-text skill
			// never reaches here, so it can be expanded even without a Space.
			if !safeObjectSegment.MatchString(spaceID) {
				s.deleteObjects(ctx, uploaded...)
				return nil, nil, nil, ErrInvalidRequest
			}
			key := deterministicSkillObjectKey(spaceID, pluginID, it.path, it.bytes)
			// Never treat a failed stat as absence, or overwrite a prior
			// version's content-addressed file while trying to repair it.
			_, statErr := s.storage.StatObject(ctx, key)
			if statErr == nil {
				existing, err := s.readArtifactBytes(ctx, key, s.maxAttachmentBytes)
				if err != nil {
					s.deleteObjects(ctx, uploaded...)
					return nil, nil, nil, err
				}
				if !bytes.Equal(existing, it.bytes) {
					s.deleteObjects(ctx, uploaded...)
					return nil, nil, nil, ErrIntegrity
				}
			} else if errors.Is(statErr, storage.ErrObjectNotFound) {
				contentType := attachmentMIME(it.path, it.isText)
				if err := s.storage.PutObject(ctx, key, bytes.NewReader(it.bytes), int64(size), contentType); err != nil {
					s.deleteObjects(ctx, uploaded...)
					return nil, nil, nil, err
				}
				uploaded = append(uploaded, key)
			} else {
				s.deleteObjects(ctx, uploaded...)
				return nil, nil, nil, statErr
			}
			if it.isText {
				spilled = append(spilled, it.path)
			}
			att["content_type"] = "storage"
			att["content_size"] = int64(size)
			att["content_hash"] = "sha256:" + hex.EncodeToString(sum[:])
			att["storage_uri"] = key
		}
		attachments = append(attachments, att)
	}
	return attachments, uploaded, spilled, nil
}

// rootRelative strips the SKILL.md directory prefix from an entry path so the
// tree is anchored at the SKILL.md's directory. Entries outside that directory
// (unusual) keep their path.
func rootRelative(p, dir string) string {
	if dir == "." || dir == "" {
		return p
	}
	return strings.TrimPrefix(p, dir+"/")
}

// deterministicSkillObjectKey derives a stable managed-prefix object key for a
// skill file from (pluginID, rootedPath, content). Including a content digest
// makes the key content-addressed: identical bytes dedupe to one object, and
// two versions whose file at the same path differs get distinct keys instead of
// silently overwriting each other. Re-running import/expand for unchanged bytes
// overwrites the same key, keeping plugin_json reproducible.
func deterministicSkillObjectKey(spaceID, pluginID, rootedPath string, content []byte) string {
	sum := sha256.New()
	sum.Write([]byte(pluginID + "\x00" + rootedPath + "\x00"))
	sum.Write(content)
	name := "skill-" + pluginID + "-" + hex.EncodeToString(sum.Sum(nil))[:16]
	if ext := path.Ext(rootedPath); ext != "" && len(ext) <= 12 && safeExtension(ext) {
		name += strings.ToLower(ext)
	}
	return approvedAttachmentPrefix(spaceID) + name
}

// safeExtension reports whether ext (leading dot included) is limited to the
// characters allowed in a managed object key so the derived key stays valid.
func safeExtension(ext string) bool {
	for _, r := range ext {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.':
		default:
			return false
		}
	}
	return true
}

// attachmentMIME returns a fixed, whitespace-free media type for an attachment
// path (SKILL.md is always text/markdown), defaulting by text/binary class.
func attachmentMIME(p string, isText bool) string {
	if strings.EqualFold(path.Base(p), "SKILL.md") {
		return "text/markdown"
	}
	if mediaType, ok := extMIME[strings.ToLower(path.Ext(p))]; ok {
		return mediaType
	}
	if isText {
		return "text/plain"
	}
	return "application/octet-stream"
}

// ExpandSkillPackage migrates one legacy skill package (SKILL.md stub +
// skill/ref.json pointer, or skill/package.zip storage attachment) into the flat
// attachment tree, returning the canonical package bytes and whether it changed.
// A package already in tree form (no legacy pointer) is returned unchanged. When
// the legacy pointer resolves to a stored zip the tree is rebuilt from it (binary
// files re-uploaded to this Space's managed prefix under deterministic keys);
// when no zip exists the package collapses to a single SKILL.md attachment
// (inline content, or the trusted object_key document). Exported so the
// storage-aware expand-skills migration can reuse the live write-path logic.
// ExpandSkillPackage rewrites a legacy skill package into the 2.0 flat
// attachment tree. It returns the canonical 2.0 package (no storage_uri inline),
// the host-private path->object-key sidecar for any spilled storage attachments
// (nil when all-inline), whether anything changed, and an error. The caller
// persists both the package and the sidecar (attachment_keys_json).
func (s *Service) ExpandSkillPackage(ctx context.Context, spaceID, pluginID string, pkg, keys json.RawMessage) (json.RawMessage, json.RawMessage, bool, error) {
	attachments := decodePackageAttachments(pkg)
	legacy := false
	for _, a := range attachments {
		if a.Path == "skill/ref.json" || a.Path == "skill/package.zip" {
			legacy = true
			break
		}
	}
	if !legacy {
		return pkg, nil, false, nil
	}
	// The repackage phase strips inline storage_uri into the sidecar, so a legacy
	// skill/package.zip key is resolved from `keys` (attachment_keys_json), not the
	// package. Thread it onto the working plugin or migrationZipKey finds nothing
	// and the archive gets rebuilt as a SKILL.md-only stub.
	p := &model.Plugin{Name: pluginID, SpaceID: &spaceID, Package: pkg, AttachmentKeys: keys}
	ref := s.skillRef(p)

	var newAtts []map[string]any
	if key, ok := s.migrationZipKey(p, ref); ok {
		zipData, err := s.getObjectBytes(ctx, key, s.maxArchiveBytes)
		if err != nil {
			return nil, nil, false, err
		}
		// buildSkillAttachmentTree requires a valid Space only if a file must be
		// uploaded to storage; an all-text package expands without one.
		atts, _, _, err := s.buildSkillAttachmentTree(ctx, spaceID, pluginID, zipData, nil, nil)
		if err != nil {
			return nil, nil, false, err
		}
		newAtts = atts
	} else {
		// No stored zip: collapse to a single SKILL.md attachment. The
		// authoritative body is the referenced object (snapshot skills inline only
		// a stub entry file), so prefer ref.ObjectKey and fall back to the inline
		// SKILL.md — matching SkillMarkdown and the backfill's own layout. Reading
		// the object here is safe: the caller write path forbids persisting a
		// legacy-root/cross-Space ref.json (skillRefKeysScoped), so any pointer
		// migrationArtifactKey trusts was written by the backfill.
		var md string
		var ok bool
		if migrationArtifactKey(ref.ObjectKey, p.SpaceID) {
			// A trusted object pointer is authoritative: a read failure (throttle,
			// transient 5xx, timeout) must FAIL this row rather than silently
			// degrading to the one-line stub — expand-skills is one-way, so a
			// fallback here would permanently destroy the real SKILL.md (A3).
			data, err := s.getObjectBytes(ctx, ref.ObjectKey, s.maxAttachmentBytes)
			if err != nil {
				return nil, nil, false, fmt.Errorf("read skill markdown object %s: %w", ref.ObjectKey, err)
			}
			md, ok = string(data), true
		} else {
			md, ok = rawAttachmentContent(pkg, "SKILL.md")
		}
		if !ok || !utf8.ValidString(md) {
			return nil, nil, false, ErrNotFound
		}
		newAtts = []map[string]any{{
			"path":         "SKILL.md",
			"content_type": "raw",
			"mime_type":    "text/markdown",
			"raw_content":  md,
		}}
	}
	raw, err := json.Marshal(map[string]any{"$schema": packageSchema, "attachments": newAtts})
	if err != nil {
		return nil, nil, false, err
	}
	// Split the host object keys out of the built tree so the persisted package is
	// a valid 2.0 document (no storage_uri) and the keys land in the sidecar.
	stripped, keys, err := splitStorageKeys(raw, spaceID)
	if err != nil {
		return nil, nil, false, err
	}
	canonical, err := libplugin.CanonicalJSON(stripped)
	if err != nil {
		return nil, nil, false, err
	}
	// Validate the expanded package against the full lib package-shape rules
	// (unique attachment paths, required SKILL.md) before the one-way migration
	// commits it: buildSkillAttachmentTree strips the skillDir prefix, which can
	// collapse two distinct archive entries onto one normalized path — a row the
	// API would then permanently refuse on the next upsert (P1-4). Fail the row so
	// expand-skills records a skip instead of writing an unrepairable package.
	if _, err := libplugin.DecodePackage(libplugin.TypeSkill, canonical); err != nil {
		return nil, nil, false, fmt.Errorf("expanded skill package rejected by lib: %w", err)
	}
	return canonical, keys, true, nil
}

// WouldTruncateSkillPackage reports whether ExpandSkillPackage, given these keys,
// would collapse a legacy-pointer package to a SKILL.md-only stub because it
// cannot resolve the real archive/object — i.e. expanding it would discard the
// package's files. It mirrors ExpandSkillPackage's own resolution decision
// (migrationZipKey, then migrationArtifactKey on the ref object) rather than a
// path-shape heuristic. A package whose pointer resolves, or that carries no real
// pointer to lose (a genuine inline SKILL.md snapshot), does NOT truncate. Used by
// the backfill to fail-closed skip audit snapshots that have no resolvable key
// instead of rebuilding them as a stub.
func (s *Service) WouldTruncateSkillPackage(spaceID, pluginID string, pkg, keys json.RawMessage) bool {
	hasZipAttachment := false
	for _, a := range decodePackageAttachments(pkg) {
		if a.Path == "skill/package.zip" {
			hasZipAttachment = true
			break
		}
	}
	p := &model.Plugin{Name: pluginID, SpaceID: &spaceID, Package: pkg, AttachmentKeys: keys}
	ref := s.skillRef(p)
	if _, ok := s.migrationZipKey(p, ref); ok {
		return false // the managed zip resolves — buildSkillAttachmentTree expands the real files
	}
	// The zip pointer did NOT resolve. If there WAS one (a package.zip attachment
	// or a ref zip key), ExpandSkillPackage takes the SKILL.md-stub else-branch and
	// drops the archive's other files — so fail closed BEFORE the ObjectKey
	// shortcut. A resolvable ref.json ObjectKey only supplies the stub's SKILL.md
	// body; it does NOT recover the lost zip files, so it must not green-light the
	// row.
	if hasZipAttachment || ref.zipKey() != "" {
		return true
	}
	if migrationArtifactKey(ref.ObjectKey, p.SpaceID) {
		return false // no zip to lose; the referenced object is the authoritative SKILL.md
	}
	// No zip and no resolvable object: a non-empty (untrusted/unresolvable) object
	// pointer would be lost to an inline stub; a pure inline snapshot loses nothing.
	return ref.ObjectKey != ""
}

// getObjectBytes reads an object fully, capped at limit bytes.
func (s *Service) getObjectBytes(ctx context.Context, key string, limit int64) ([]byte, error) {
	body, err := s.storage.GetObject(ctx, key)
	if err != nil {
		return nil, err
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
