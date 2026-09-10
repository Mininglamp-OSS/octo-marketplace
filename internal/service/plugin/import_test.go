package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

type importStorage struct {
	objects map[string][]byte
	deletes []string
	events  *[]string
}

func (s *importStorage) PresignPut(context.Context, string, string, time.Duration) (string, http.Header, error) {
	return "", nil, errors.New("unexpected PresignPut")
}
func (s *importStorage) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://cdn.invalid/" + key, nil
}
func (s *importStorage) PublicURL(context.Context, string) (string, error) {
	return "", errors.New("unexpected PublicURL")
}
func (s *importStorage) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	value, ok := s.objects[key]
	if !ok {
		return nil, errors.New("missing object " + key)
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}
func (s *importStorage) StatObject(_ context.Context, key string) (storage.ObjectInfo, error) {
	value, ok := s.objects[key]
	if !ok {
		return storage.ObjectInfo{}, storage.ErrObjectNotFound
	}
	return storage.ObjectInfo{Size: int64(len(value))}, nil
}
func (s *importStorage) PutObject(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.objects[key] = data
	return nil
}
func (s *importStorage) DeleteObject(_ context.Context, key string) error {
	s.deletes = append(s.deletes, key)
	if s.events != nil {
		*s.events = append(*s.events, "delete:"+key)
	}
	delete(s.objects, key)
	return nil
}
func (s *importStorage) CopyObject(context.Context, string, string) error {
	return errors.New("unexpected CopyObject")
}

type fakeParseTasks struct {
	task       *skillrepo.ParseTaskRow
	consumed   []string
	released   []string
	consumeErr error
	events     *[]string
}

func (f *fakeParseTasks) GetParseTask(_ context.Context, id string) (*skillrepo.ParseTaskRow, error) {
	if f.task != nil && f.task.ID == id {
		return f.task, nil
	}
	return nil, nil
}
func (f *fakeParseTasks) MarkParseTaskConsumed(_ context.Context, id, ownerID, spaceID, skillID string) error {
	f.consumed = append(f.consumed, id+"|"+ownerID+"|"+spaceID+"|"+skillID)
	return f.consumeErr
}
func (f *fakeParseTasks) ReleaseConsumedParseTask(_ context.Context, id string) error {
	f.released = append(f.released, id)
	if f.events != nil {
		*f.events = append(*f.events, "release:"+id)
	}
	return nil
}

func skillZipFixture(t *testing.T) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	md, err := zw.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := md.Write([]byte("---\nname: uploaded-skill\nversion: 1.0.0\n---\n# Uploaded Skill\nBody.")); err != nil {
		t.Fatal(err)
	}
	extra, err := zw.Create("scripts/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extra.Write([]byte("echo ok")); err != nil {
		t.Fatal(err)
	}
	binary, err := zw.Create("assets/logo.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binary.Write([]byte{0x00, 0xff, 0xfe, 0x89, 0x50}); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func importFixtures(t *testing.T) (*fakeStore, *importStorage, *fakeParseTasks, *Service) {
	t.Helper()
	zipBytes, sha := skillZipFixture(t)
	store := &fakeStore{plugins: map[string]*model.Plugin{}}
	blobs := &importStorage{objects: map[string][]byte{"tmp/upload.zip": zipBytes}}
	tasks := &fakeParseTasks{task: &skillrepo.ParseTaskRow{
		ID: "task-1", OwnerID: "user-1", SpaceID: "space-a", Status: "success",
		FileName: "orig.zip", FileURL: "tmp/upload.zip", FileSize: int64(len(zipBytes)), FileSHA256: sha,
		ResultName: "My Skill", ResultVersion: "2.0.0", ResultTags: []byte(`["deploy"]`),
	}}
	svc := New(store, blobs).WithParseTasks(tasks).WithRuntime(sequenceIDs("plugin-new", "zip-obj", "md-obj", "audit-new", "version-new", "placement-new", "extra-1", "extra-2"), func() time.Time { return time.Date(2026, 8, 22, 8, 0, 0, 0, time.UTC) })
	return store, blobs, tasks, svc
}

func sequenceIDs(ids ...string) func() string {
	n := 0
	return func() string {
		id := ids[n%len(ids)]
		n++
		return id
	}
}

func TestImportCreatesSkillPluginAndAttachesDefaultPlacement(t *testing.T) {
	store, blobs, tasks, svc := importFixtures(t)
	detail, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", Icon: "icons/s.png"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks.consumed) != 1 || tasks.consumed[0] != "task-1|user-1|space-a|" {
		t.Fatalf("consumed = %#v", tasks.consumed)
	}
	if len(tasks.released) != 0 {
		t.Fatalf("released = %#v", tasks.released)
	}
	created := store.create
	if created == nil || created.Type != model.PluginTypeSkill || created.Name != "My Skill" || created.Icon != "icons/s.png" {
		t.Fatalf("created = %#v", created)
	}
	if string(created.Tags) != `["deploy"]` {
		t.Fatalf("tags = %s", created.Tags)
	}
	// Create stamps the resolved package version (the parse task's 2.0.0), NOT the
	// "1.0.0" default — the row's current_version must match the version baked into
	// the shipped SKILL.md.
	if created.CurrentVersion == nil || *created.CurrentVersion != "2.0.0" {
		t.Fatalf("create current_version = %v, want the package version 2.0.0", created.CurrentVersion)
	}
	pkg := string(created.Package)
	if !strings.Contains(pkg, `"SKILL.md"`) || !strings.Contains(pkg, "# Uploaded Skill") {
		t.Fatalf("package missing inline SKILL.md: %s", pkg)
	}
	// Each source file becomes its own attachment: text inline, binary storage.
	if !strings.Contains(pkg, `"scripts/run.sh"`) || !strings.Contains(pkg, "echo ok") {
		t.Fatalf("package missing inline text file: %s", pkg)
	}
	if !strings.Contains(pkg, `"assets/logo.bin"`) || !strings.Contains(pkg, `"content_type":"storage"`) {
		t.Fatalf("package missing binary storage attachment: %s", pkg)
	}
	if strings.Contains(pkg, "skill/ref.json") || strings.Contains(pkg, "skill/package.zip") {
		t.Fatalf("legacy ref.json/package.zip must be gone: %s", pkg)
	}
	// The create attaches the default placement itself (no separate publish): the
	// import carries no category, so the placement carries a nil category.
	if len(store.createPlace) != 1 {
		t.Fatalf("placements = %#v, want exactly one default placement", store.createPlace)
	}
	pl := store.createPlace[0]
	if pl.PlacementCode != "default" || !pl.Visible || pl.CategoryID != nil {
		t.Fatalf("placement = %#v, want default+visible with nil category", pl)
	}
	uploaded := 0
	for key := range blobs.objects {
		if strings.HasPrefix(key, "plugins/space-a/attachments/") {
			uploaded++
		}
	}
	// Only the binary spills to object storage; the two text files stay inline.
	if uploaded != 1 {
		t.Fatalf("uploaded objects = %#v", blobs.objects)
	}
	// Q2: the reserved ID is the persisted ID. It is baked into the SKILL.md
	// frontmatter and the spilled object namespace, so all three must agree — no
	// second id minted at Create.
	if detail.Plugin.ID != "plugin-new" || created.ID != "plugin-new" {
		t.Fatalf("persisted id = %q / row id = %q, want reserved plugin-new", detail.Plugin.ID, created.ID)
	}
	if !strings.Contains(pkg, "id: plugin-new") {
		t.Fatalf("SKILL.md frontmatter id does not match persisted id: %s", pkg)
	}
	namespaced := false
	for key := range blobs.objects {
		if strings.HasPrefix(key, "plugins/space-a/attachments/skill-plugin-new-") {
			namespaced = true
		}
	}
	if !namespaced {
		t.Fatalf("spilled object not namespaced under persisted id: %#v", blobs.objects)
	}
}

// TestImportReuploadPreservesIconWhenOmitted is the B2 regression: a
// package-only re-upload omits the icon, and the full-replace import must keep
// the existing row's icon rather than clearing it.
func TestImportReuploadPreservesIconWhenOmitted(t *testing.T) {
	store, _, _, svc := importFixtures(t)
	space := "space-a"
	// Private: a LISTED plugin can no longer be reuploaded at all (that is an
	// unreviewed change to what the org reads — see TestReimportOfAListedPluginIsRefused).
	existing := &model.Plugin{ID: "skill-1", Name: "Existing", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilityPrivate, Icon: "icons/kept.png", Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{"attachments":[]}`)}
	store.plugins["skill-1"] = existing

	// Re-upload to a fresh version with NO icon in the request.
	if _, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", PluginID: "skill-1", Version: "3.0.0"}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if store.update == nil || store.update.Icon != "icons/kept.png" {
		t.Fatalf("icon not preserved on package-only re-upload: %#v", store.update)
	}
}

// TestImportReuploadRejectsEmbeddedChild is the tenant-side embedded-guard
// regression (import.go): re-importing (a standalone skill upload bound to an
// existing plugin_id) an is_embedded=1 row — a bundled skill / squad member owned
// by its container graph — returns ErrNotFound even for the owner, so it can only
// be swapped through a container reupload. The parse task is never consumed (the
// guard fires before mutation), so the upload stays retryable.
// TestImportReuploadKeepsStoredVersionWhenOmitted is the version-reset
// regression: a package-only tenant reupload that sends no version must keep the
// row's stored current_version label, not reset it to the package version or the
// "1.0.0" default. (versionSubmitted must reach Service.update; it was previously
// defeated by an unconditional req.Version override.)
func TestImportReuploadKeepsStoredVersionWhenOmitted(t *testing.T) {
	store, _, _, svc := importFixtures(t)
	space := "space-a"
	ver := "2.4.0"
	existing := &model.Plugin{ID: "skill-1", Name: "Existing", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilityPrivate, CurrentVersion: &ver, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{"attachments":[]}`)}
	store.plugins["skill-1"] = existing

	// No Version in the request — the stored 2.4.0 label must survive.
	if _, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", PluginID: "skill-1"}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if store.update == nil || store.update.CurrentVersion == nil || *store.update.CurrentVersion != ver {
		t.Fatalf("stored version label not preserved on omitted-version reupload: %#v", store.update)
	}
}

func TestImportReuploadRejectsEmbeddedChild(t *testing.T) {
	store, _, tasks, svc := importFixtures(t)
	space := "space-a"
	store.plugins["skill-emb"] = &model.Plugin{ID: "skill-emb", Name: "Bundled", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilitySpace, IsEmbedded: true, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{"attachments":[]}`)}

	_, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", PluginID: "skill-emb", Version: "3.0.0"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for an embedded child", err)
	}
	if store.update != nil {
		t.Fatalf("an embedded-child re-import must not mutate the row: %#v", store.update)
	}
	if len(tasks.consumed) != 0 {
		t.Fatalf("parse task consumed on the embedded-child guard: %#v", tasks.consumed)
	}
}

func TestImportRejectsForeignUnfinishedOrReboundTasks(t *testing.T) {
	for _, mutate := range []func(*skillrepo.ParseTaskRow){
		func(task *skillrepo.ParseTaskRow) { task.OwnerID = "attacker" },
		func(task *skillrepo.ParseTaskRow) { task.SpaceID = "other-space" },
		func(task *skillrepo.ParseTaskRow) { task.Status = "processing" },
		func(task *skillrepo.ParseTaskRow) { task.SkillID = "legacy-skill" },
	} {
		_, _, tasks, svc := importFixtures(t)
		mutate(tasks.task)
		if _, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1"}); !errors.Is(err, ErrInvalidParseTask) {
			t.Fatalf("err = %v", err)
		}
		if len(tasks.consumed) != 0 {
			t.Fatalf("foreign task was consumed: %#v", tasks.consumed)
		}
	}
}

func TestImportRejectsPublicVisibility(t *testing.T) {
	_, _, _, svc := importFixtures(t)
	if _, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", Visibility: model.PluginVisibilityPublic}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v", err)
	}
}

func TestImportReleasesTaskAndDeletesObjectsWhenCreateFails(t *testing.T) {
	store, blobs, tasks, svc := importFixtures(t)
	store.err = errors.New("db down")
	if _, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1"}); err == nil {
		t.Fatal("expected error")
	}
	if len(tasks.released) != 1 || tasks.released[0] != "task-1" {
		t.Fatalf("released = %#v", tasks.released)
	}
	deleted := 0
	for _, key := range blobs.deletes {
		if strings.HasPrefix(key, "plugins/space-a/attachments/") {
			deleted++
		}
	}
	// The one spilled binary object is cleaned up when the create fails.
	if deleted != 1 {
		t.Fatalf("deletes = %#v", blobs.deletes)
	}
}

func TestSkillMarkdownTrustsOnlyOwnSpaceRefObjects(t *testing.T) {
	space := "space-a"
	inlinePkg := packageWith(rawAtt("SKILL.md", "# inline doc"))
	// Own-Space ref object is trusted and served over the inline stub.
	ownPkg := packageWith(
		rawAtt("SKILL.md", "# own stub"),
		rawAtt("skill/ref.json", `{"object_key":"plugins/space-a/attachments/own.md","zip_object_key":"plugins/space-a/attachments/own.zip","file_name":"pack.zip"}`),
	)
	// Legacy-root ref object is NOT trusted on the caller path (served only after
	// expand-skills rewrites it into an own-Space tree); the read must fall back
	// to the inline SKILL.md, never fetch the shared legacy-root object.
	legacyPkg := packageWith(
		rawAtt("SKILL.md", "# legacy stub"),
		rawAtt("skill/ref.json", `{"object_key":"skills/legacy/SKILL.md","zip_object_key":"skills/legacy/skill.zip","file_name":"pack.zip"}`),
	)
	store := &fakeStore{plugins: map[string]*model.Plugin{
		"inline-1": {ID: "inline-1", Name: "Inline", Type: model.PluginTypeSkill, SpaceID: &space, Package: inlinePkg},
		"own-1":    {ID: "own-1", Name: "Own", Type: model.PluginTypeSkill, SpaceID: &space, Package: ownPkg},
		"legacy-1": {ID: "legacy-1", Name: "Legacy", Type: model.PluginTypeSkill, SpaceID: &space, Package: legacyPkg},
	}}
	blobs := &importStorage{objects: map[string][]byte{
		"plugins/space-a/attachments/own.md":  []byte("# own doc"),
		"plugins/space-a/attachments/own.zip": []byte("own-zip"),
		"skills/legacy/SKILL.md":              []byte("# legacy doc"),
		"skills/legacy/skill.zip":             []byte("zip-bytes"),
	}}
	svc := New(store, blobs)

	if content, err := svc.SkillMarkdown(context.Background(), testCaller, "inline-1"); err != nil || content != "# inline doc" {
		t.Fatalf("inline = %q err=%v", content, err)
	}
	// Own-Space ref object wins over the inline stub.
	if content, err := svc.SkillMarkdown(context.Background(), testCaller, "own-1"); err != nil || content != "# own doc" {
		t.Fatalf("own = %q err=%v", content, err)
	}
	// Legacy-root object is never fetched — the inline stub is served instead.
	if content, err := svc.SkillMarkdown(context.Background(), testCaller, "legacy-1"); err != nil || content != "# legacy stub" {
		t.Fatalf("legacy must fall back to inline, got %q err=%v", content, err)
	}

	// Own-Space zip streams; the legacy-root zip must NOT be streamed.
	dl, err := svc.OpenSkillPackage(context.Background(), testCaller, "own-1")
	if err != nil {
		t.Fatal(err)
	}
	var ownZip bytes.Buffer
	if err := dl.Write(&ownZip); err != nil || ownZip.String() != "own-zip" {
		t.Fatalf("own zip = %q err=%v", ownZip.String(), err)
	}
	legacyDL, err := svc.OpenSkillPackage(context.Background(), testCaller, "legacy-1")
	if err != nil {
		t.Fatal(err)
	}
	var legacyZip bytes.Buffer
	if err := legacyDL.Write(&legacyZip); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacyZip.String(), "zip-bytes") {
		t.Fatalf("legacy-root zip must not be streamed; got %q", legacyZip.String())
	}
}

func TestOpenSkillPackageRejectsUnsafeLegacyKeys(t *testing.T) {
	space := "space-a"
	pkg := packageWith(rawAtt("skill/ref.json", `{"zip_object_key":"../secrets/root.zip"}`))
	store := &fakeStore{plugins: map[string]*model.Plugin{"bad-1": {ID: "bad-1", Name: "Bad", Type: model.PluginTypeSkill, SpaceID: &space, Package: pkg}}}
	svc := New(store, &importStorage{objects: map[string][]byte{}})
	if _, err := svc.OpenSkillPackage(context.Background(), testCaller, "bad-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestSkillArtifactsRejectForgedCrossSpacePointers(t *testing.T) {
	space := "space-a"
	// A caller-writable ref.json pointing into ANOTHER Space's managed prefix
	// must never be fetched, even though the key shape itself is clean.
	forged := packageWith(rawAtt("skill/ref.json", `{"object_key":"plugins/space-b/attachments/loot.md","zip_object_key":"plugins/space-b/attachments/loot.zip"}`))
	store := &fakeStore{plugins: map[string]*model.Plugin{
		"forged-1": {ID: "forged-1", Name: "Forged", Type: model.PluginTypeSkill, SpaceID: &space, Package: forged},
	}}
	blobs := &importStorage{objects: map[string][]byte{
		"plugins/space-b/attachments/loot.md":  []byte("secret"),
		"plugins/space-b/attachments/loot.zip": []byte("secret-zip"),
	}}
	svc := New(store, blobs)
	if _, err := svc.SkillMarkdown(context.Background(), testCaller, "forged-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("skill_md err = %v", err)
	}
	if _, err := svc.OpenSkillPackage(context.Background(), testCaller, "forged-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("download err = %v", err)
	}
}

func TestSkillArtifactsServeOwnSpaceManagedPointers(t *testing.T) {
	space := "space-a"
	pkg := packageWith(rawAtt("skill/ref.json", `{"file_name":"pack.zip","object_key":"plugins/space-a/attachments/doc.md","zip_object_key":"plugins/space-a/attachments/pack.zip"}`))
	store := &fakeStore{plugins: map[string]*model.Plugin{
		"own-1": {ID: "own-1", Name: "Own", Type: model.PluginTypeSkill, SpaceID: &space, Package: pkg},
	}}
	blobs := &importStorage{objects: map[string][]byte{
		"plugins/space-a/attachments/doc.md":   []byte("# own doc"),
		"plugins/space-a/attachments/pack.zip": []byte("own-zip"),
	}}
	svc := New(store, blobs)
	content, err := svc.SkillMarkdown(context.Background(), testCaller, "own-1")
	if err != nil || content != "# own doc" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	download, err := svc.OpenSkillPackage(context.Background(), testCaller, "own-1")
	if err != nil {
		t.Fatal(err)
	}
	var ownZip bytes.Buffer
	if err := download.Write(&ownZip); err != nil {
		t.Fatal(err)
	}
	if ownZip.String() != "own-zip" {
		t.Fatalf("streamed own zip = %q", ownZip.String())
	}
}

func TestResolveIconOnlyPresignsIconNamespaces(t *testing.T) {
	svc := New(&fakeStore{}, &importStorage{objects: map[string][]byte{}})
	ctx := context.Background()
	if got := svc.resolveIcon(ctx, "icons/abc/logo.png"); got != "https://cdn.invalid/icons/abc/logo.png" {
		t.Fatalf("icon namespace not presigned: %q", got)
	}
	// A caller-chosen key outside the icon namespaces must never yield a
	// signed URL for an unrelated object.
	if got := svc.resolveIcon(ctx, "plugins/space-b/attachments/loot.zip"); got != "plugins/space-b/attachments/loot.zip" {
		t.Fatalf("non-icon key was presigned: %q", got)
	}
	if got := svc.resolveIcon(ctx, "🐙"); got != "🐙" {
		t.Fatalf("glyph mangled: %q", got)
	}
}

// TestImportUpdateFailureDeletesNewObjects is the update-path rollback
// regression: when the repo `update` call fails, the freshly-uploaded package
// objects are cleaned up (no orphaned blobs) and the parse task is released so
// the upload stays retryable. The old publish-failure path no longer exists — a
// re-import is a single transactional save revision, so any failure rolls the
// new objects back rather than leaving them committed.
func TestImportUpdateFailureDeletesNewObjects(t *testing.T) {
	store, blobs, tasks, svc := importFixtures(t)
	space := "space-a"
	store.plugins["existing-1"] = &model.Plugin{ID: "existing-1", Name: "Old", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{"attachments":[]}`)}
	store.updateErr = errors.New("update failed")

	_, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", PluginID: "existing-1", Version: "9.9.9"})
	if err == nil {
		t.Fatal("expected update failure")
	}
	// The newly-uploaded binary spill is rolled back with the failed update.
	deleted := 0
	for _, key := range blobs.deletes {
		if strings.HasPrefix(key, "plugins/space-a/attachments/") {
			deleted++
		}
	}
	if deleted != 1 {
		t.Fatalf("deletes = %#v, want the one spilled object cleaned up", blobs.deletes)
	}
	if len(tasks.released) != 1 {
		t.Fatalf("task released = %#v", tasks.released)
	}
}

func TestSkillMarkdownRefObjectWinsOverInlineStub(t *testing.T) {
	space := "space-a"
	// Snapshot layout: inline SKILL.md is a stub; the authoritative document
	// lives behind an OWN-SPACE ref pointer and must win. (Legacy-root pointers
	// are no longer trusted on the caller path — see the own-Space rule.)
	pkg := packageWith(
		rawAtt("SKILL.md", "# stub"),
		rawAtt("skill/ref.json", `{"object_key":"plugins/space-a/attachments/full.md"}`),
	)
	store := &fakeStore{plugins: map[string]*model.Plugin{
		"snap-1": {ID: "snap-1", Name: "Snap", Type: model.PluginTypeSkill, SpaceID: &space, Package: pkg},
	}}
	blobs := &importStorage{objects: map[string][]byte{
		"plugins/space-a/attachments/full.md": []byte("# full doc"),
	}}
	content, err := New(store, blobs).SkillMarkdown(context.Background(), testCaller, "snap-1")
	if err != nil || content != "# full doc" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	// A trusted pointer whose object is missing falls back to the inline text
	// instead of failing the read.
	blobs.objects = map[string][]byte{}
	content, err = New(store, blobs).SkillMarkdown(context.Background(), testCaller, "snap-1")
	if err != nil || content != "# stub" {
		t.Fatalf("fallback content=%q err=%v", content, err)
	}
}

// A tenant upload lands as a DRAFT and keeps the visibility it declared. The old
// behaviour clamped the value to private, which under the current model would
// throw away the author's stated intent — the thing Publish reads to decide
// whether the upload needs org review.
func TestImportLandsDraftAndKeepsTheDeclaredVisibility(t *testing.T) {
	for _, tc := range []struct {
		asked model.PluginVisibility
		want  model.PluginVisibility
	}{
		{"", model.PluginVisibilityPrivate}, // unset defaults to the narrowest intent
		{model.PluginVisibilitySpace, model.PluginVisibilitySpace},
		{model.PluginVisibilityPrivate, model.PluginVisibilityPrivate},
	} {
		store, _, _, svc := importFixtures(t)
		if _, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", Visibility: tc.asked}); err != nil {
			t.Fatalf("visibility=%q import err = %v", tc.asked, err)
		}
		if store.create == nil {
			t.Fatalf("visibility=%q: nothing persisted", tc.asked)
		}
		if store.create.Visibility != tc.want {
			t.Errorf("import asked %q, PERSISTED visibility %q, want %q", tc.asked, store.create.Visibility, tc.want)
		}
		if store.create.ListingState != model.PluginListingStateDraft {
			t.Errorf("import asked %q, PERSISTED listing_state %q; a fresh upload must not be listed", tc.asked, store.create.ListingState)
		}
	}
}

// A re-import must not delist an already-approved plugin (nor promote a private
// one). It therefore replaces LIVE content without re-review — deliberate, and
// recorded in the divergence note.
func TestReimportPreservesTheExistingVisibility(t *testing.T) {
	// Only `private` is exercised here: a LISTED plugin cannot be reuploaded at
	// all any more (TestReimportOfAListedPluginIsRefused), so "preserves space" is
	// no longer a reachable outcome.
	for _, existing := range []model.PluginVisibility{model.PluginVisibilityPrivate} {
		store, _, _, svc := importFixtures(t)
		space := "space-a"
		store.plugins["plugin-1"] = &model.Plugin{
			ID: "plugin-1", Name: "My Skill", Type: model.PluginTypeSkill,
			OwnerUID: "user-1", SpaceID: &space, Visibility: existing,
			Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{"plugin_name":"My Skill","name":"my-skill","description":"d"}`),
			Package: json.RawMessage(`{"attachments":[]}`),
		}
		// A client asking to widen the plugin on reupload must not get it.
		if _, err := svc.Import(context.Background(), testCaller, ImportParams{
			ParseTaskID: "task-1", PluginID: "plugin-1", Visibility: model.PluginVisibilitySpace,
		}); err != nil {
			t.Fatalf("existing=%q reupload err = %v", existing, err)
		}
		if store.update == nil {
			t.Fatalf("existing=%q: nothing persisted", existing)
		}
		if store.update.Visibility != existing {
			t.Fatalf("existing=%q, PERSISTED %q; a reupload must neither promote nor delist", existing, store.update.Visibility)
		}
	}
}

// resolveImportFields is the import path's own visibility decision, tested
// directly because Service.createWithID independently validates it on a fresh
// create: without this the import-side default could be deleted and the
// end-to-end import tests would still pass on the create path alone.
func TestResolveImportFieldsDefaultsToPrivateAndPreservesIntent(t *testing.T) {
	task := &skillrepo.ParseTaskRow{ID: "task-1", ResultName: "My Skill", ResultVersion: "2.0.0"}
	space := "space-a"
	listed := &model.Plugin{
		ID: "plugin-1", Name: "My Skill", Visibility: model.PluginVisibilitySpace, SpaceID: &space,
		ListingState: model.PluginListingStatePublished,
		Manifest:     json.RawMessage(`{"plugin_name":"My Skill","name":"my-skill","description":"d"}`),
	}
	tests := []struct {
		name        string
		params      ImportParams
		systemAdmin bool
		old         *model.Plugin
		want        model.PluginVisibility
	}{
		{name: "fresh tenant import defaults private", want: model.PluginVisibilityPrivate},
		{name: "fresh tenant import asking for space keeps space",
			params: ImportParams{Visibility: model.PluginVisibilitySpace}, want: model.PluginVisibilitySpace},
		{name: "reupload of a listed plugin keeps space",
			params: ImportParams{Visibility: model.PluginVisibilityPrivate}, old: listed, want: model.PluginVisibilitySpace},
		{name: "system admin import is untouched",
			params: ImportParams{Visibility: model.PluginVisibilitySystem}, systemAdmin: true, want: model.PluginVisibilitySystem},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := resolveImportFields(tt.params, task, tt.systemAdmin, tt.old)
			if err != nil {
				t.Fatalf("resolveImportFields: %v", err)
			}
			if f.visibility != tt.want {
				t.Fatalf("visibility = %q, want %q", f.visibility, tt.want)
			}
		})
	}
	// An explicitly invalid value must still be a 400, not a silent downgrade.
	if _, err := resolveImportFields(ImportParams{Visibility: model.PluginVisibilityPublic}, task, false, nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("public visibility err = %v, want ErrInvalidRequest", err)
	}
}

// The re-import path goes through Service.update, so the listed-plugin lock
// covers it too: replacing a listed skill's package is an unreviewed change to
// what the whole Space is reading.
func TestReimportOfAListedPluginIsRefused(t *testing.T) {
	store, _, tasks, svc := importFixtures(t)
	space := "space-a"
	store.plugins["plugin-1"] = &model.Plugin{
		ID: "plugin-1", Name: "My Skill", Type: model.PluginTypeSkill,
		OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilitySpace,
		ListingState: model.PluginListingStatePublished,
		Tags:         json.RawMessage(`[]`), Manifest: json.RawMessage(`{"plugin_name":"My Skill","name":"my-skill","description":"d"}`),
		Package: json.RawMessage(`{"attachments":[]}`),
	}
	_, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", PluginID: "plugin-1"})
	if !errors.Is(err, ErrListedRequiresReview) {
		t.Fatalf("err = %v, want ErrListedRequiresReview", err)
	}
	if store.update != nil {
		t.Fatal("an unreviewed reupload of a listed plugin was PERSISTED")
	}
	// The parse task is released so the author can retry through the review flow.
	if len(tasks.released) != 1 {
		t.Fatalf("released = %#v; a refused reupload must not consume the upload", tasks.released)
	}
}
