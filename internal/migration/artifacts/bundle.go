package artifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/snapshot"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type ObjectLimits struct{ MaxObjectBytes, MaxTotalBytes int64 }

func DefaultObjectLimits() ObjectLimits { return ObjectLimits{256 << 20, 10 << 30} }

type ObjectOpener func(context.Context, string) (io.ReadCloser, error)
type ObjectIndex struct {
	Format             int                      `json:"format"`
	AtomicWithDatabase bool                     `json:"atomic_with_database"`
	Entries            map[string]snapshot.File `json:"entries"`
}

func bundleError(code string) error { return fmt.Errorf("%s", code) }

// OpenLocalObject rejects symlinks in every component; Root also prevents path
// traversal and symlink races from escaping the selected directory.
func OpenLocalObject(dir, key string) (io.ReadCloser, error) {
	if !validKey(key) {
		return nil, bundleError("invalid_object_key")
	}
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, bundleError("unsafe_object_root")
	}
	root, e := os.OpenRoot(dir)
	if e != nil {
		return nil, bundleError("object_read_failed")
	}
	defer root.Close()
	parts := strings.Split(key, "/")
	for i := range parts {
		st, e = root.Lstat(strings.Join(parts[:i+1], "/"))
		if e != nil || st.Mode()&os.ModeSymlink != 0 {
			return nil, bundleError("unsafe_object_path")
		}
	}
	f, e := root.Open(key)
	if e != nil {
		return nil, bundleError("object_read_failed")
	}
	st, e = f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, bundleError("unsafe_object_file")
	}
	return f, nil
}

// Collect inventories all verified rows, streams only referenced objects, then
// seals the key index and each blob into the final snapshot identity.
func Collect(ctx context.Context, b snapshot.Bundle, open ObjectOpener, l ObjectLimits) (snapshot.Bundle, error) {
	if l.MaxObjectBytes <= 0 || l.MaxTotalBytes <= 0 {
		return b, bundleError("invalid_object_limits")
	}
	keys := map[string]bool{}
	for _, t := range b.Manifest.Tables {
		e := snapshot.ReadTable(b, t.Name, func(cols []string, vals []*[]byte) error {
			for i, v := range vals {
				if v == nil {
					continue
				}
				found, e := ReferencedKeys(cols[i], *v)
				if e != nil {
					return bundleError("object_inventory_failed")
				}
				for _, key := range found {
					keys[key] = true
				}
			}
			return nil
		})
		if e != nil {
			return b, bundleError("object_inventory_failed")
		}
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	idx := ObjectIndex{Format: 1, Entries: map[string]snapshot.File{}}
	var total int64
	unique := map[string]bool{}
	for _, key := range ordered {
		if ctx.Err() != nil {
			return b, bundleError("object_copy_cancelled")
		}
		if open == nil {
			return b, bundleError("object_reader_required")
		}
		r, e := open(ctx, key)
		if e != nil {
			return b, bundleError("object_read_failed")
		}
		limit := l.MaxObjectBytes
		if remaining := l.MaxTotalBytes - total; remaining < limit {
			limit = remaining
		}
		f, e := snapshot.AddObject(b.Dir, io.LimitReader(r, limit+1))
		ce := r.Close()
		if e != nil || ce != nil {
			return b, bundleError("object_copy_failed")
		}
		if f.Size > limit {
			return b, bundleError("object_budget_exceeded")
		}
		total += f.Size
		idx.Entries[key] = f
		if !unique[f.Path] {
			b.Manifest.Objects = append(b.Manifest.Objects, f)
			unique[f.Path] = true
		}
	}
	raw, e := json.Marshal(idx)
	if e != nil {
		return b, bundleError("object_index_encode_failed")
	}
	p := filepath.Join(b.Dir, "object-index.json")
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return b, bundleError("object_index_write_failed")
	}
	_, e = f.Write(raw)
	ce := f.Close()
	if e != nil || ce != nil {
		return b, bundleError("object_index_write_failed")
	}
	h := sha256.Sum256(raw)
	b.Manifest.Objects = append(b.Manifest.Objects, snapshot.File{Path: "object-index.json", SHA256: hex.EncodeToString(h[:]), Size: int64(len(raw))})
	b, e = snapshot.Seal(b)
	if e != nil {
		return b, bundleError("snapshot_seal_failed")
	}
	return b, nil
}
func loadIndex(b snapshot.Bundle) (ObjectIndex, error) {
	idx := ObjectIndex{}
	v, e := snapshot.Load(b.Dir)
	if e != nil || v.ID != b.ID {
		return idx, bundleError("snapshot_integrity_failed")
	}
	registered := map[string]snapshot.File{}
	for _, f := range v.Manifest.Objects {
		registered[f.Path] = f
	}
	f, ok := registered["object-index.json"]
	if !ok || f.Size > 16<<20 {
		return idx, bundleError("object_index_missing")
	}
	r, e := OpenLocalObject(b.Dir, "object-index.json")
	if e != nil {
		return idx, e
	}
	raw, e := io.ReadAll(io.LimitReader(r, 16<<20+1))
	r.Close()
	if e != nil || json.Unmarshal(raw, &idx) != nil || idx.Format != 1 || idx.Entries == nil {
		return idx, bundleError("object_index_invalid")
	}
	for key, f := range idx.Entries {
		if !validKey(key) || f.Path != "object-"+f.SHA256 || registered[f.Path] != f || f.Size < 0 || f.Size > DefaultObjectLimits().MaxObjectBytes {
			return idx, bundleError("object_index_invalid")
		}
	}
	return idx, nil
}
func BundleReader(b snapshot.Bundle) (Reader, error) {
	idx, e := loadIndex(b)
	if e != nil {
		return nil, e
	}
	return func(ctx context.Context, key string) ([]byte, error) {
		if ctx.Err() != nil {
			return nil, bundleError("object_read_cancelled")
		}
		f, ok := idx.Entries[key]
		if !ok {
			return nil, bundleError("object_not_in_snapshot")
		}
		r, e := OpenLocalObject(b.Dir, f.Path)
		if e != nil {
			return nil, e
		}
		defer r.Close()
		raw, e := io.ReadAll(io.LimitReader(r, f.Size+1))
		h := sha256.Sum256(raw)
		if e != nil || int64(len(raw)) != f.Size || hex.EncodeToString(h[:]) != f.SHA256 {
			return nil, bundleError("object_integrity_failed")
		}
		return raw, nil
	}, nil
}

// SourceObjectIndex returns only an integrity-checked index, preserving original
// source names so callers can distinguish host descriptors from opaque files.
func SourceObjectIndex(b snapshot.Bundle) (ObjectIndex, error) { return loadIndex(b) }

// WriteLocalObject is immutable and idempotent. Existing different bytes fail;
// no pre-existing object is overwritten. Successful writes are read back.
func WriteLocalObject(dir, key string, r io.Reader, size int64, digest string) error {
	if !validKey(key) || size < 0 || size > DefaultObjectLimits().MaxObjectBytes {
		return bundleError("invalid_target_object")
	}
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return bundleError("unsafe_target_storage")
	}
	root, e := os.OpenRoot(dir)
	if e != nil {
		return bundleError("unsafe_target_storage")
	}
	defer root.Close()
	parts := strings.Split(path.Dir(key), "/")
	if path.Dir(key) != "." {
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			st, e := root.Lstat(prefix)
			if os.IsNotExist(e) {
				if e = root.Mkdir(prefix, 0700); e != nil && !os.IsExist(e) {
					return bundleError("object_directory_failed")
				}
				st, e = root.Lstat(prefix)
			}
			if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				return bundleError("unsafe_target_path")
			}
		}
	}
	var nonce [16]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return bundleError("object_write_failed")
	}
	pending := path.Join(path.Dir(key), ".migration-pending-"+hex.EncodeToString(nonce[:]))
	f, e := root.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return bundleError("object_write_failed")
	}
	defer root.Remove(pending)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, size+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return bundleError("object_write_integrity_failed")
	}
	if e = root.Link(pending, key); e != nil && !os.IsExist(e) {
		return bundleError("object_write_failed")
	}
	read, e := OpenLocalObject(dir, key)
	if e != nil {
		return e
	}
	defer read.Close()
	h = sha256.New()
	n, e = io.Copy(h, io.LimitReader(read, size+1))
	if e != nil || n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return bundleError("target_object_mismatch")
	}
	return nil
}
func CopyBundleObjects(ctx context.Context, b snapshot.Bundle, target string) error {
	idx, e := loadIndex(b)
	if e != nil {
		return e
	}
	keys := make([]string, 0, len(idx.Entries))
	for k := range idx.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if ctx.Err() != nil {
			return bundleError("object_copy_cancelled")
		}
		f := idx.Entries[k]
		r, e := OpenLocalObject(b.Dir, f.Path)
		if e != nil {
			return e
		}
		e = WriteLocalObject(target, k, r, f.Size, f.SHA256)
		r.Close()
		if e != nil {
			return e
		}
	}
	return nil
}
