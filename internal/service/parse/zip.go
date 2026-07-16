package parse

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxExtractedSize = 50 * 1024 * 1024 // 50MB total extracted size limit
	maxSkillMDSize   = 1 * 1024 * 1024  // 1MB SKILL.md size limit
)

// ExtractResult holds the results of zip extraction.
type ExtractResult struct {
	SkillMDContent []byte
	TotalSize      int64
}

// ExtractZip safely extracts a zip file and returns the SKILL.md content.
// It enforces: no zip slip, no symlinks, size limits.
func ExtractZip(zipPath string, maxZipSize int64) (*ExtractResult, string, string) {
	info, err := os.Stat(zipPath)
	if err != nil {
		return nil, "INVALID_ZIP", "cannot stat zip file"
	}
	if info.Size() > maxZipSize {
		return nil, "FILE_TOO_LARGE", fmt.Sprintf("zip file exceeds %dMB limit", maxZipSize/(1024*1024))
	}

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, "INVALID_ZIP", "cannot open zip file: " + err.Error()
	}
	defer r.Close()

	var totalSize int64
	var skillMDContent []byte
	var skillMDFound bool

	for _, f := range r.File {
		// Security: check for zip slip
		if errCode, errMsg := validateZipEntry(f); errCode != "" {
			return nil, errCode, errMsg
		}

		if f.FileInfo().IsDir() {
			continue
		}

		totalSize += int64(f.UncompressedSize64)
		if totalSize > maxExtractedSize {
			return nil, "FILE_TOO_LARGE", fmt.Sprintf("extracted content exceeds %dMB limit", maxExtractedSize/(1024*1024))
		}

		// Look for SKILL.md (case-insensitive) at root level OR one level deep.
		// Supports both:
		//   SKILL.md          (root level)
		//   some-dir/SKILL.md (single top-level subdirectory)
		name := filepath.Base(f.Name)
		dir := filepath.Dir(f.Name)
		isRoot := dir == "." || dir == ""
		isOneLevel := !isRoot && !strings.Contains(dir, "/")
		if strings.EqualFold(name, "SKILL.md") && (isRoot || isOneLevel) {
			if f.UncompressedSize64 > maxSkillMDSize {
				return nil, "SKILL_MD_TOO_LARGE", fmt.Sprintf("SKILL.md exceeds %dMB limit", maxSkillMDSize/(1024*1024))
			}
			content, err := readZipFile(f)
			if err != nil {
				return nil, "INVALID_ZIP", "cannot read SKILL.md: " + err.Error()
			}
			skillMDContent = content
			skillMDFound = true
		}
	}

	if !skillMDFound {
		return nil, "SKILL_MD_NOT_FOUND", "zip 包中未找到 SKILL.md 文件"
	}

	return &ExtractResult{
		SkillMDContent: skillMDContent,
		TotalSize:      totalSize,
	}, "", ""
}

// validateZipEntry checks a zip entry for path traversal and symlinks.
func validateZipEntry(f *zip.File) (string, string) {
	// Reject absolute paths
	if filepath.IsAbs(f.Name) {
		return "ZIP_SLIP_DETECTED", "absolute path detected: " + f.Name
	}

	// Reject paths with ..
	cleaned := filepath.Clean(f.Name)
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, string(filepath.Separator)+"..") {
		return "ZIP_SLIP_DETECTED", "path traversal detected: " + f.Name
	}

	// On Unix, also check for .. in raw name
	if strings.Contains(f.Name, "../") || strings.Contains(f.Name, "..\\") {
		return "ZIP_SLIP_DETECTED", "path traversal detected: " + f.Name
	}

	// Reject symlinks
	if f.FileInfo().Mode()&os.ModeSymlink != 0 {
		return "ZIP_SLIP_DETECTED", "symlink detected: " + f.Name
	}

	return "", ""
}

// readZipFile reads the contents of a single zip entry.
func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	// Read with limit to prevent decompression bombs
	limited := io.LimitReader(rc, maxSkillMDSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxSkillMDSize {
		return nil, fmt.Errorf("file too large")
	}
	return data, nil
}
