package skill

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// makeZip creates an in-memory zip with the given entries.
func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatalf("makeZip: create %s: %v", name, err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatalf("makeZip: write %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("makeZip: close: %v", err)
	}
	return buf.Bytes()
}

func TestRewriteZipPackage_Normal(t *testing.T) {
	originalSkillMD := `---
name: old-name
description: old desc
version: 0.1.0
tags: [old-tag]
---
This is the body content.
`
	zipData := makeZip(t, map[string]string{
		"SKILL.md":     originalSkillMD,
		"README.md":    "# Hello",
		"src/main.lua": "print('hi')",
	})

	params := RewriteParams{
		Name:       "new-skill",
		Desc:       "A brand new description",
		Version:    "1.0.0",
		Tags:       []string{"tag1", "tag2"},
		ID:         "skill-uuid-123",
		ForkedFrom: "source-uuid-456",
	}

	result, err := RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), params)
	if err != nil {
		t.Fatalf("RewriteZipPackage failed: %v", err)
	}

	// Verify the result zip is valid and contains all entries
	reader, err := zip.NewReader(bytes.NewReader(result.ZipBytes), result.ZipSize)
	if err != nil {
		t.Fatalf("reading result zip: %v", err)
	}

	foundSkillMD := false
	foundReadme := false
	foundSrc := false
	for _, f := range reader.File {
		switch f.Name {
		case "SKILL.md":
			foundSkillMD = true
		case "README.md":
			foundReadme = true
			content, _ := readZipEntry(f)
			if string(content) != "# Hello" {
				t.Errorf("README.md content changed: %q", string(content))
			}
		case "src/main.lua":
			foundSrc = true
			content, _ := readZipEntry(f)
			if string(content) != "print('hi')" {
				t.Errorf("src/main.lua content changed: %q", string(content))
			}
		}
	}
	if !foundSkillMD {
		t.Error("SKILL.md missing from result zip")
	}
	if !foundReadme {
		t.Error("README.md missing from result zip")
	}
	if !foundSrc {
		t.Error("src/main.lua missing from result zip")
	}

	// Verify SKILL.md content
	md := string(result.SkillMD)
	if !strings.Contains(md, "name: new-skill") {
		t.Errorf("SKILL.md missing name: new-skill, got:\n%s", md)
	}
	if !strings.Contains(md, "description: A brand new description") {
		t.Errorf("SKILL.md missing description, got:\n%s", md)
	}
	if !strings.Contains(md, "version: 1.0.0") {
		t.Errorf("SKILL.md missing version, got:\n%s", md)
	}
	if !strings.Contains(md, "id: skill-uuid-123") {
		t.Errorf("SKILL.md missing id, got:\n%s", md)
	}
	if !strings.Contains(md, "forked_from: source-uuid-456") {
		t.Errorf("SKILL.md missing forked_from, got:\n%s", md)
	}
	if !strings.Contains(md, "tag1") || !strings.Contains(md, "tag2") {
		t.Errorf("SKILL.md missing tags, got:\n%s", md)
	}
	// Body should be preserved
	if !strings.Contains(md, "This is the body content.") {
		t.Errorf("SKILL.md body lost, got:\n%s", md)
	}

	// Verify ZipSize matches
	if result.ZipSize != int64(len(result.ZipBytes)) {
		t.Errorf("ZipSize = %d, want %d", result.ZipSize, len(result.ZipBytes))
	}

	// Verify SHA256
	h := sha256.Sum256(result.ZipBytes)
	wantSHA := hex.EncodeToString(h[:])
	if result.ZipSHA256 != wantSHA {
		t.Errorf("ZipSHA256 = %q, want %q", result.ZipSHA256, wantSHA)
	}
}

func TestRewriteZipPackage_NoSkillMD(t *testing.T) {
	zipData := makeZip(t, map[string]string{
		"README.md": "# Hello",
		"main.go":   "package main",
	})

	params := RewriteParams{
		Name:    "my-skill",
		Desc:    "desc",
		Version: "1.0.0",
	}

	_, err := RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), params)
	if err == nil {
		t.Fatal("expected error for zip without SKILL.md")
	}
	if !strings.Contains(err.Error(), "SKILL.md not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRewriteZipPackage_NonRootSkillMDNotMatched(t *testing.T) {
	// SKILL.md inside a subdirectory should NOT be matched
	zipData := makeZip(t, map[string]string{
		"subdir/SKILL.md": "---\nname: nested\n---\nbody",
		"README.md":       "# Root",
	})

	params := RewriteParams{
		Name:    "my-skill",
		Desc:    "desc",
		Version: "1.0.0",
	}

	_, err := RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), params)
	if err == nil {
		t.Fatal("expected error: non-root SKILL.md should not be matched")
	}
	if !strings.Contains(err.Error(), "SKILL.md not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRewriteZipPackage_VendorMetadataPreserved(t *testing.T) {
	originalSkillMD := `---
name: old
description: old desc
version: 0.1.0
metadata:
  openclaw:
    author: vendor-user
    license: MIT
  custom_field: hello
---
Body here.
`
	zipData := makeZip(t, map[string]string{
		"SKILL.md": originalSkillMD,
	})

	params := RewriteParams{
		Name:    "new-skill",
		Desc:    "new desc",
		Version: "2.0.0",
		Tags:    []string{"tag1"},
		ID:      "uuid-new",
		RawMetadata: map[string]interface{}{
			"name":        "old",
			"description": "old desc",
			"version":     "0.1.0",
			"metadata": map[string]interface{}{
				"openclaw": map[string]interface{}{
					"author":  "vendor-user",
					"license": "MIT",
				},
				"custom_field": "hello",
			},
		},
	}

	result, err := RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), params)
	if err != nil {
		t.Fatalf("RewriteZipPackage failed: %v", err)
	}

	md := string(result.SkillMD)

	// Business fields overwritten
	if !strings.Contains(md, "name: new-skill") {
		t.Errorf("name not overwritten, got:\n%s", md)
	}
	if !strings.Contains(md, "description: new desc") {
		t.Errorf("description not overwritten, got:\n%s", md)
	}
	if !strings.Contains(md, "version: 2.0.0") {
		t.Errorf("version not overwritten, got:\n%s", md)
	}
	if !strings.Contains(md, "id: uuid-new") {
		t.Errorf("id not set, got:\n%s", md)
	}

	// Vendor metadata preserved
	if !strings.Contains(md, "openclaw") {
		t.Errorf("vendor metadata.openclaw lost, got:\n%s", md)
	}
	if !strings.Contains(md, "vendor-user") {
		t.Errorf("vendor metadata.openclaw.author lost, got:\n%s", md)
	}
	if !strings.Contains(md, "MIT") {
		t.Errorf("vendor metadata.openclaw.license lost, got:\n%s", md)
	}
	if !strings.Contains(md, "custom_field") {
		t.Errorf("vendor metadata.custom_field lost, got:\n%s", md)
	}

	// Body preserved
	if !strings.Contains(md, "Body here.") {
		t.Errorf("body lost, got:\n%s", md)
	}
}

func TestRewriteZipPackage_BusinessFieldsOverwrite(t *testing.T) {
	originalSkillMD := `---
name: original-name
description: original-desc
version: 0.0.1
tags: [old-tag]
id: old-uuid
forked_from: old-fork
source_slug: should-be-removed
---
Keep this body.
`
	zipData := makeZip(t, map[string]string{
		"SKILL.md": originalSkillMD,
	})

	params := RewriteParams{
		Name:       "overwritten-name",
		Desc:       "overwritten-desc",
		Version:    "3.0.0",
		Tags:       []string{"new-tag1", "new-tag2"},
		ID:         "new-uuid",
		ForkedFrom: "new-fork-uuid",
		RawMetadata: map[string]interface{}{
			"name":        "original-name",
			"description": "original-desc",
			"version":     "0.0.1",
			"tags":        []string{"old-tag"},
			"id":          "old-uuid",
			"forked_from": "old-fork",
			"source_slug": "should-be-removed",
		},
	}

	result, err := RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), params)
	if err != nil {
		t.Fatalf("RewriteZipPackage failed: %v", err)
	}

	md := string(result.SkillMD)

	// New values present
	if !strings.Contains(md, "name: overwritten-name") {
		t.Errorf("name not overwritten, got:\n%s", md)
	}
	if !strings.Contains(md, "description: overwritten-desc") {
		t.Errorf("description not overwritten, got:\n%s", md)
	}
	if !strings.Contains(md, "version: 3.0.0") {
		t.Errorf("version not overwritten, got:\n%s", md)
	}
	if !strings.Contains(md, "id: new-uuid") {
		t.Errorf("id not overwritten, got:\n%s", md)
	}
	if !strings.Contains(md, "forked_from: new-fork-uuid") {
		t.Errorf("forked_from not overwritten, got:\n%s", md)
	}
	if !strings.Contains(md, "new-tag1") || !strings.Contains(md, "new-tag2") {
		t.Errorf("tags not overwritten, got:\n%s", md)
	}

	// Old values gone
	if strings.Contains(md, "original-name") {
		t.Errorf("old name still present, got:\n%s", md)
	}
	if strings.Contains(md, "original-desc") {
		t.Errorf("old description still present, got:\n%s", md)
	}
	if strings.Contains(md, "old-tag") {
		t.Errorf("old tags still present, got:\n%s", md)
	}
	if strings.Contains(md, "source_slug") {
		t.Errorf("source_slug should be removed, got:\n%s", md)
	}

	// Body preserved
	if !strings.Contains(md, "Keep this body.") {
		t.Errorf("body lost, got:\n%s", md)
	}
}

func TestRewriteZipPackage_CaseInsensitiveSkillMD(t *testing.T) {
	// Test that skill.md (lowercase) is also matched
	originalSkillMD := `---
name: old
description: desc
version: 1.0.0
---
body
`
	zipData := makeZip(t, map[string]string{
		"skill.md": originalSkillMD,
	})

	params := RewriteParams{
		Name:    "new-name",
		Desc:    "new desc",
		Version: "2.0.0",
	}

	result, err := RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), params)
	if err != nil {
		t.Fatalf("RewriteZipPackage failed: %v", err)
	}

	md := string(result.SkillMD)
	if !strings.Contains(md, "name: new-name") {
		t.Errorf("expected rewritten name, got:\n%s", md)
	}
}

func TestRewriteZipPackage_EmptyTags(t *testing.T) {
	originalSkillMD := `---
name: old
description: desc
version: 1.0.0
tags: [keep-this]
---
body
`
	zipData := makeZip(t, map[string]string{
		"SKILL.md": originalSkillMD,
	})

	// Empty Tags should remove tags from output
	params := RewriteParams{
		Name:    "new",
		Desc:    "desc",
		Version: "1.0.0",
		Tags:    nil,
	}

	result, err := RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), params)
	if err != nil {
		t.Fatalf("RewriteZipPackage failed: %v", err)
	}

	md := string(result.SkillMD)
	if strings.Contains(md, "tags:") {
		t.Errorf("tags should be absent when empty, got:\n%s", md)
	}
	if strings.Contains(md, "keep-this") {
		t.Errorf("old tag should be removed, got:\n%s", md)
	}
}

func TestRewriteZipPackage_NoForkedFrom(t *testing.T) {
	originalSkillMD := `---
name: old
description: desc
version: 1.0.0
forked_from: old-source
---
body
`
	zipData := makeZip(t, map[string]string{
		"SKILL.md": originalSkillMD,
	})

	// Empty ForkedFrom should remove it
	params := RewriteParams{
		Name:       "new",
		Desc:       "desc",
		Version:    "1.0.0",
		ForkedFrom: "",
		RawMetadata: map[string]interface{}{
			"forked_from": "old-source",
		},
	}

	result, err := RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), params)
	if err != nil {
		t.Fatalf("RewriteZipPackage failed: %v", err)
	}

	md := string(result.SkillMD)
	if strings.Contains(md, "forked_from") {
		t.Errorf("forked_from should be absent, got:\n%s", md)
	}
}

func TestRewriteZipPackage_PreservesFileHeaders(t *testing.T) {
	// Create a zip with specific compression method
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	// Add SKILL.md
	header := &zip.FileHeader{
		Name:   "SKILL.md",
		Method: zip.Deflate,
	}
	fw, err := w.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte("---\nname: old\ndescription: d\nversion: 1.0.0\n---\nbody"))

	// Add another file with Store method
	header2 := &zip.FileHeader{
		Name:   "data.bin",
		Method: zip.Store,
	}
	fw2, err := w.CreateHeader(header2)
	if err != nil {
		t.Fatal(err)
	}
	fw2.Write([]byte("binary data"))

	w.Close()

	zipData := buf.Bytes()
	params := RewriteParams{
		Name:    "new",
		Desc:    "desc",
		Version: "1.0.0",
	}

	result, err := RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), params)
	if err != nil {
		t.Fatalf("RewriteZipPackage failed: %v", err)
	}

	// Verify the result zip preserves the data.bin entry
	reader, err := zip.NewReader(bytes.NewReader(result.ZipBytes), result.ZipSize)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range reader.File {
		if f.Name == "data.bin" {
			content, _ := readZipEntry(f)
			if string(content) != "binary data" {
				t.Errorf("data.bin content changed: %q", string(content))
			}
			return
		}
	}
	t.Error("data.bin not found in result zip")
}

func TestIsRootSkillMD(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"SKILL.md", true},
		{"skill.md", true},
		{"Skill.md", true},
		{"SKILL.MD", true},
		{"subdir/SKILL.md", false},
		{"a/b/skill.md", false},
		{"README.md", false},
		{"skill.txt", false},
		{"SKILL.md.bak", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isRootSkillMD(tt.path); got != tt.want {
				t.Errorf("isRootSkillMD(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestSplitFrontmatterAndBody(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantFM   string
		wantBody string
	}{
		{
			name:     "normal frontmatter",
			input:    "---\nname: test\n---\nbody content",
			wantFM:   "name: test",
			wantBody: "body content",
		},
		{
			name:     "no frontmatter",
			input:    "just body content",
			wantFM:   "",
			wantBody: "just body content",
		},
		{
			name:     "empty content",
			input:    "",
			wantFM:   "",
			wantBody: "",
		},
		{
			name:     "only delimiters",
			input:    "---\n---\n",
			wantFM:   "",
			wantBody: "",
		},
		{
			name:     "unclosed frontmatter",
			input:    "---\nname: test\nbody here",
			wantFM:   "",
			wantBody: "---\nname: test\nbody here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body := splitFrontmatterAndBody([]byte(tt.input))
			if fm != tt.wantFM {
				t.Errorf("frontmatter = %q, want %q", fm, tt.wantFM)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestRewriteZipPackage_VendorOpenclawFieldsIntact(t *testing.T) {
	// Specifically test that metadata.openclaw.* fields survive rewrite
	originalSkillMD := `---
name: my-tool
description: A tool
version: 1.0.0
metadata:
  openclaw:
    runtime: node
    min_version: "18.0"
    capabilities:
      - web
      - api
  octo:
    internal_id: xxx
---
Tool body content.
`
	zipData := makeZip(t, map[string]string{
		"SKILL.md": originalSkillMD,
	})

	params := RewriteParams{
		Name:    "renamed-tool",
		Desc:    "A renamed tool",
		Version: "2.0.0",
		Tags:    []string{"tool"},
		ID:      "tool-uuid",
		RawMetadata: map[string]interface{}{
			"name":        "my-tool",
			"description": "A tool",
			"version":     "1.0.0",
			"metadata": map[string]interface{}{
				"openclaw": map[string]interface{}{
					"runtime":     "node",
					"min_version": "18.0",
					"capabilities": []interface{}{
						"web",
						"api",
					},
				},
				"octo": map[string]interface{}{
					"internal_id": "xxx",
				},
			},
		},
	}

	result, err := RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), params)
	if err != nil {
		t.Fatalf("RewriteZipPackage failed: %v", err)
	}

	md := string(result.SkillMD)

	// Business fields rewritten
	if !strings.Contains(md, "name: renamed-tool") {
		t.Errorf("name not rewritten, got:\n%s", md)
	}

	// Vendor fields preserved — openclaw
	if !strings.Contains(md, "openclaw") {
		t.Errorf("metadata.openclaw lost, got:\n%s", md)
	}
	if !strings.Contains(md, "runtime") {
		t.Errorf("metadata.openclaw.runtime lost, got:\n%s", md)
	}
	if !strings.Contains(md, "node") {
		t.Errorf("metadata.openclaw.runtime value lost, got:\n%s", md)
	}
	if !strings.Contains(md, "min_version") {
		t.Errorf("metadata.openclaw.min_version lost, got:\n%s", md)
	}
	if !strings.Contains(md, "capabilities") {
		t.Errorf("metadata.openclaw.capabilities lost, got:\n%s", md)
	}

	// Vendor fields preserved — octo (even though spec says not to write it,
	// the RawMetadata key "metadata" is a single vendor map — we preserve whatever is there)
	if !strings.Contains(md, "octo") {
		t.Errorf("metadata.octo lost, got:\n%s", md)
	}

	// Body preserved
	if !strings.Contains(md, "Tool body content.") {
		t.Errorf("body lost, got:\n%s", md)
	}
}

func TestRewriteZipPackage_NoRawMetadata(t *testing.T) {
	originalSkillMD := `---
name: old
description: old desc
version: 0.1.0
---
body
`
	zipData := makeZip(t, map[string]string{
		"SKILL.md": originalSkillMD,
	})

	params := RewriteParams{
		Name:        "new-skill",
		Desc:        "new desc",
		Version:     "1.0.0",
		RawMetadata: nil, // no raw metadata provided
	}

	result, err := RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), params)
	if err != nil {
		t.Fatalf("RewriteZipPackage failed: %v", err)
	}

	md := string(result.SkillMD)
	if !strings.Contains(md, "name: new-skill") {
		t.Errorf("name not set, got:\n%s", md)
	}
	if !strings.Contains(md, "body") {
		t.Errorf("body lost, got:\n%s", md)
	}
}
