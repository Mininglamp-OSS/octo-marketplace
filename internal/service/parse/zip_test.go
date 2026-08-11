package parse

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func createTestZip(t *testing.T, files map[string][]byte) string {
	t.Helper()
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return zipPath
}

func TestExtractZip_ValidSkillMD(t *testing.T) {
	content := []byte("# My Skill\n\nA test skill.")
	zipPath := createTestZip(t, map[string][]byte{
		"SKILL.md":  content,
		"other.txt": []byte("hello"),
	})

	result, errCode, errMsg := ExtractZip(zipPath, 20*1024*1024)
	if errCode != "" {
		t.Fatalf("unexpected error: %s: %s", errCode, errMsg)
	}
	if string(result.SkillMDContent) != string(content) {
		t.Errorf("SkillMDContent = %q, want %q", result.SkillMDContent, content)
	}
}

func TestExtractZip_CaseInsensitiveSkillMD(t *testing.T) {
	content := []byte("# Skill\n\nDesc.")
	zipPath := createTestZip(t, map[string][]byte{
		"skill.md": content,
	})

	result, errCode, _ := ExtractZip(zipPath, 20*1024*1024)
	if errCode != "" {
		t.Fatalf("unexpected error: %s", errCode)
	}
	if string(result.SkillMDContent) != string(content) {
		t.Error("did not find case-insensitive SKILL.md")
	}
}

func TestExtractZip_MultipleSkillMDRejected(t *testing.T) {
	// Root + one-level SKILL.md is ambiguous — the archive author could serve
	// one file while the client runs another — so extraction must reject it
	// rather than silently pick one.
	zipPath := createTestZip(t, map[string][]byte{
		"SKILL.md":     []byte("# root"),
		"sub/SKILL.md": []byte("# nested"),
	})

	_, errCode, _ := ExtractZip(zipPath, 20*1024*1024)
	if errCode != "MULTIPLE_SKILL_MD" {
		t.Fatalf("errCode = %q, want MULTIPLE_SKILL_MD", errCode)
	}
}

func TestExtractZip_BackslashSkillMDCounted(t *testing.T) {
	// A Windows-style backslash path must not smuggle a second SKILL.md past the
	// multi-candidate guard (filepath treats "\\" as one segment on Linux).
	zipPath := createTestZip(t, map[string][]byte{
		"SKILL.md":      []byte("# root"),
		"sub\\SKILL.md": []byte("# nested"),
	})

	_, errCode, _ := ExtractZip(zipPath, 20*1024*1024)
	if errCode != "MULTIPLE_SKILL_MD" {
		t.Fatalf("errCode = %q, want MULTIPLE_SKILL_MD", errCode)
	}
}

func TestExtractZip_MissingSkillMD(t *testing.T) {
	zipPath := createTestZip(t, map[string][]byte{
		"README.md": []byte("hello"),
	})

	_, errCode, _ := ExtractZip(zipPath, 20*1024*1024)
	if errCode != "SKILL_MD_NOT_FOUND" {
		t.Errorf("errCode = %q, want SKILL_MD_NOT_FOUND", errCode)
	}
}

func TestExtractZip_ZipSlipDotDot(t *testing.T) {
	// Create a zip with a path-traversal entry
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "evil.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	fw, _ := w.Create("../../../etc/passwd")
	_, _ = fw.Write([]byte("evil"))
	fw2, _ := w.Create("SKILL.md")
	_, _ = fw2.Write([]byte("# Skill"))
	_ = w.Close()

	_, errCode, _ := ExtractZip(zipPath, 20*1024*1024)
	if errCode != "ZIP_SLIP_DETECTED" {
		t.Errorf("errCode = %q, want ZIP_SLIP_DETECTED", errCode)
	}
}

func TestExtractZip_InvalidZipFile(t *testing.T) {
	tmpDir := t.TempDir()
	badPath := filepath.Join(tmpDir, "bad.zip")
	_ = os.WriteFile(badPath, []byte("not a zip"), 0o644)

	_, errCode, _ := ExtractZip(badPath, 20*1024*1024)
	if errCode != "INVALID_ZIP" {
		t.Errorf("errCode = %q, want INVALID_ZIP", errCode)
	}
}

func TestExtractZip_SkillMDInSubdir(t *testing.T) {
	// SKILL.md one level deep is now supported
	zipPath := createTestZip(t, map[string][]byte{
		"subdir/SKILL.md": []byte("---\nname: test-skill\ndescription: A test\n---\n# Skill"),
	})

	result, errCode, _ := ExtractZip(zipPath, 20*1024*1024)
	if errCode != "" {
		t.Errorf("errCode = %q, want empty (one-level subdir allowed)", errCode)
	}
	if result == nil || len(result.SkillMDContent) == 0 {
		t.Errorf("expected SKILL.md content to be extracted")
	}
}

func TestExtractSkillFiles_TextOnlyExcludingSkillMD(t *testing.T) {
	zipPath := createTestZip(t, map[string][]byte{
		"SKILL.md":     []byte("# Skill"),                          // excluded (reserved)
		"reference.md": []byte("# Reference\nnotes"),               // text → kept
		"scripts/a.sh": []byte("echo hi\n"),                        // text → kept
		"logo.png":     {0x89, 0x50, 0x4e, 0x47, 0x00, 0xff, 0xfe}, // binary → skipped
	})

	files, skipped, errCode, errMsg := ExtractSkillFiles(zipPath, 20*1024*1024, 0)
	if errCode != "" {
		t.Fatalf("unexpected error: %s: %s", errCode, errMsg)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = f.Content
	}
	if _, ok := got["SKILL.md"]; ok {
		t.Errorf("SKILL.md should be excluded, got %v", got)
	}
	if got["reference.md"] != "# Reference\nnotes" || got["scripts/a.sh"] != "echo hi\n" {
		t.Errorf("text files not extracted correctly: %#v", got)
	}
	if len(files) != 2 {
		t.Errorf("kept %d files, want 2: %#v", len(files), got)
	}
	found := false
	for _, s := range skipped {
		if s == "logo.png" {
			found = true
		}
	}
	if !found {
		t.Errorf("logo.png should be reported skipped, skipped=%v", skipped)
	}
}

func TestExtractSkillFiles_RespectsMaxFiles(t *testing.T) {
	zipPath := createTestZip(t, map[string][]byte{
		"SKILL.md": []byte("# S"),
		"a.txt":    []byte("a"),
		"b.txt":    []byte("b"),
		"c.txt":    []byte("c"),
	})
	files, skipped, errCode, _ := ExtractSkillFiles(zipPath, 20*1024*1024, 2)
	if errCode != "" {
		t.Fatalf("errCode=%s", errCode)
	}
	if len(files) != 2 {
		t.Fatalf("kept %d, want 2 (maxFiles)", len(files))
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped %d, want 1 over the cap", len(skipped))
	}
}
