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
	if err = os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return false, err
	}
	f, err := os.CreateTemp(filepath.Dir(full), ".version-*")
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
