package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// LocalStorage implements Storage using the local filesystem.
// Presigned URLs point to a local HTTP proxy endpoint.
type LocalStorage struct {
	baseDir string
	baseURL string // e.g. "http://127.0.0.1:8092"
}

// NewLocal creates a local storage backed by the given directory.
// baseURL is the server's own address used to construct presigned-like URLs.
func NewLocal(baseDir, baseURL string) *LocalStorage {
	return &LocalStorage{baseDir: baseDir, baseURL: baseURL}
}

// safePath validates and resolves a key to a safe absolute path within baseDir.
// Existing path components are evaluated through symlinks so a link inside the
// storage tree cannot redirect an operation outside the configured root.
func (s *LocalStorage) safePath(key string) (string, error) {
	// Reject absolute paths
	if filepath.IsAbs(key) {
		return "", fmt.Errorf("absolute path not allowed: %s", key)
	}
	// Reject dot-dot segments
	if strings.Contains(key, "..") {
		return "", fmt.Errorf("path traversal not allowed: %s", key)
	}
	if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
		return "", fmt.Errorf("local storage: mkdir base: %w", err)
	}

	cleaned := filepath.Clean(key)
	absBase, err := filepath.Abs(s.baseDir)
	if err != nil {
		return "", fmt.Errorf("local storage: resolve base: %w", err)
	}
	realBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		return "", fmt.Errorf("local storage: evaluate base: %w", err)
	}
	absFull := filepath.Join(absBase, cleaned)
	realFull, err := resolveExistingPath(absFull)
	if err != nil {
		return "", err
	}
	if !pathWithin(realBase, realFull) {
		return "", fmt.Errorf("path escapes base directory: %s", key)
	}
	return realFull, nil
}

// resolveExistingPath evaluates the nearest existing ancestor and appends any
// not-yet-created suffix. This catches symlinked directories for both reads and
// new writes.
func resolveExistingPath(path string) (string, error) {
	candidate := path
	var missing []string
	for {
		_, err := os.Lstat(candidate)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return "", fmt.Errorf("local storage: evaluate path: %w", err)
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("local storage: inspect path: %w", err)
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", fmt.Errorf("local storage: no existing path ancestor")
		}
		missing = append(missing, filepath.Base(candidate))
		candidate = parent
	}
}

func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// PresignPut returns a URL to which the client can PUT a file.
// For local storage, this is a backend proxy endpoint.
func (s *LocalStorage) PresignPut(_ context.Context, key string, contentType string, _ time.Duration) (string, http.Header, error) {
	full, err := s.safePath(key)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", nil, fmt.Errorf("local storage: mkdir: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/_storage/upload/%s", s.baseURL, key)
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return url, h, nil
}

// PresignGet returns a URL from which the client can GET the file.
func (s *LocalStorage) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	full, err := s.safePath(key)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(full); err != nil {
		return "", fmt.Errorf("local storage: file not found: %w", err)
	}
	url := fmt.Sprintf("%s/api/v1/_storage/download/%s", s.baseURL, key)
	return url, nil
}

// PublicURL returns the persistent proxy URL for a key without checking
// whether the file exists — unlike PresignGet, this is called BEFORE the
// upload happens (we hand the URL back to the client to store on the
// record, then the client PUTs the bytes to the presigned put URL).
func (s *LocalStorage) PublicURL(_ context.Context, key string) (string, error) {
	if _, err := s.safePath(key); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/api/v1/_storage/download/%s", s.baseURL, key), nil
}

// GetObject opens the local file for reading.
func (s *LocalStorage) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	full, err := s.safePath(key)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(full, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("local storage: open: %w", err)
	}
	return f, nil
}

// StatObject returns local object metadata without opening the body for parsing.
func (s *LocalStorage) StatObject(_ context.Context, key string) (ObjectInfo, error) {
	full, err := s.safePath(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("local storage: stat: %w", err)
	}
	return ObjectInfo{Size: info.Size()}, nil
}

// PutObject writes data from a reader to the local filesystem.
func (s *LocalStorage) PutObject(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	return s.WriteObject(key, reader)
}

// DeleteObject removes the file from disk.
func (s *LocalStorage) DeleteObject(_ context.Context, key string) error {
	full, err := s.safePath(key)
	if err != nil {
		return err
	}
	err = os.Remove(full)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("local storage: remove: %w", err)
	}
	return nil
}

// WriteObject writes data to the local filesystem (used by the local upload proxy).
func (s *LocalStorage) WriteObject(key string, r io.Reader) (retErr error) {
	full, err := s.safePath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("local storage: mkdir: %w", err)
	}
	parent := filepath.Dir(full)
	f, err := os.CreateTemp(parent, ".upload-*")
	if err != nil {
		return fmt.Errorf("local storage: create temp: %w", err)
	}
	tmpName := f.Name()
	defer func() {
		_ = f.Close()
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("local storage: write: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("local storage: close: %w", err)
	}
	// Re-check the destination after the write window before the atomic move.
	checked, err := s.safePath(key)
	if err != nil || checked != full {
		return fmt.Errorf("local storage: destination changed during upload")
	}
	if err := os.Rename(tmpName, full); err != nil {
		return fmt.Errorf("local storage: commit upload: %w", err)
	}
	return nil
}

// CopyObject copies a file from srcKey to dstKey within the local filesystem.
func (s *LocalStorage) CopyObject(_ context.Context, srcKey, dstKey string) error {
	srcFull, err := s.safePath(srcKey)
	if err != nil {
		return err
	}
	dstFull, err := s.safePath(dstKey)
	if err != nil {
		return err
	}
	src, err := os.OpenFile(srcFull, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("local storage: copy open src: %w", err)
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dstFull), 0o755); err != nil {
		return fmt.Errorf("local storage: copy mkdir: %w", err)
	}
	dst, err := os.CreateTemp(filepath.Dir(dstFull), ".copy-*")
	if err != nil {
		return fmt.Errorf("local storage: copy create temp: %w", err)
	}
	tmpName := dst.Name()
	committed := false
	defer func() {
		_ = dst.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("local storage: copy: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("local storage: copy close: %w", err)
	}
	checked, err := s.safePath(dstKey)
	if err != nil || checked != dstFull {
		return fmt.Errorf("local storage: copy destination changed")
	}
	if err := os.Rename(tmpName, dstFull); err != nil {
		return fmt.Errorf("local storage: copy commit: %w", err)
	}
	committed = true
	return nil
}
