package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (s *LocalStorage) PutImmutable(ctx context.Context, key string, r io.Reader, size int64, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !IsImmutableArtifactKey(key) || size < 0 {
		return false, ErrImmutableObject
	}
	full, err := s.safePath(key)
	if err != nil {
		return false, err
	}
	if !s.isImmutablePath(full) {
		return false, ErrImmutableObject
	}
	if err = os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return false, err
	}
	scratch, err := s.safePath(".scratch")
	if err != nil {
		return false, err
	}
	if err = os.MkdirAll(scratch, 0o700); err != nil {
		return false, err
	}
	// Stage incomplete bytes outside the immutable object namespace. Link below
	// publishes the complete file atomically without replacing an existing key.
	f, err := os.CreateTemp(scratch, ".version-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(r, size+1))
	if err != nil {
		return false, err
	}
	if n != size {
		return false, fmt.Errorf("immutable artifact size mismatch")
	}
	if err = f.Close(); err != nil {
		return false, err
	}
	checked, err := s.safePath(key)
	if err != nil || checked != full {
		return false, ErrImmutableObject
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	// link is atomic and cannot replace an existing file; rename would overwrite.
	if err = os.Link(f.Name(), full); errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
