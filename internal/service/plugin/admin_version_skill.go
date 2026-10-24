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

// Historical helpers always resolve the requested resource/version pair first.
// They never fall back to the mutable package when historical bytes are absent.
func (s *Service) adminVersionSkill(ctx context.Context, caller Caller, pluginID, versionID string) (*model.Plugin, *model.PluginVersion, error) {
	v, err := s.ManagedVersion(ctx, caller, pluginID, versionID, true)
	if err != nil {
		return nil, nil, err
	}
	if v.PluginType != model.PluginTypeSkill {
		return nil, nil, ErrInvalidRequest
	}
	p, _, err := s.repo.GetWithRelations(ctx, adminScope(caller), v.PluginID)
	if err != nil {
		return nil, nil, mapStoreError(err)
	}
	return p, v, nil
}

func (s *Service) AdminVersionSkillMarkdown(ctx context.Context, caller Caller, pluginID, versionID string) (string, error) {
	p, v, err := s.adminVersionSkill(ctx, caller, pluginID, versionID)
	if err != nil {
		return "", err
	}
	data, err := s.versionFileBytes(ctx, p, v, "SKILL.md")
	return string(data), err
}

func (s *Service) AdminOpenVersionSkillPackage(ctx context.Context, caller Caller, pluginID, versionID string) (*SkillPackageStream, error) {
	p, v, err := s.adminVersionSkill(ctx, caller, pluginID, versionID)
	if err != nil {
		return nil, err
	}
	// A legacy stored ZIP is verified against the historical descriptor before
	// returning any bytes. Otherwise reconstruct from individually verified files.
	if _, _, _, ok, e := legacyVersionPointer(v, "skill/package.zip"); e != nil {
		return nil, e
	} else if ok {
		data, e := s.versionFileBytes(ctx, p, v, "skill/package.zip")
		if e != nil {
			return nil, e
		}
		return &SkillPackageStream{FileName: p.Name + ".zip", Write: func(w io.Writer) error { _, e := w.Write(data); return e }}, nil
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
		data, e := s.versionFileBytes(ctx, p, v, a.Path)
		if e != nil {
			return nil, e
		}
		total += int64(len(data))
		if total > s.maxArchiveBytes {
			return nil, ErrTooLarge
		}
		fw, e := zw.CreateHeader(&zip.FileHeader{Name: a.Path, Method: zip.Deflate, Modified: fixedZipModTime})
		if e != nil {
			return nil, e
		}
		if _, e = fw.Write(data); e != nil {
			return nil, e
		}
	}
	if len(seen) == 0 {
		return nil, ErrNotFound
	}
	if err = zw.Close(); err != nil {
		return nil, err
	}
	if int64(out.Len()) > s.maxArchiveBytes {
		return nil, ErrTooLarge
	}
	data := out.Bytes()
	return &SkillPackageStream{FileName: p.Name + ".zip", Write: func(w io.Writer) error { _, e := w.Write(data); return e }}, nil
}
