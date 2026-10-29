// Package artifacts reads frozen source files without executing their contents.
package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/artifactscope"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service/parse"
)

type File struct {
	ObjectKey string
	Path      string
	MIME      string
	Data      []byte
}
type Limits struct {
	MaxFiles                                     int
	MaxFileBytes, MaxTotalBytes, MaxArchiveBytes int64
}

// Match the default service readers: 20 MiB per attachment and 100 MiB in total.
func DefaultLimits() Limits { return Limits{4096, 20 << 20, 100 << 20, 100 << 20} }

// Reader must enforce maxBytes before allocating the full object. Callers also
// check the returned length so an invalid adapter cannot bypass the contract.
type Reader func(context.Context, string, int64) ([]byte, error)

// NewReader bounds object-store reads, including the one-byte overflow probe.
func NewReader(open func(context.Context, string) (io.ReadCloser, error)) Reader {
	return func(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
		if maxBytes < 0 || maxBytes > DefaultLimits().MaxArchiveBytes {
			return nil, fmt.Errorf("object_bytes_exceeded")
		}
		r, err := open(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("object_read_failed")
		}
		defer r.Close()
		b, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
		if err != nil {
			return nil, fmt.Errorf("object_read_failed")
		}
		if int64(len(b)) > maxBytes {
			return nil, fmt.Errorf("object_bytes_exceeded")
		}
		return b, nil
	}
}

type attachment struct {
	Path string `json:"path"`
	Type string `json:"content_type"`
	MIME string `json:"mime_type"`
	Raw  string `json:"raw_content"`
	Key  string `json:"storage_uri"`
	Size int64  `json:"content_size"`
	Hash string `json:"content_hash"`
}
type ref struct {
	Object string `json:"object_key"`
	Zip    string `json:"zip_object_key"`
	URL    string `json:"file_url"`
}

func validPath(s string) bool {
	return s != "" && utf8.ValidString(s) && path.Clean(s) == s && s != "." && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "../") && !strings.ContainsAny(s, "\\\x00:\r\n")
}
func validKey(s string) bool { return validPath(s) && len(s) <= 512 && !strings.ContainsAny(s, "?#") }
func allowedKey(k string, p *model.Plugin) bool {
	if !validKey(k) {
		return false
	}
	if k == p.Icon && artifactscope.IsManagedIconKey(k) {
		return true
	}
	if strings.HasPrefix(k, "plugins/") || strings.HasPrefix(k, "platform/") {
		return artifactscope.Allows(p, k)
	}
	return legacyKeyOwned(k, p)
}
func Resolve(ctx context.Context, p *model.Plugin, read Reader, l Limits) ([]File, error) {
	files, _, err := ResolveWithIcon(ctx, p, read, l)
	return files, err
}

// ResolveWithIcon includes the scalar managed icon in the same deduplicated
// source-read budget. It retains the globally uploaded key, never follows URLs.
func ResolveWithIcon(ctx context.Context, p *model.Plugin, read Reader, l Limits) ([]File, *File, error) {
	var icon File
	files, err := resolve(ctx, p, read, l, &icon)
	if icon.ObjectKey == "" {
		return files, nil, err
	}
	return files, &icon, err
}

func resolve(ctx context.Context, p *model.Plugin, read Reader, l Limits, icon *File) ([]File, error) {
	if p == nil || l.MaxFiles <= 0 || l.MaxFileBytes <= 0 || l.MaxTotalBytes <= 0 || l.MaxArchiveBytes <= 0 {
		return nil, fmt.Errorf("invalid_artifact_limits")
	}
	var doc struct {
		Attachments []attachment `json:"attachments"`
	}
	if model.ValidateUniqueJSONFields(p.Package) != nil || json.Unmarshal(p.Package, &doc) != nil || len(doc.Attachments) == 0 || len(doc.Attachments) > l.MaxFiles+2 {
		return nil, fmt.Errorf("invalid_package_attachments")
	}
	seen := map[string]bool{}
	var pointer ref
	var zipAtt *attachment
	var envelope map[string]json.RawMessage
	var rawAttachments []json.RawMessage
	_ = json.Unmarshal(p.Package, &envelope)
	_ = json.Unmarshal(envelope["attachments"], &rawAttachments)
	for i, a := range doc.Attachments {
		if !validPath(a.Path) || seen[a.Path] {
			return nil, fmt.Errorf("invalid_or_duplicate_attachment_path")
		}
		seen[a.Path] = true
		if a.Path == "skill/ref.json" {
			if a.Type != "raw" || model.ValidateUniqueJSONFields([]byte(a.Raw)) != nil || json.Unmarshal([]byte(a.Raw), &pointer) != nil {
				return nil, fmt.Errorf("invalid_legacy_ref")
			}
		}
		if a.Path == "skill/package.zip" {
			zipAtt = &doc.Attachments[i]
		}
	}
	assertionPackage := p.Package
	if pointer.Object != "" {
		effective := []json.RawMessage{}
		for i, a := range doc.Attachments {
			if a.Path != "SKILL.md" {
				effective = append(effective, rawAttachments[i])
			}
		}
		envelope["attachments"], _ = json.Marshal(effective)
		assertionPackage, _ = json.Marshal(envelope)
	}
	assertions, err := ReferenceAssertions("plugin_json", assertionPackage, p.AttachmentKeys)
	if err != nil {
		return nil, err
	}
	for _, a := range assertions {
		if !allowedKey(a.Key, p) {
			return nil, fmt.Errorf("object_scope_mismatch")
		}
	}
	keys := map[string]string{}
	if len(p.AttachmentKeys) > 0 && string(p.AttachmentKeys) != "null" {
		if model.ValidateUniqueJSONFields(p.AttachmentKeys) != nil || json.Unmarshal(p.AttachmentKeys, &keys) != nil {
			return nil, fmt.Errorf("invalid_attachment_sidecar")
		}
	}
	// Classify archive keys before any read, so ordinary files get the smaller
	// file ceiling even when first encountered through an assertion.
	archiveKeys := map[string]bool{}
	for _, a := range doc.Attachments {
		if p.Type == model.PluginTypeSkill && a.Path == "skill/package.zip" && a.Type == "storage" {
			key := keys[a.Path]
			if key == "" {
				key = a.Key
			}
			archiveKeys[key] = true
		}
		if p.Type == model.PluginTypeSkill && a.Path == "skill/ref.json" && a.Type == "raw" {
			var r ref
			if model.ValidateUniqueJSONFields([]byte(a.Raw)) != nil || json.Unmarshal([]byte(a.Raw), &r) != nil {
				return nil, fmt.Errorf("invalid_legacy_ref")
			}
			if r.Zip != "" {
				archiveKeys[r.Zip] = true
			} else if validKey(r.URL) {
				archiveKeys[r.URL] = true
			}
		}
	}
	cache := map[string][]byte{}
	var readBytes int64
	load := func(key string) ([]byte, error) {
		if !allowedKey(key, p) {
			return nil, fmt.Errorf("object_scope_mismatch")
		}
		if b, ok := cache[key]; ok {
			return b, nil
		}
		maxBytes := l.MaxFileBytes
		if archiveKeys[key] {
			maxBytes = l.MaxArchiveBytes
		}
		if remaining := l.MaxTotalBytes - readBytes; remaining < maxBytes {
			maxBytes = remaining
		}
		if maxBytes < 0 {
			return nil, fmt.Errorf("file_budget_exceeded")
		}
		if read == nil {
			return nil, fmt.Errorf("object_reader_required")
		}
		data, err := read(ctx, key, maxBytes)
		if err != nil {
			return nil, fmt.Errorf("object_read_failed")
		}
		if int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("object_bytes_exceeded")
		}
		readBytes += int64(len(data))
		cache[key] = data
		return data, nil
	}
	for _, a := range assertions {
		maxBytes := l.MaxFileBytes
		if archiveKeys[a.Key] {
			maxBytes = l.MaxArchiveBytes
		}
		if a.Size != nil {
			if *a.Size > maxBytes {
				return nil, fmt.Errorf("object_bytes_exceeded")
			}
			if _, ok := cache[a.Key]; !ok && *a.Size > l.MaxTotalBytes-readBytes {
				return nil, fmt.Errorf("file_budget_exceeded")
			}
		}
		data, err := load(a.Key)
		if err != nil {
			return nil, err
		}
		if err := verifyReferenceAssertion(a, data); err != nil {
			return nil, err
		}
	}
	get := func(a attachment) ([]byte, error) {
		if a.Type == "raw" {
			return []byte(a.Raw), nil
		}
		if a.Type != "storage" {
			return nil, fmt.Errorf("unsupported_attachment_type")
		}
		key := keys[a.Path]
		if key == "" {
			key = a.Key
		}
		data, e := load(key)
		if e != nil {
			return nil, e
		}
		if int64(len(data)) != a.Size {
			return nil, fmt.Errorf("object_size_mismatch")
		}
		if a.Hash != "" {
			h := sha256.Sum256(data)
			if a.Hash != "sha256:"+hex.EncodeToString(h[:]) {
				return nil, fmt.Errorf("object_digest_mismatch")
			}
		}
		return data, nil
	}
	var files []File
	var fileBytes int64
	add := func(name, typ string, data []byte) error {
		if !validPath(name) || strings.Contains(strings.ToLower("/"+name+"/"), "/.git/") {
			return fmt.Errorf("unsafe_file_path")
		}
		if len(files) >= l.MaxFiles || int64(len(data)) > l.MaxFileBytes {
			return fmt.Errorf("file_budget_exceeded")
		}
		if typ == "" {
			typ = mime.TypeByExtension(path.Ext(name))
			if typ == "" {
				if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
					typ = "text/plain"
				} else {
					typ = "application/octet-stream"
				}
			}
		}
		for _, f := range files {
			if f.Path == name {
				if bytes.Equal(f.Data, data) && compatibleArtifactMIME(f.MIME, typ) {
					return nil
				}
				return fmt.Errorf("duplicate_file_path")
			}
		}
		if int64(len(data)) > l.MaxTotalBytes-fileBytes {
			return fmt.Errorf("file_budget_exceeded")
		}
		fileBytes += int64(len(data))
		files = append(files, File{Path: name, MIME: typ, Data: append([]byte(nil), data...)})
		return nil
	}
	zipKey := pointer.Zip
	if zipKey == "" && validKey(pointer.URL) {
		zipKey = pointer.URL
	}
	if p.Type == model.PluginTypeSkill && (zipAtt != nil || zipKey != "") {
		var data []byte
		var e error
		if zipAtt != nil {
			data, e = get(*zipAtt)
		} else {
			data, e = load(zipKey)
		}
		if e != nil {
			return nil, e
		}
		entries, code, _ := parse.ExtractArchive(bytes.NewReader(data), int64(len(data)), l.MaxTotalBytes, l.MaxFileBytes, l.MaxFiles)
		if code != "" {
			return nil, fmt.Errorf("archive_%s", strings.ToLower(code))
		}
		root := ""
		count := 0
		for n := range entries {
			if parse.IsSkillMDCandidate(n) {
				root = path.Dir(n)
				count++
			}
		}
		if count != 1 {
			return nil, fmt.Errorf("archive_skill_entry_ambiguous")
		}
		names := make([]string, 0, len(entries))
		for n := range entries {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			name := n
			if root != "." {
				if !strings.HasPrefix(n, root+"/") {
					return nil, fmt.Errorf("archive_file_outside_skill_root")
				}
				name = strings.TrimPrefix(n, root+"/")
			}
			if e = add(name, "", entries[n]); e != nil {
				return nil, e
			}
		}
		if pointer.Object != "" {
			md, e := load(pointer.Object)
			if e != nil {
				return nil, e
			}
			for i := range files {
				if files[i].Path == "SKILL.md" {
					next := fileBytes - int64(len(files[i].Data)) + int64(len(md))
					if int64(len(md)) > l.MaxFileBytes || next > l.MaxTotalBytes {
						return nil, fmt.Errorf("file_budget_exceeded")
					}
					fileBytes = next
					files[i].Data = md
				}
			}
		}
		for _, a := range doc.Attachments {
			if a.Path == "skill/ref.json" || a.Path == "skill/package.zip" || (a.Path == "SKILL.md" && (a.Type == "raw" || pointer.Object != "")) {
				continue
			}
			data, e := get(a)
			if e != nil {
				return nil, e
			}
			if e = add(a.Path, a.MIME, data); e != nil {
				return nil, e
			}
		}
	} else {
		for _, a := range doc.Attachments {
			if a.Path == "skill/ref.json" {
				continue
			}
			var data []byte
			var e error
			if a.Path == "SKILL.md" && pointer.Object != "" {
				data, e = load(pointer.Object)
			} else {
				data, e = get(a)
			}
			if e != nil {
				return nil, e
			}
			if e = add(a.Path, a.MIME, data); e != nil {
				return nil, e
			}
		}
		if seen["skill/ref.json"] && pointer.Object == "" && pointer.Zip == "" && pointer.URL != "" {
			return nil, fmt.Errorf("unsupported_external_pointer")
		}
	}
	var total int64
	entry := "AGENTS.md"
	if p.Type == model.PluginTypeSkill {
		entry = "SKILL.md"
	}
	found := p.Type == model.PluginTypeConnector
	for _, f := range files {
		total += int64(len(f.Data))
		if int64(len(f.Data)) > l.MaxFileBytes || total > l.MaxTotalBytes {
			return nil, fmt.Errorf("file_budget_exceeded")
		}
		if f.Path == entry && utf8.Valid(f.Data) && strings.TrimSpace(string(f.Data)) != "" {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("required_entry_missing")
	}
	if p.Icon != "" && !strings.HasPrefix(p.Icon, "http://") && !strings.HasPrefix(p.Icon, "https://") && strings.Contains(p.Icon, "/") {
		if !artifactscope.IsManagedIconKey(p.Icon) || !validKey(p.Icon) {
			return nil, fmt.Errorf("icon_scope_mismatch")
		}
		data, err := load(p.Icon)
		if err != nil {
			return nil, err
		}
		*icon = File{Path: p.Icon, ObjectKey: p.Icon, MIME: mime.TypeByExtension(path.Ext(p.Icon)), Data: append([]byte(nil), data...)}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// ReferencedKeys examines only known pointer carriers. It never follows URLs.
func ReferencedKeys(column string, raw []byte) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	found := map[string]bool{}
	add := func(k string) error {
		if k == "" {
			return nil
		}
		if !validKey(k) {
			return fmt.Errorf("unsupported_external_pointer")
		}
		found[k] = true
		return nil
	}
	if column == "icon_key" || column == "file_url" || column == "icon" || column == "icon_url" || column == "zip_object_key" || column == "object_key" || column == "storage_uri" {
		if (column == "icon" || column == "icon_url" || column == "icon_key") && (!validKey(string(raw)) || !strings.Contains(string(raw), "/")) {
			return nil, nil
		}
		if err := add(string(raw)); err != nil {
			return nil, err
		}
	}
	if err := visitCarriers(column, raw, func(kind string, m map[string]any) error {
		if kind == "sidecar" {
			for _, value := range m {
				key, ok := value.(string)
				if !ok {
					return fmt.Errorf("invalid_attachment_sidecar")
				}
				if err := add(key); err != nil {
					return err
				}
			}
			return nil
		}
		fields := []string{}
		switch kind {
		case "ref":
			fields = []string{"object_key", "zip_object_key", "skill_md_object_key", "file_url"}
		case "artifact":
			fields = []string{"object_key"}
		case "storage":
			fields = []string{"storage_uri"}
		case "icons":
			fields = []string{"icon", "icon_url", "icon_key"}
		}
		for _, field := range fields {
			if value, ok := m[field]; ok {
				key, ok := value.(string)
				if !ok {
					return fmt.Errorf("invalid_object_pointer")
				}
				if kind == "icons" && !(strings.HasPrefix(key, "icons/") || strings.HasPrefix(key, "mcp-icons/")) {
					continue
				}
				if err := add(key); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// ReferenceAssertion preserves source-declared integrity independently of the
// snapshot's own digest of whatever bytes were downloaded.
type ReferenceAssertion struct {
	Key  string
	Hash string
	Size *int64
}

func ReferenceAssertions(column string, raw, sidecar []byte) ([]ReferenceAssertion, error) {
	keys := map[string]string{}
	if len(sidecar) > 0 && string(sidecar) != "null" {
		if json.Unmarshal(sidecar, &keys) != nil {
			return nil, fmt.Errorf("invalid_attachment_sidecar")
		}
	}
	out := []ReferenceAssertion{}
	add := func(key string, m map[string]any, hk, sk string) error {
		if key == "" {
			return nil
		}
		if !validKey(key) {
			return fmt.Errorf("unsupported_external_pointer")
		}
		a := ReferenceAssertion{Key: key}
		if v, ok := m[hk]; ok {
			h, ok := v.(string)
			if !ok {
				return fmt.Errorf("invalid_object_digest")
			}
			h = strings.TrimPrefix(h, "sha256:")
			decoded, e := hex.DecodeString(h)
			if e != nil || len(decoded) != 32 || strings.ToLower(h) != h {
				return fmt.Errorf("invalid_object_digest")
			}
			a.Hash = h
		}
		if v, ok := m[sk]; ok {
			num, ok := v.(json.Number)
			if !ok {
				return fmt.Errorf("invalid_object_size")
			}
			size, e := num.Int64()
			if e != nil || size < 0 {
				return fmt.Errorf("invalid_object_size")
			}
			a.Size = &size
		}
		out = append(out, a)
		return nil
	}
	visit := func(kind string, m map[string]any) error {
		switch kind {
		case "ref":
			for _, field := range []string{"object_key", "zip_object_key", "skill_md_object_key", "file_url"} {
				if value, ok := m[field]; ok {
					key, ok := value.(string)
					if !ok {
						return fmt.Errorf("invalid_object_pointer")
					}
					if e := add(key, m, field+"_hash", field+"_size"); e != nil {
						return e
					}
				}
			}
		case "artifact":
			if value, ok := m["object_key"]; ok {
				key, ok := value.(string)
				if !ok {
					return fmt.Errorf("invalid_object_pointer")
				}
				return add(key, m, "digest", "size")
			}
		case "storage":
			h, ok := m["content_hash"].(string)
			if !ok || !strings.HasPrefix(h, "sha256:") || len(h) != 71 {
				return fmt.Errorf("invalid_object_digest")
			}
			if _, ok := m["content_size"]; !ok {
				return fmt.Errorf("invalid_object_size")
			}
			key, _ := m["storage_uri"].(string)
			if filePath, ok := m["path"].(string); ok && keys[filePath] != "" {
				key = keys[filePath]
			}
			if key == "" {
				return fmt.Errorf("invalid_object_pointer")
			}
			return add(key, m, "content_hash", "content_size")
		}
		return nil
	}

	if column == "object_key" || column == "zip_object_key" || column == "file_url" || column == "storage_uri" {
		if e := add(string(raw), nil, "", ""); e != nil {
			return nil, e
		}
		return out, nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	if e := visitCarriers(column, raw, visit); e != nil {
		return nil, e
	}

	return out, nil
}
func verifyReferenceAssertion(a ReferenceAssertion, b []byte) error {
	if a.Size != nil && int64(len(b)) != *a.Size {
		return fmt.Errorf("object_size_mismatch")
	}
	if a.Hash != "" {
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != a.Hash {
			return fmt.Errorf("object_digest_mismatch")
		}
	}
	return nil
}

func VerifyReferenceAssertions(ctx context.Context, assertions []ReferenceAssertion, read Reader) error {
	cache := map[string][]byte{}
	var total int64
	limits := DefaultLimits()
	for _, a := range assertions {
		if read == nil {
			return fmt.Errorf("object_reader_required")
		}
		b, ok := cache[a.Key]
		if !ok {
			maxBytes := limits.MaxTotalBytes - total
			if a.Size != nil {
				if *a.Size < 0 || *a.Size > maxBytes {
					return fmt.Errorf("object_bytes_exceeded")
				}
				maxBytes = *a.Size
			}
			var err error
			b, err = read(ctx, a.Key, maxBytes)
			if err != nil {
				return fmt.Errorf("object_read_failed")
			}
			if int64(len(b)) > maxBytes {
				return fmt.Errorf("object_bytes_exceeded")
			}
			total += int64(len(b))
			cache[a.Key] = b
		}
		if err := verifyReferenceAssertion(a, b); err != nil {
			return err
		}
	}
	return nil
}

func compatibleArtifactMIME(a, b string) bool {
	aa, _, e := mime.ParseMediaType(a)
	bb, _, f := mime.ParseMediaType(b)
	return e == nil && f == nil && strings.EqualFold(aa, bb)
}
