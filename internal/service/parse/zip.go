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
	maxManifestFiles = 500              // cap on the number of paths recorded in Files
)

// ExtractResult holds the results of zip extraction.
type ExtractResult struct {
	SkillMDContent []byte
	TotalSize      int64
	// Files is the manifest of regular-file paths inside the package (dirs
	// excluded), in archive order. Used to show a bundled-file list in the UI.
	Files []string
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

	var totalSize uint64
	var skillMD *zip.File
	skillMDCount := 0
	capFiles := len(r.File)
	if capFiles > maxManifestFiles {
		capFiles = maxManifestFiles
	}
	files := make([]string, 0, capFiles)

	for _, f := range r.File {
		// Security: check for zip slip
		if errCode, errMsg := validateZipEntry(f); errCode != "" {
			return nil, errCode, errMsg
		}

		if f.FileInfo().IsDir() {
			continue
		}

		// Cap the recorded manifest so a package with pathologically many entries
		// can't bloat the stored row / detail responses (SKILL.md scan continues).
		if len(files) < maxManifestFiles {
			files = append(files, f.Name)
		}

		// UncompressedSize64 is an attacker-controlled uint64 from the archive
		// header. Accumulate in uint64 (an int64 cast could flip negative and
		// defeat the guard) and reject as soon as the declared total exceeds the
		// cap — a single over-large entry trips it before any wrap is possible.
		totalSize += f.UncompressedSize64
		if totalSize > uint64(maxExtractedSize) {
			return nil, "FILE_TOO_LARGE", fmt.Sprintf("extracted content exceeds %dMB limit", maxExtractedSize/(1024*1024))
		}

		// SKILL.md (case-insensitive) at root level OR one level deep. A package
		// with more than one such candidate is ambiguous: a "shallowest-wins" vs
		// "last-wins" split lets the archive author serve one file while a client
		// runs another, so it is rejected outright rather than resolved by a
		// heuristic (the count is checked after the loop).
		if isSkillMDCandidate(f.Name) {
			skillMDCount++
			skillMD = f
		}
	}

	if skillMDCount == 0 {
		return nil, "SKILL_MD_NOT_FOUND", "zip 包中未找到 SKILL.md 文件"
	}
	if skillMDCount > 1 {
		return nil, "MULTIPLE_SKILL_MD", "zip 包中包含多个 SKILL.md 文件，请只保留一个"
	}
	if skillMD.UncompressedSize64 > maxSkillMDSize {
		return nil, "SKILL_MD_TOO_LARGE", fmt.Sprintf("SKILL.md exceeds %dMB limit", maxSkillMDSize/(1024*1024))
	}
	skillMDContent, err := readZipFile(skillMD)
	if err != nil {
		return nil, "INVALID_ZIP", "cannot read SKILL.md: " + err.Error()
	}

	return &ExtractResult{
		SkillMDContent: skillMDContent,
		TotalSize:      int64(totalSize),
		Files:          files,
	}, "", ""
}

// isSkillMDCandidate reports whether name is a SKILL.md (case-insensitive) at
// the root or exactly one directory deep — the only two locations the catalog
// recognises. Backslashes are normalised to "/" first so a Windows-style path
// (which the Linux server's filepath would treat as a single segment) cannot
// smuggle a second SKILL.md past the multi-candidate guard.
func isSkillMDCandidate(name string) bool {
	name = strings.ReplaceAll(name, `\`, "/")
	if !strings.EqualFold(filepath.Base(name), "SKILL.md") {
		return false
	}
	dir := filepath.Dir(name)
	if dir == "." || dir == "" {
		return true
	}
	return !strings.Contains(dir, "/")
}

// validateZipEntry checks a zip entry for path traversal and symlinks.
func validateZipEntry(f *zip.File) (string, string) {
	// Reject absolute paths
	if filepath.IsAbs(f.Name) {
		return "ZIP_SLIP_DETECTED", "absolute path detected: " + f.Name
	}

	// Reject Windows-rooted names too. The server runs on Linux so
	// filepath.IsAbs misses `C:\...`, `\\server\share` and `\rooted`, but the
	// package is distributed for client-side install — a Windows client must
	// never receive a rooted entry.
	if isWindowsRooted(f.Name) {
		return "ZIP_SLIP_DETECTED", "rooted path detected: " + f.Name
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

// isWindowsRooted reports whether name is a Windows absolute/rooted path
// (drive-letter `X:\`/`X:/`, UNC `\\host\share`, or a leading `\`), which
// host-native filepath.IsAbs does not catch on a Linux server.
func isWindowsRooted(name string) bool {
	if strings.HasPrefix(name, `\`) {
		return true
	}
	if len(name) >= 2 && name[1] == ':' {
		c := name[0]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			return true
		}
	}
	return false
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
