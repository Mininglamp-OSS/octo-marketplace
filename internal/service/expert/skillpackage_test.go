package expert

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

// memObjectStore is an in-memory storage.Storage for exercising the whole-
// package skill path (presign → upload → extract → copy) without OSS/disk.
type memObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemObjectStore() *memObjectStore {
	return &memObjectStore{objects: map[string][]byte{}}
}

func (m *memObjectStore) PresignPut(_ context.Context, key, _ string, _ time.Duration) (string, http.Header, error) {
	h := http.Header{}
	h.Set("Content-Type", "application/zip")
	return "https://obj.example/put/" + key, h, nil
}

func (m *memObjectStore) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[key]; !ok {
		return "", fmt.Errorf("not found: %s", key)
	}
	return "https://obj.example/get/" + key, nil
}

func (m *memObjectStore) PublicURL(_ context.Context, key string) (string, error) {
	return "https://obj.example/" + key, nil
}

func (m *memObjectStore) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}

func (m *memObjectStore) StatObject(_ context.Context, key string) (storage.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return storage.ObjectInfo{}, fmt.Errorf("not found: %s", key)
	}
	return storage.ObjectInfo{Size: int64(len(data))}, nil
}

func (m *memObjectStore) PutObject(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	return nil
}

func (m *memObjectStore) DeleteObject(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *memObjectStore) CopyObject(_ context.Context, src, dst string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[src]
	if !ok {
		return fmt.Errorf("not found: %s", src)
	}
	m.objects[dst] = append([]byte(nil), data...)
	return nil
}

func (m *memObjectStore) has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[key]
	return ok
}

func (m *memObjectStore) get(key string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objects[key]
}

// newServiceWithObjects builds a service backed by both the in-memory repo fake
// and the in-memory object store, so the skill-package path is exercised.
func newServiceWithObjects() (*Service, *fakeStore, *memObjectStore) {
	repo := newFakeStore()
	obj := newMemObjectStore()
	seq := 0
	svc := New(repo, obj, func() string {
		seq++
		return fmt.Sprintf("id-%d", seq)
	})
	svc.now = func() time.Time { return time.Date(2026, 8, 6, 10, 15, 0, 0, time.UTC) }
	return svc, repo, obj
}

// buildSkillZip returns a .skill package: a SKILL.md (with the given frontmatter
// name) plus the extra files listed.
func buildSkillZip(t *testing.T, name string, extra map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	skillMD := fmt.Sprintf("---\nname: %s\ndescription: test\n---\n\n# %s\n\nbody\n", name, name)
	w, err := zw.Create("SKILL.md")
	if err != nil {
		t.Fatalf("zip create SKILL.md: %v", err)
	}
	if _, err := w.Write([]byte(skillMD)); err != nil {
		t.Fatalf("zip write SKILL.md: %v", err)
	}
	for path, content := range extra {
		fw, err := zw.Create(path)
		if err != nil {
			t.Fatalf("zip create %s: %v", path, err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", path, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestCreateExpert_SkillPackage_StoresAndExtracts(t *testing.T) {
	svc, _, obj := newServiceWithObjects()
	ctx := context.Background()

	zipBytes := buildSkillZip(t, "架构评审清单", map[string]string{
		"scripts/run.py":     "print('hi')",
		"references/spec.md": "# spec",
	})
	uploadKey := "expert-uploads/up-1/arch.skill"
	if err := obj.PutObject(ctx, uploadKey, bytes.NewReader(zipBytes), int64(len(zipBytes)), "application/zip"); err != nil {
		t.Fatalf("seed upload: %v", err)
	}

	req := baseExpert()
	req.Skills = []model.SkillWrite{{
		Name:            "ignored-client-name",
		UploadObjectKey: uploadKey,
		FileName:        "arch.skill",
		FileSize:        int64(len(zipBytes)),
	}}

	detail, err := svc.CreateExpert(ctx, callerA, req)
	if err != nil {
		t.Fatalf("CreateExpert: %v", err)
	}
	if len(detail.Skills) != 1 {
		t.Fatalf("want 1 skill, got %d", len(detail.Skills))
	}
	sk := detail.Skills[0]
	if sk.Name != "架构评审清单" {
		t.Errorf("name = %q, want frontmatter name 架构评审清单", sk.Name)
	}
	if !sk.HasContent || !sk.CanDownload {
		t.Errorf("has_content=%v can_download=%v, want both true", sk.HasContent, sk.CanDownload)
	}
	if sk.FileName != "arch.skill" || sk.FileSize != int64(len(zipBytes)) {
		t.Errorf("file meta = %q/%d", sk.FileName, sk.FileSize)
	}
	if !containsAll(sk.Files, "SKILL.md", "scripts/run.py", "references/spec.md") {
		t.Errorf("files manifest = %v", sk.Files)
	}

	// The permanent objects exist; the temp upload was cleaned up.
	stored, _ := svc.repo.GetExpertByID(ctx, detail.ExpertID)
	ref := stored.Skills[0]
	if ref.ObjectKey == "" || ref.ZipObjectKey == "" {
		t.Fatalf("stored ref missing object keys: %+v", ref)
	}
	if !strings.HasPrefix(ref.ObjectKey, "experts/"+detail.ExpertID+"/skills/") {
		t.Errorf("unexpected md key %q", ref.ObjectKey)
	}
	if !obj.has(ref.ObjectKey) || !obj.has(ref.ZipObjectKey) {
		t.Errorf("permanent objects missing (md=%q zip=%q)", ref.ObjectKey, ref.ZipObjectKey)
	}
	if obj.has(uploadKey) {
		t.Errorf("temp upload %s should have been deleted", uploadKey)
	}
	if !bytes.Equal(obj.get(ref.ZipObjectKey), zipBytes) {
		t.Errorf("stored zip differs from uploaded package")
	}

	// skill_md serves the extracted SKILL.md; download presigns the zip.
	md, err := svc.GetExpertSkillMD(ctx, callerA, detail.ExpertID, 0)
	if err != nil {
		t.Fatalf("GetExpertSkillMD: %v", err)
	}
	if !strings.Contains(md, "# 架构评审清单") {
		t.Errorf("skill_md missing body: %q", md)
	}
	url, err := svc.GetExpertSkillDownload(ctx, callerA, detail.ExpertID, 0)
	if err != nil || url == "" {
		t.Fatalf("GetExpertSkillDownload: url=%q err=%v", url, err)
	}
}

func TestCreateExpert_SkillPackage_NoSkillMD_Rejected(t *testing.T) {
	svc, _, obj := newServiceWithObjects()
	ctx := context.Background()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("README.md")
	_, _ = w.Write([]byte("# not a skill"))
	_ = zw.Close()

	uploadKey := "expert-uploads/up-2/bad.zip"
	_ = obj.PutObject(ctx, uploadKey, bytes.NewReader(buf.Bytes()), int64(buf.Len()), "application/zip")

	req := baseExpert()
	req.Skills = []model.SkillWrite{{UploadObjectKey: uploadKey, FileName: "bad.zip"}}
	if _, err := svc.CreateExpert(ctx, callerA, req); err == nil {
		t.Fatalf("want error for package without SKILL.md, got nil")
	}
}

func TestCreateExpert_SkillPackage_BadUploadPrefix_Rejected(t *testing.T) {
	svc, _, obj := newServiceWithObjects()
	ctx := context.Background()

	zipBytes := buildSkillZip(t, "x", nil)
	// Key NOT under expert-uploads/ — must be rejected even though it exists.
	_ = obj.PutObject(ctx, "experts/other/skills/0/skill.zip", bytes.NewReader(zipBytes), int64(len(zipBytes)), "application/zip")

	req := baseExpert()
	req.Skills = []model.SkillWrite{{UploadObjectKey: "experts/other/skills/0/skill.zip", FileName: "x.zip"}}
	if _, err := svc.CreateExpert(ctx, callerA, req); err == nil {
		t.Fatalf("want error for upload key outside expert-uploads/, got nil")
	}

	// Path-traversal in the upload key is rejected even under the prefix.
	traversal := "expert-uploads/../experts/victim/skills/0/skill.zip"
	_ = obj.PutObject(ctx, traversal, bytes.NewReader(zipBytes), int64(len(zipBytes)), "application/zip")
	req2 := baseExpert()
	req2.Skills = []model.SkillWrite{{UploadObjectKey: traversal, FileName: "x.zip"}}
	if _, err := svc.CreateExpert(ctx, callerA, req2); err == nil {
		t.Fatalf("want error for traversal upload key, got nil")
	}
}

func TestPatchExpert_NameOnlySkill_PreservesPackage(t *testing.T) {
	svc, _, obj := newServiceWithObjects()
	ctx := context.Background()

	zipBytes := buildSkillZip(t, "架构评审清单", map[string]string{"scripts/run.py": "print(1)"})
	uploadKey := "expert-uploads/up-p/arch.skill"
	if err := obj.PutObject(ctx, uploadKey, bytes.NewReader(zipBytes), int64(len(zipBytes)), "application/zip"); err != nil {
		t.Fatalf("seed upload: %v", err)
	}
	req := baseExpert()
	req.Skills = []model.SkillWrite{{UploadObjectKey: uploadKey, FileName: "arch.skill", FileSize: int64(len(zipBytes))}}
	created, err := svc.CreateExpert(ctx, callerA, req)
	if err != nil {
		t.Fatalf("CreateExpert: %v", err)
	}
	// Capture the stored object keys before the PATCH.
	stored, _ := svc.repo.GetExpertByID(ctx, created.ExpertID)
	if len(stored.Skills) != 1 || stored.Skills[0].ZipObjectKey == "" {
		t.Fatalf("expected a stored package skill, got %+v", stored.Skills)
	}
	zipKey := stored.Skills[0].ZipObjectKey
	mdKey := stored.Skills[0].ObjectKey

	// Read-modify-write PATCH: change summary, resend the skill NAME-ONLY (exactly
	// what the client does, since the read side never returns content/upload key).
	newSummary := "更新后的简介"
	patched, err := svc.PatchExpert(ctx, callerA, created.ExpertID, model.ExpertPatchRequest{
		Summary: &newSummary,
		Skills:  &[]model.SkillWrite{{Name: "架构评审清单"}},
	})
	if err != nil {
		t.Fatalf("PatchExpert: %v", err)
	}
	if len(patched.Skills) != 1 {
		t.Fatalf("want 1 skill after patch, got %d", len(patched.Skills))
	}
	if !patched.Skills[0].HasContent || !patched.Skills[0].CanDownload {
		t.Errorf("PATCH wiped the package: has_content=%v can_download=%v",
			patched.Skills[0].HasContent, patched.Skills[0].CanDownload)
	}
	// The stored keys must be unchanged and the objects still present.
	after, _ := svc.repo.GetExpertByID(ctx, created.ExpertID)
	if after.Skills[0].ZipObjectKey != zipKey || after.Skills[0].ObjectKey != mdKey {
		t.Errorf("object keys changed on preserve: md %q→%q zip %q→%q",
			mdKey, after.Skills[0].ObjectKey, zipKey, after.Skills[0].ZipObjectKey)
	}
	if !obj.has(zipKey) || !obj.has(mdKey) {
		t.Errorf("preserved objects missing after patch")
	}
	// skill_md + download still resolve.
	if _, err := svc.GetExpertSkillMD(ctx, callerA, created.ExpertID, 0); err != nil {
		t.Errorf("GetExpertSkillMD after preserve: %v", err)
	}
	if url, err := svc.GetExpertSkillDownload(ctx, callerA, created.ExpertID, 0); err != nil || url == "" {
		t.Errorf("GetExpertSkillDownload after preserve: url=%q err=%v", url, err)
	}
}

func TestPatchExpert_DropSkill_RemovesFromList(t *testing.T) {
	svc, _, obj := newServiceWithObjects()
	ctx := context.Background()

	zipBytes := buildSkillZip(t, "临时技能", nil)
	uploadKey := "expert-uploads/up-d/tmp.skill"
	_ = obj.PutObject(ctx, uploadKey, bytes.NewReader(zipBytes), int64(len(zipBytes)), "application/zip")
	req := baseExpert()
	req.Skills = []model.SkillWrite{{UploadObjectKey: uploadKey, FileName: "tmp.skill"}}
	created, err := svc.CreateExpert(ctx, callerA, req)
	if err != nil {
		t.Fatalf("CreateExpert: %v", err)
	}
	// PATCH with an empty skill list drops the skill.
	patched, err := svc.PatchExpert(ctx, callerA, created.ExpertID, model.ExpertPatchRequest{
		Skills: &[]model.SkillWrite{},
	})
	if err != nil {
		t.Fatalf("PatchExpert: %v", err)
	}
	if len(patched.Skills) != 0 {
		t.Errorf("want 0 skills after drop, got %d", len(patched.Skills))
	}
}

func TestInitSkillUpload(t *testing.T) {
	svc, _, _ := newServiceWithObjects()
	ctx := context.Background()

	init, err := svc.InitSkillUpload(ctx, "arch.skill", 1024)
	if err != nil {
		t.Fatalf("InitSkillUpload: %v", err)
	}
	if !strings.HasPrefix(init.UploadObjectKey, expertUploadPrefix) {
		t.Errorf("upload key = %q, want %s prefix", init.UploadObjectKey, expertUploadPrefix)
	}
	if !strings.HasSuffix(init.UploadObjectKey, "/arch.skill") {
		t.Errorf("upload key = %q, want filename suffix", init.UploadObjectKey)
	}
	if init.PresignedURL == "" || init.Method != http.MethodPut {
		t.Errorf("presigned = %q method = %q", init.PresignedURL, init.Method)
	}

	if _, err := svc.InitSkillUpload(ctx, "notes.txt", 10); err == nil {
		t.Errorf("want error for bad extension")
	}
	if _, err := svc.InitSkillUpload(ctx, "big.zip", maxSkillPackageBytes+1); err == nil {
		t.Errorf("want error for oversize")
	}
	if _, err := svc.InitSkillUpload(ctx, "empty.zip", 0); err == nil {
		t.Errorf("want error for non-positive size")
	}
}

func containsAll(haystack []string, needles ...string) bool {
	set := make(map[string]struct{}, len(haystack))
	for _, h := range haystack {
		set[h] = struct{}{}
	}
	for _, n := range needles {
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
}
