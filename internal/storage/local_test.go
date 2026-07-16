package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalStorage_WriteAndGetObject(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLocal(tmpDir, "http://localhost:8092")

	key := "skills/abc/test.zip"
	content := "hello world"

	if err := ls.WriteObject(key, strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}

	// Verify file exists on disk
	full := filepath.Join(tmpDir, key)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("file content = %q, want %q", data, content)
	}

	// GetObject
	rc, err := ls.GetObject(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != content {
		t.Errorf("GetObject = %q, want %q", got, content)
	}
}

func TestLocalStorage_PresignPut(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLocal(tmpDir, "http://localhost:8092")

	url, headers, err := ls.PresignPut(context.Background(), "skills/abc/file.zip", "application/zip", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(url, "/api/v1/_storage/upload/skills/abc/file.zip") {
		t.Errorf("url = %q, expected to contain upload path", url)
	}
	if headers.Get("Content-Type") != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", headers.Get("Content-Type"))
	}
}

func TestLocalStorage_PresignGet(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLocal(tmpDir, "http://localhost:8092")

	// File doesn't exist
	_, err := ls.PresignGet(context.Background(), "skills/abc/missing.zip", 0)
	if err == nil {
		t.Error("expected error for missing file")
	}

	// Write file first
	key := "skills/abc/present.zip"
	_ = ls.WriteObject(key, strings.NewReader("data"))

	url, err := ls.PresignGet(context.Background(), key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(url, "/api/v1/_storage/download/skills/abc/present.zip") {
		t.Errorf("url = %q, expected to contain download path", url)
	}
}

func TestLocalStorage_DeleteObject(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLocal(tmpDir, "http://localhost:8092")

	key := "skills/abc/delete-me.zip"
	_ = ls.WriteObject(key, strings.NewReader("data"))

	if err := ls.DeleteObject(context.Background(), key); err != nil {
		t.Fatal(err)
	}

	// File should be gone
	full := filepath.Join(tmpDir, key)
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Error("file should have been deleted")
	}

	// Deleting non-existent file should not error
	if err := ls.DeleteObject(context.Background(), "nonexistent"); err != nil {
		t.Errorf("delete nonexistent should not error: %v", err)
	}
}

func TestLocalStorage_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLocal(tmpDir, "http://localhost:8092")

	traversalKeys := []string{
		"../../../etc/passwd",
		"skills/../../etc/passwd",
		"/etc/passwd",
		"skills/../../../etc/shadow",
		"..\\..\\windows\\system32\\config\\sam",
	}

	for _, key := range traversalKeys {
		t.Run("WriteObject_"+key, func(t *testing.T) {
			err := ls.WriteObject(key, strings.NewReader("evil"))
			if err == nil {
				t.Errorf("WriteObject(%q) should have been rejected", key)
			}
		})

		t.Run("GetObject_"+key, func(t *testing.T) {
			_, err := ls.GetObject(context.Background(), key)
			if err == nil {
				t.Errorf("GetObject(%q) should have been rejected", key)
			}
		})

		t.Run("PresignPut_"+key, func(t *testing.T) {
			_, _, err := ls.PresignPut(context.Background(), key, "", 0)
			if err == nil {
				t.Errorf("PresignPut(%q) should have been rejected", key)
			}
		})

		t.Run("PresignGet_"+key, func(t *testing.T) {
			_, err := ls.PresignGet(context.Background(), key, 0)
			if err == nil {
				t.Errorf("PresignGet(%q) should have been rejected", key)
			}
		})

		t.Run("DeleteObject_"+key, func(t *testing.T) {
			err := ls.DeleteObject(context.Background(), key)
			if err == nil {
				t.Errorf("DeleteObject(%q) should have been rejected", key)
			}
		})
	}
}
