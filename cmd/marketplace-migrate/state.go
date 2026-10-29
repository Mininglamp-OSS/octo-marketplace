package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration"
)

// This private checkpoint retains the original plan for retries after heads change.
// It is not a database backup and must never be committed or shared.
type runState struct {
	Version             int
	Database            migration.DatabaseIdentity
	StorageID, PolicyID string
	Plan                migration.Plan
}

func validateState(s runState, db migration.DatabaseIdentity, storageID, policyID string) error {
	if s.Version != 5 || !s.Plan.CategorySnapshotCaptured || s.Database != db || s.StorageID != storageID || s.PolicyID != policyID || s.Plan.ID == "" || s.Plan.SnapshotID == "" {
		return fmt.Errorf("run_state_binding_mismatch")
	}
	return nil
}
func saveState(name string, s runState) error {
	f, e := os.CreateTemp(filepath.Dir(name), ".marketplace-run-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	if e = gob.NewEncoder(io.MultiWriter(f, h)).Encode(s); e != nil {
		return e
	}
	if _, e = f.Write(h.Sum(nil)); e != nil {
		return e
	}
	info, statErr := f.Stat()
	if statErr != nil {
		return statErr
	}
	if info.Size() > 1<<30 {
		return fmt.Errorf("run_state_too_large")
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	// Link publishes atomically and refuses to replace an existing checkpoint.
	return os.Link(f.Name(), name)
}
func loadState(name string) (runState, error) {
	var s runState
	info, e := os.Lstat(name)
	if e != nil {
		return s, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 32 || info.Size() > 1<<30 {
		return s, fmt.Errorf("run_state_invalid")
	}
	f, e := os.Open(name)
	if e != nil {
		return s, e
	}
	defer f.Close()
	actual, e := f.Stat()
	if e != nil || !os.SameFile(info, actual) {
		return s, fmt.Errorf("run_state_invalid")
	}
	n := info.Size() - sha256.Size
	h := sha256.New()
	if _, e = io.CopyN(h, f, n); e != nil {
		return s, e
	}
	sum := make([]byte, sha256.Size)
	if _, e = io.ReadFull(f, sum); e != nil || !bytes.Equal(sum, h.Sum(nil)) {
		return s, fmt.Errorf("run_state_checksum_failed")
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return s, e
	}
	d := gob.NewDecoder(io.LimitReader(f, n))
	if e = d.Decode(&s); e != nil {
		return s, e
	}
	var extra runState
	if d.Decode(&extra) != io.EOF {
		return s, fmt.Errorf("run_state_invalid")
	}
	return s, nil
}
