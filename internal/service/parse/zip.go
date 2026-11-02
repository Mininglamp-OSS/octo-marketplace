package parse

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
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

// SkillEntry carries a regular-file entry's original bytes and text encoding.
type SkillEntry struct {
	Path   string
	Bytes  []byte
	IsText bool
}

// ExtractSkillTree reads every regular-file entry (SKILL.md included) from a zip
// provided as an in-memory reader, applying the same zip-slip/symlink/bomb guards
// and total-size cap as ExtractZip, and enforcing the single-SKILL.md rule. Each
// entry is read whole (up to maxFileSize) so binary files survive with their
// bytes; exceeding maxFileSize or maxFiles is a hard error rather than a silent
// skip, so no file vanishes from the reconstructed package. Directories are
// excluded. Entry paths are returned verbatim (archive order); the caller is
// responsible for rooting them at the SKILL.md directory.
func ExtractSkillTree(ra io.ReaderAt, size, maxZipSize, maxFileSize int64, maxFiles int) ([]SkillEntry, string, string) {
	if size > maxZipSize {
		return nil, "FILE_TOO_LARGE", fmt.Sprintf("zip file exceeds %dMB limit", maxZipSize/(1024*1024))
	}
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, "INVALID_ZIP", "cannot open zip file: " + err.Error()
	}
	if maxFiles <= 0 {
		maxFiles = maxManifestFiles
	}

	var totalSize uint64
	var actualSize int64
	skillMDCount := 0
	// Bound the preallocation by maxFiles, not the (attacker-controlled) central
	// directory entry count, so a zip declaring millions of entries cannot force a
	// huge up-front slice allocation before the per-entry maxFiles cap fires below.
	capHint := len(zr.File)
	if capHint > maxFiles {
		capHint = maxFiles
	}
	entries := make([]SkillEntry, 0, capHint)
	for _, f := range zr.File {
		if errCode, errMsg := validateZipEntry(f); errCode != "" {
			return nil, errCode, errMsg
		}
		if f.FileInfo().IsDir() {
			continue
		}
		totalSize += f.UncompressedSize64
		if totalSize > uint64(maxExtractedSize) {
			return nil, "FILE_TOO_LARGE", fmt.Sprintf("extracted content exceeds %dMB limit", maxExtractedSize/(1024*1024))
		}
		if isSkillMDCandidate(f.Name) {
			skillMDCount++
		}
		if len(entries) >= maxFiles {
			return nil, "TOO_MANY_FILES", fmt.Sprintf("package exceeds %d files", maxFiles)
		}
		if int64(f.UncompressedSize64) > maxFileSize {
			return nil, "FILE_TOO_LARGE", fmt.Sprintf("entry %s exceeds %dMB limit", f.Name, maxFileSize/(1024*1024))
		}
		data, err := readZipFileLimited(f, maxFileSize)
		if err != nil {
			return nil, "INVALID_ZIP", "cannot read entry " + f.Name + ": " + err.Error()
		}
		// Bound the ACTUAL decompressed bytes held in memory, not just the
		// archive's declared sizes: a bomb can declare tiny per-entry sizes (so
		// the declared total passes above) yet decompress to maxFileSize each,
		// which across maxFiles entries would otherwise pin tens of GB.
		actualSize += int64(len(data))
		if actualSize > maxExtractedSize {
			return nil, "FILE_TOO_LARGE", fmt.Sprintf("extracted content exceeds %dMB limit", maxExtractedSize/(1024*1024))
		}
		entries = append(entries, SkillEntry{Path: f.Name, Bytes: data, IsText: utf8.Valid(data)})
	}

	if skillMDCount == 0 {
		return nil, "SKILL_MD_NOT_FOUND", "zip 包中未找到 SKILL.md 文件"
	}
	if skillMDCount > 1 {
		return nil, "MULTIPLE_SKILL_MD", "zip 包中包含多个 SKILL.md 文件，请只保留一个"
	}
	return entries, "", ""
}

// ExtractArchive reads every regular-file entry of an in-memory zip into a
// path->bytes map, applying the zip-slip/symlink guards and independent archive
// transport, extracted-total, and per-entry byte limits. maxFiles==0 retains
// the historical default cap; maxFiles<0 is unlimited. Exceeding any configured
// limit is a hard error so no entry is silently dropped. Directories are
// excluded; entry paths are returned verbatim. A duplicate path is a hard error.
func ExtractArchive(ra io.ReaderAt, size, maxArchiveBytes, maxTotal, maxFileSize int64, maxFiles int) (map[string][]byte, string, string) {
	if size > maxArchiveBytes {
		return nil, "FILE_TOO_LARGE", fmt.Sprintf("archive exceeds %dMB transport limit", maxArchiveBytes/(1024*1024))
	}
	if maxTotal < 0 || maxFileSize < 0 {
		return nil, "FILE_TOO_LARGE", "invalid archive content limit"
	}
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, "INVALID_ZIP", "cannot open zip file: " + err.Error()
	}
	if maxFiles == 0 {
		maxFiles = maxManifestFiles
	}
	out := make(map[string][]byte)
	var actualSize int64
	for _, f := range zr.File {
		if errCode, errMsg := validateZipEntry(f); errCode != "" {
			return nil, errCode, errMsg
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if maxFiles > 0 && len(out) >= maxFiles {
			return nil, "TOO_MANY_FILES", fmt.Sprintf("archive exceeds %d files", maxFiles)
		}
		if f.UncompressedSize64 > uint64(maxFileSize) {
			return nil, "FILE_TOO_LARGE", fmt.Sprintf("entry %s exceeds %dMB limit", f.Name, maxFileSize/(1024*1024))
		}
		if _, dup := out[f.Name]; dup {
			return nil, "DUPLICATE_ENTRY", "duplicate archive entry: " + f.Name
		}
		if f.UncompressedSize64 > uint64(maxTotal-actualSize) {
			return nil, "FILE_TOO_LARGE", fmt.Sprintf("extracted content exceeds %dMB limit", maxTotal/(1024*1024))
		}
		data, err := readZipFileLimited(f, maxFileSize)
		if err != nil {
			return nil, "INVALID_ZIP", "cannot read entry " + f.Name + ": " + err.Error()
		}
		if int64(len(data)) > maxTotal-actualSize {
			return nil, "FILE_TOO_LARGE", fmt.Sprintf("extracted content exceeds %dMB limit", maxTotal/(1024*1024))
		}
		actualSize += int64(len(data))
		out[f.Name] = data
	}
	return out, "", ""
}

// IsSkillMDCandidate reports whether an entry path is the package's SKILL.md
// (root or exactly one directory deep). Exported for callers that reconstruct
// the attachment tree and must root it at the SKILL.md directory.
func IsSkillMDCandidate(name string) bool { return isSkillMDCandidate(name) }

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
	return readZipFileLimited(f, maxSkillMDSize)
}

// readZipFileLimited reads one zip entry, capped at max bytes (rejecting a
// decompression bomb that lies about its size).
func readZipFileLimited(f *zip.File, max int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	limited := io.LimitReader(rc, max+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("file too large")
	}
	return data, nil
}
