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
// It rejects absolute paths, dot-dot segments, and any key that would escape baseDir.
func (s *LocalStorage) safePath(key string) (string, error) {
	// Reject absolute paths
	if filepath.IsAbs(key) {
		return "", fmt.Errorf("absolute path not allowed: %s", key)
	}
	// Reject dot-dot segments
	if strings.Contains(key, "..") {
		return "", fmt.Errorf("path traversal not allowed: %s", key)
	}
	// Clean and join
	cleaned := filepath.Clean(key)
	full := filepath.Join(s.baseDir, cleaned)
	// Final check: resolved path must be under baseDir
	absBase, _ := filepath.Abs(s.baseDir)
	absFull, _ := filepath.Abs(full)
	if !strings.HasPrefix(absFull, absBase+string(filepath.Separator)) && absFull != absBase {
		return "", fmt.Errorf("path escapes base directory: %s", key)
	}
	return full, nil
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

// GetObject opens the local file for reading.
func (s *LocalStorage) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	full, err := s.safePath(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("local storage: open: %w", err)
	}
	return f, nil
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
func (s *LocalStorage) WriteObject(key string, r io.Reader) error {
	full, err := s.safePath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("local storage: mkdir: %w", err)
	}
	f, err := os.Create(full)
	if err != nil {
		return fmt.Errorf("local storage: create: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("local storage: write: %w", err)
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
	src, err := os.Open(srcFull)
	if err != nil {
		return fmt.Errorf("local storage: copy open src: %w", err)
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dstFull), 0o755); err != nil {
		return fmt.Errorf("local storage: copy mkdir: %w", err)
	}
	dst, err := os.Create(dstFull)
	if err != nil {
		return fmt.Errorf("local storage: copy create dst: %w", err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("local storage: copy: %w", err)
	}
	return nil
}
