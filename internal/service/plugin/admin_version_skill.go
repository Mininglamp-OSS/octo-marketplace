package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"path"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// SkillMarkdownResult is the A23 text result. VersionID is the immutable
// snapshot actually read; the handler must serialize it so a caller can tell
// which version it received instead of inferring "latest" from the label.
type SkillMarkdownResult struct {
	Content   string
	VersionID string
}

// adminVersionSkill resolves the exact snapshot A23 reads. An omitted
// version_id means current, never published: an explicit Admin fallback to the
// public A06/A07 published view would silently serve different bytes.
//
// The nil-version return preserves the pre-version-management Admin behavior:
// when the rollout is disabled there is no snapshot identity, so the handler
// reads the current row and cannot report a version_id. Once the rollout is on,
// a missing current pointer is an explicit integrity/rollout error rather than a
// silent published or empty answer.
func (s *Service) adminVersionSkill(ctx context.Context, caller Caller, pluginID, versionID string) (*model.Plugin, *model.PluginVersion, error) {
	pid, err := parseStorageID(pluginID)
	if err != nil {
		return nil, nil, err
	}
	p, _, err := s.repo.GetWithRelations(ctx, adminScope(caller), pid)
	if err != nil {
		return nil, nil, mapStoreError(err)
	}
	if p.Type != model.PluginTypeSkill {
		return nil, nil, &ReviewFieldError{Field: "plugin_type", Reason: "unsupported_type", Err: ErrInvalidRequest}
	}
	if !s.versionManagement {
		if strings.TrimSpace(versionID) != "" {
			return nil, nil, ErrNotFound
		}
		return p, nil, nil
	}
	if strings.TrimSpace(versionID) == "" {
		if p.CurrentVersionID == nil {
			return nil, nil, ErrVersionBaselineUnavailable
		}
		versionID = *p.CurrentVersionID
	}
	v, err := s.ManagedVersion(ctx, caller, pluginID, versionID, true)
	if err != nil {
		return nil, nil, err
	}
	return p, v, nil
}

// AdminSkillMarkdown returns the SKILL.md text of any skill Plugin by id,
// resolved cross-Space under the admin scope. versionID is optional; the empty
// value resolves the current snapshot. Callers reach it only through the
// admin-gated route, so the route gate is the authorization.
func (s *Service) AdminSkillMarkdown(ctx context.Context, caller Caller, pluginID, versionID string) (*SkillMarkdownResult, error) {
	if strings.TrimSpace(caller.UID) == "" {
		return nil, ErrInvalidRequest
	}
	p, v, err := s.adminVersionSkill(ctx, caller, pluginID, versionID)
	if err != nil {
		return nil, err
	}
	if v == nil {
		// Pre-version-management compatibility: read the current row through the
		// existing admin-safe helper; there is no snapshot id to report.
		content, err := s.skillMarkdown(ctx, adminScope(caller), pluginID)
		if err != nil {
			return nil, err
		}
		return &SkillMarkdownResult{Content: content}, nil
	}
	data, err := s.versionFileBytes(ctx, p, v, "SKILL.md")
	if err != nil {
		return nil, err
	}
	return &SkillMarkdownResult{Content: string(data), VersionID: v.ID}, nil
}

// AdminOpenSkillPackage resolves any skill Plugin's package by id, cross-Space
// under the admin scope. versionID is optional; the empty value resolves the
// current snapshot. The returned stream records the snapshot actually served.
func (s *Service) AdminOpenSkillPackage(ctx context.Context, caller Caller, pluginID, versionID string) (*SkillPackageStream, error) {
	if strings.TrimSpace(caller.UID) == "" {
		return nil, ErrInvalidRequest
	}
	p, v, err := s.adminVersionSkill(ctx, caller, pluginID, versionID)
	if err != nil {
		return nil, err
	}
	if v == nil {
		if s.storage == nil {
			return nil, ErrInvalidRequest
		}
		// Pre-version-management compatibility: current row, no version id.
		return s.openSkillPackage(ctx, adminScope(caller), pluginID)
	}
	stream, err := s.openVersionSkillPackage(ctx, p, v)
	if err != nil {
		return nil, err
	}
	s.trackDownload(ctx, p.ID)
	stream.VersionID = v.ID
	return stream, nil
}

// openVersionSkillPackage builds the ZIP for one exact immutable snapshot. A
// legacy stored ZIP is verified against the historical descriptor before any
// bytes are returned; otherwise the archive is reconstructed from individually
// verified snapshot files. It never falls back to the mutable current package.
func (s *Service) openVersionSkillPackage(ctx context.Context, p *model.Plugin, v *model.PluginVersion) (*SkillPackageStream, error) {
	if _, _, _, ok, err := legacyVersionPointer(v, "skill/package.zip"); err != nil {
		return nil, err
	} else if ok {
		data, err := s.versionFileBytes(ctx, p, v, "skill/package.zip")
		if err != nil {
			return nil, err
		}
		return &SkillPackageStream{FileName: p.Name + ".zip", Write: func(w io.Writer) error {
			_, err := w.Write(data)
			return err
		}}, nil
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	seen := map[string]bool{}
	var total int64
	for _, a := range decodePackageAttachments(v.Package) {
		if a.Path == "skill/ref.json" || a.Path == "skill/package.zip" {
			continue
		}
		if a.Path == "" || a.Path == "." || path.Clean(a.Path) != a.Path || path.IsAbs(a.Path) || a.Path == ".." || strings.HasPrefix(a.Path, "../") || strings.ContainsAny(a.Path, "\\\x00") || seen[a.Path] {
			return nil, ErrIntegrity
		}
		if len(seen) >= maxSkillZipEntries {
			return nil, ErrTooLarge
		}
		seen[a.Path] = true
		data, err := s.versionFileBytes(ctx, p, v, a.Path)
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		if total > s.maxArchiveBytes {
			return nil, ErrTooLarge
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: a.Path, Method: zip.Deflate, Modified: fixedZipModTime})
		if err != nil {
			return nil, err
		}
		if _, err = fw.Write(data); err != nil {
			return nil, err
		}
	}
	if len(seen) == 0 {
		return nil, ErrNotFound
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if int64(out.Len()) > s.maxArchiveBytes {
		return nil, ErrTooLarge
	}
	data := out.Bytes()
	return &SkillPackageStream{FileName: p.Name + ".zip", Write: func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	}}, nil
}
