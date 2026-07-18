package skill

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// RewriteParams holds the parameters for rewriting a zip's SKILL.md frontmatter.
type RewriteParams struct {
	Name       string
	Desc       string
	Version    string
	Tags       []string
	ID         string // skill UUID to inject
	ForkedFrom string // source skill UUID
	// RawMetadata is the original YAML map from the uploaded SKILL.md.
	// User vendor fields (anything not in the known set) are preserved.
	RawMetadata map[string]interface{}
}

// RewriteResult holds the output of RewriteZipPackage.
type RewriteResult struct {
	ZipBytes  []byte
	SkillMD   []byte
	ZipSize   int64
	ZipSHA256 string
}

// knownBusinessFields are fields that RewriteZipPackage overwrites with the
// values from RewriteParams. They are never inherited from RawMetadata.
var knownBusinessFields = map[string]bool{
	"name":        true,
	"description": true,
	"version":     true,
	"tags":        true,
	"id":          true,
	"forked_from": true,
	"source_slug": true,
	"space_id":    true,
}

// forbiddenMetadataVendors are metadata sub-keys that must NOT appear in the
// rewritten frontmatter. Only user vendor fields (e.g. "openclaw") are kept.
var forbiddenMetadataVendors = map[string]bool{
	"octo": true,
}

// RewriteZipPackage rewrites the SKILL.md frontmatter inside a zip archive.
// Non-SKILL.md entries are copied verbatim (preserving headers).
// Returns the new zip bytes, extracted SKILL.md content, size, and SHA256.
func RewriteZipPackage(zipData io.ReaderAt, zipSize int64, p RewriteParams) (*RewriteResult, error) {
	reader, err := zip.NewReader(zipData, zipSize)
	if err != nil {
		return nil, fmt.Errorf("rewrite: open zip: %w", err)
	}

	var skillMDEntry *zip.File
	for _, f := range reader.File {
		if isRootSkillMD(f.Name) {
			skillMDEntry = f
			break
		}
	}
	if skillMDEntry == nil {
		return nil, fmt.Errorf("rewrite: SKILL.md not found in zip root")
	}

	// Read the original SKILL.md
	origMD, err := readZipEntry(skillMDEntry)
	if err != nil {
		return nil, fmt.Errorf("rewrite: read SKILL.md: %w", err)
	}

	// Build the new SKILL.md with rewritten frontmatter
	newMD := buildRewrittenSkillMD(origMD, p)

	// Write the new zip
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)

	for _, f := range reader.File {
		if isRootSkillMD(f.Name) {
			// Write the rewritten SKILL.md
			header := f.FileHeader
			header.UncompressedSize64 = uint64(len(newMD))
			w, err := writer.CreateHeader(&header)
			if err != nil {
				return nil, fmt.Errorf("rewrite: create SKILL.md header: %w", err)
			}
			if _, err := w.Write(newMD); err != nil {
				return nil, fmt.Errorf("rewrite: write SKILL.md: %w", err)
			}
			continue
		}

		// Copy other entries verbatim
		if err := copyZipEntry(writer, f); err != nil {
			return nil, fmt.Errorf("rewrite: copy entry %s: %w", f.Name, err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("rewrite: close zip: %w", err)
	}

	zipBytes := buf.Bytes()
	h := sha256.Sum256(zipBytes)

	return &RewriteResult{
		ZipBytes:  zipBytes,
		SkillMD:   newMD,
		ZipSize:   int64(len(zipBytes)),
		ZipSHA256: hex.EncodeToString(h[:]),
	}, nil
}

// isRootSkillMD checks if a zip entry path is SKILL.md at the root level.
// Only matches entries whose base name is "skill.md" (case-insensitive) and
// that reside at the zip root (path.Dir == "." or "").
func isRootSkillMD(name string) bool {
	dir := path.Dir(name)
	if dir != "." && dir != "" {
		return false
	}
	return strings.EqualFold(path.Base(name), "skill.md")
}

// buildRewrittenSkillMD constructs the new SKILL.md content with updated frontmatter.
func buildRewrittenSkillMD(original []byte, p RewriteParams) []byte {
	_, body := splitFrontmatterAndBody(original)

	// Build the new frontmatter map, preserving user vendor fields
	fm := make(map[string]interface{})

	// Start with raw metadata (user vendor fields) if available
	if p.RawMetadata != nil {
		for k, v := range p.RawMetadata {
			// Only copy vendor fields; skip business fields that we overwrite below
			if knownBusinessFields[k] {
				continue
			}
			// For the "metadata" key, filter out forbidden vendor sub-keys (e.g. "octo")
			if k == "metadata" {
				filtered := filterMetadataVendors(v)
				if filtered != nil {
					fm[k] = filtered
				}
			} else {
				fm[k] = v
			}
		}
	}

	// Overwrite business fields
	fm["name"] = p.Name
	fm["description"] = p.Desc
	fm["version"] = p.Version
	if len(p.Tags) > 0 {
		fm["tags"] = p.Tags
	} else {
		delete(fm, "tags")
	}
	if p.ID != "" {
		fm["id"] = p.ID
	}
	if p.ForkedFrom != "" {
		fm["forked_from"] = p.ForkedFrom
	} else {
		delete(fm, "forked_from")
	}

	// Remove fields we explicitly don't want in the frontmatter
	delete(fm, "source_slug")
	delete(fm, "space_id")

	yamlBytes, err := yaml.Marshal(fm)
	if err != nil {
		// Fallback: just write the known fields
		yamlBytes = buildMinimalYAML(p)
	}

	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")
	if body != "" {
		buf.WriteString(body)
	}

	return buf.Bytes()
}

// filterMetadataVendors takes the raw "metadata" value (expected to be a map)
// and returns a new map with forbidden vendor sub-keys removed. Returns nil if
// the result is empty or the input is not a map.
func filterMetadataVendors(v interface{}) interface{} {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	filtered := make(map[string]interface{}, len(m))
	for k, val := range m {
		if forbiddenMetadataVendors[k] {
			continue
		}
		filtered[k] = val
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func buildMinimalYAML(p RewriteParams) []byte {
	fm := map[string]interface{}{
		"name":        p.Name,
		"description": p.Desc,
		"version":     p.Version,
	}
	if len(p.Tags) > 0 {
		fm["tags"] = p.Tags
	}
	if p.ID != "" {
		fm["id"] = p.ID
	}
	if p.ForkedFrom != "" {
		fm["forked_from"] = p.ForkedFrom
	}
	b, _ := yaml.Marshal(fm)
	return b
}

// splitFrontmatterAndBody splits content into frontmatter YAML and body.
func splitFrontmatterAndBody(content []byte) (string, string) {
	text := string(content)
	lines := strings.Split(text, "\n")

	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", text
	}

	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			fmContent := strings.Join(lines[1:i], "\n")
			body := strings.Join(lines[i+1:], "\n")
			return fmContent, body
		}
	}

	// No closing delimiter
	return "", text
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func copyZipEntry(w *zip.Writer, f *zip.File) error {
	header := f.FileHeader
	writer, err := w.CreateHeader(&header)
	if err != nil {
		return err
	}

	if f.FileInfo().IsDir() {
		return nil
	}

	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	_, err = io.Copy(writer, rc)
	return err
}
