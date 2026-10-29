package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
)

// adminSkillZipFixture is an all-text skill zip (no binary), so it expands to an
// all-inline attachment tree that needs no managed-prefix Space — the constraint
// admin skills live under in the empty global Space.
func adminSkillZipFixture(t *testing.T) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	md, err := zw.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := md.Write([]byte("---\nname: admin-skill\nversion: 1.0.0\n---\n# Admin Skill\nBody.")); err != nil {
		t.Fatal(err)
	}
	extra, err := zw.Create("scripts/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extra.Write([]byte("echo ok")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func adminImportFixtures(t *testing.T) (*fakeStore, *importStorage, *fakeParseTasks, *Service) {
	t.Helper()
	zipBytes, sha := adminSkillZipFixture(t)
	store := &fakeStore{plugins: map[string]*model.Plugin{}}
	blobs := &importStorage{objects: map[string][]byte{"tmp/admin.zip": zipBytes}}
	// The admin upload lives in the empty global Space (GlobalTagSpaceID).
	tasks := &fakeParseTasks{task: &skillrepo.ParseTaskRow{
		ID: "task-admin", OwnerID: "admin-1", SpaceID: "", Status: "success",
		FileName: "orig.zip", FileURL: "tmp/admin.zip", FileSize: int64(len(zipBytes)), FileSHA256: sha,
		ResultName: "Admin Skill", ResultVersion: "1.0.0", ResultTags: []byte(`["deploy"]`),
	}}
	svc := New(store, blobs).WithParseTasks(tasks).WithRuntime(sequenceIDs("plugin-admin", "audit-1", "extra-1", "extra-2"), func() time.Time { return time.Date(2026, 8, 22, 8, 0, 0, 0, time.UTC) })
	return store, blobs, tasks, svc
}

// TestAdminImportCreatesPublicGlobalSkillWithPlacement locks the admin create
// conventions: a public, empty-global-Space skill plugin created under the admin
// scope with a default visible market placement, the unified category threaded
// through, and the caller-supplied visibility ignored.
func TestAdminImportCreatesPublicGlobalSkillWithPlacement(t *testing.T) {
	store, _, tasks, svc := adminImportFixtures(t)
	category := "cat-ops"
	// The request even tries to set a public/other visibility; it must be ignored
	// and the skill convention (public) applied regardless.
	detail, err := svc.AdminImport(context.Background(), adminCaller, ImportParams{
		ParseTaskID: "task-admin", Name: "Admin Skill", CategoryID: &category, Tags: []string{"deploy"}, Version: "1.0.0", Visibility: model.PluginVisibilityPrivate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks.consumed) != 1 || tasks.consumed[0] != "task-admin|admin-1||" {
		t.Fatalf("consumed = %#v", tasks.consumed)
	}
	if len(tasks.released) != 0 {
		t.Fatalf("released = %#v", tasks.released)
	}
	created := store.create
	if created == nil || created.Type != model.PluginTypeSkill {
		t.Fatalf("created = %#v", created)
	}
	if created.Visibility != model.PluginVisibilitySystem {
		t.Fatalf("visibility = %q, want system (caller visibility must not be trusted)", created.Visibility)
	}
	if created.SpaceID == nil || *created.SpaceID != adminGlobalSpace {
		t.Fatalf("space = %v, want empty global", created.SpaceID)
	}
	if created.OwnerUID != "admin-1" {
		t.Fatalf("owner = %q, want admin-1", created.OwnerUID)
	}
	if !store.createScope.Admin {
		t.Fatalf("create not under admin scope: %#v", store.createScope)
	}
	if created.CategoryID == nil || *created.CategoryID != category {
		t.Fatalf("category = %v, want %q threaded through", created.CategoryID, category)
	}
	// The create stamps a current_version label (a save is a version).
	if created.CurrentVersion == nil || *created.CurrentVersion != "1.0.0" {
		t.Fatalf("create current_version = %v, want 1.0.0", created.CurrentVersion)
	}
	// The reserved package ID is the persisted row ID.
	if created.ID != "plugin-admin" || detail.Plugin.ID != "plugin-admin" {
		t.Fatalf("id = %q / %q, want reserved plugin-admin", created.ID, detail.Plugin.ID)
	}
	if len(store.createPlace) != 1 {
		t.Fatalf("placements = %#v, want exactly one default placement", store.createPlace)
	}
	pl := store.createPlace[0]
	if pl.PlacementCode != "default" || !pl.Visible || pl.CategoryID == nil || *pl.CategoryID != category {
		t.Fatalf("placement = %#v, want default+visible carrying the category", pl)
	}
	if !strings.Contains(string(created.Package), "# Admin Skill") {
		t.Fatalf("package missing inline SKILL.md body: %s", created.Package)
	}
}

// TestAdminImportReuploadPreservesVisibilitySpaceOwner locks the admin reupload
// conventions: the existing row's visibility, Space, owner, and creator
// provenance survive a package replacement, the row is loaded/updated
// cross-Space, no publish happens, and an omitted icon is preserved.
func TestAdminImportReuploadPreservesVisibilitySpaceOwner(t *testing.T) {
	store, _, tasks, svc := adminImportFixtures(t)
	tenant := "tenant-space"
	existing := &model.Plugin{
		ID: "skill-9", Name: "Old", Type: model.PluginTypeSkill, OwnerUID: "tenant-user", SpaceID: &tenant,
		Visibility: model.PluginVisibilityPrivate, Icon: "icons/keep.png", CreatorName: "Creator", CreatedByType: "human",
		Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{"attachments":[]}`),
	}
	store.plugins["skill-9"] = existing

	detail, err := svc.AdminImport(context.Background(), adminCaller, ImportParams{ParseTaskID: "task-admin", PluginID: "skill-9", Version: "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if store.update == nil {
		t.Fatal("no update issued")
	}
	if store.update.Visibility != model.PluginVisibilityPrivate {
		t.Fatalf("visibility force-flipped to %q; tenant-private row would be published", store.update.Visibility)
	}
	if store.update.SpaceID == nil || *store.update.SpaceID != tenant {
		t.Fatalf("space not preserved: %v", store.update.SpaceID)
	}
	if store.update.OwnerUID != "tenant-user" {
		t.Fatalf("owner rewritten to %q, want tenant-user", store.update.OwnerUID)
	}
	if store.update.CreatorName != "Creator" || store.update.CreatedByType != "human" {
		t.Fatalf("creation provenance not preserved: %#v", store.update)
	}
	if store.update.Icon != "icons/keep.png" {
		t.Fatalf("icon not preserved on package-only reupload: %q", store.update.Icon)
	}
	if detail.Plugin.ID != "skill-9" {
		t.Fatalf("persisted id = %q, want skill-9", detail.Plugin.ID)
	}
	if len(tasks.consumed) != 1 || tasks.consumed[0] != "task-admin|admin-1||" {
		t.Fatalf("consumed = %#v", tasks.consumed)
	}
}

// TestAdminImportReuploadPreservesCuratedMetadata is the B1 regression: a
// package-only reupload (the client omits name/description/category — those ride
// the follow-up metadata PATCH) must NOT reset the row's curated market identity
// to the freshly-parsed package's values. The old row's display name (plugin
// Name column), description (manifest), and category win over the package's
// parse result, so a failed/retried follow-up PATCH cannot leave a corrupted row.
func TestAdminImportReuploadPreservesCuratedMetadata(t *testing.T) {
	store, _, tasks, svc := adminImportFixtures(t)
	// The uploaded package parses to DIFFERENT display/description values; without
	// the fix these would overwrite the curated row.
	pkgDesc := "Package description"
	tasks.task.ResultName = "package-machine-name"
	tasks.task.ResultDescription = &pkgDesc

	tenant := "tenant-space"
	category := "cat-old"
	existing := &model.Plugin{
		ID: "skill-9", Name: "运维技能", Type: model.PluginTypeSkill, OwnerUID: "tenant-user", SpaceID: &tenant,
		Visibility: model.PluginVisibilityPrivate, CategoryID: &category,
		Tags:     json.RawMessage(`[]`),
		Manifest: json.RawMessage(`{"name":"ops-skill-machine","description":"Curated ops description"}`),
		Package:  json.RawMessage(`{"attachments":[]}`),
	}
	store.plugins["skill-9"] = existing

	if _, err := svc.AdminImport(context.Background(), adminCaller, ImportParams{ParseTaskID: "task-admin", PluginID: "skill-9", Version: "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	if store.update == nil {
		t.Fatal("no update issued")
	}
	// Display name (plugin Name column) preserved, not reset to the package's.
	if store.update.Name != "运维技能" {
		t.Fatalf("display name reset to %q, want the curated 运维技能", store.update.Name)
	}
	// Category preserved, not NULLed.
	if store.update.CategoryID == nil || *store.update.CategoryID != category {
		t.Fatalf("category reset to %v, want preserved %q", store.update.CategoryID, category)
	}
	// Description preserved in the rebuilt manifest, not the package's.
	if !strings.Contains(string(store.update.Manifest), "Curated ops description") {
		t.Fatalf("curated description not preserved in manifest: %s", store.update.Manifest)
	}
	if strings.Contains(string(store.update.Manifest), pkgDesc) {
		t.Fatalf("package description leaked into the reuploaded row: %s", store.update.Manifest)
	}
}

// TestAdminImportRejectsForeignOrUnfinishedTasks proves the admin import only
// consumes the admin's own completed, unbound upload.
func TestAdminImportRejectsForeignOrUnfinishedTasks(t *testing.T) {
	for _, mutate := range []func(*skillrepo.ParseTaskRow){
		func(task *skillrepo.ParseTaskRow) { task.OwnerID = "someone-else" },
		func(task *skillrepo.ParseTaskRow) { task.Status = "processing" },
		func(task *skillrepo.ParseTaskRow) { task.SkillID = "legacy-skill" },
	} {
		_, _, tasks, svc := adminImportFixtures(t)
		mutate(tasks.task)
		if _, err := svc.AdminImport(context.Background(), adminCaller, ImportParams{ParseTaskID: "task-admin"}); err != ErrInvalidParseTask {
			t.Fatalf("err = %v, want ErrInvalidParseTask", err)
		}
		if len(tasks.consumed) != 0 {
			t.Fatalf("foreign task was consumed: %#v", tasks.consumed)
		}
	}
}

// TestAdminImportCreateSnapshotsAndStampsVersionID is the yujiawei-P1 regression
// on the create branch: an admin skill import must record a plugin_versions
// snapshot (a save IS a version) and return the new id, not leave the history
// empty with a nil current_version_id.
func TestAdminImportCreateSnapshotsAndStampsVersionID(t *testing.T) {
	store, _, _, svc := adminImportFixtures(t)
	detail, err := svc.AdminImport(context.Background(), adminCaller, ImportParams{ParseTaskID: "task-admin", Name: "Admin Skill", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.createSnapshot {
		t.Fatal("admin skill import (create) did not flag SnapshotVersion — no version history is recorded")
	}
	if detail.Plugin.CurrentVersionID == nil || *detail.Plugin.CurrentVersionID != "ver-snap" {
		t.Fatalf("create response current_version_id = %v, want the new ver-snap", detail.Plugin.CurrentVersionID)
	}
}

// TestAdminImportReuploadSnapshotsAndAppliesVersion is the yujiawei-P1 regression
// on the reupload branch: a package-only reupload must record a new snapshot and
// stamp its id; a submitted version becomes current_version, while an omitted one
// keeps the stored label.
func TestAdminImportReuploadSnapshotsAndAppliesVersion(t *testing.T) {
	oldVer := "1.0.0"
	mk := func() (*fakeStore, *Service) {
		store, _, _, svc := adminImportFixtures(t)
		tenant := "tenant-space"
		store.plugins["skill-9"] = &model.Plugin{
			ID: "skill-9", Name: "Old", Type: model.PluginTypeSkill, OwnerUID: "tenant-user", SpaceID: &tenant,
			Visibility: model.PluginVisibilityPrivate, CurrentVersion: &oldVer,
			Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{"attachments":[]}`),
		}
		return store, svc
	}

	// Submitted version is applied and a new snapshot is stamped.
	store, svc := mk()
	detail, err := svc.AdminImport(context.Background(), adminCaller, ImportParams{ParseTaskID: "task-admin", PluginID: "skill-9", Version: "2.5.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.updateSnapshot {
		t.Fatal("admin reupload did not flag SnapshotVersion — the previous content is unrecoverable")
	}
	if detail.Plugin.CurrentVersionID == nil || *detail.Plugin.CurrentVersionID != "ver-snap" {
		t.Fatalf("reupload response current_version_id = %v, want the new ver-snap", detail.Plugin.CurrentVersionID)
	}
	if detail.Plugin.CurrentVersion == nil || *detail.Plugin.CurrentVersion != "2.5.0" {
		t.Fatalf("submitted version not applied: %v", detail.Plugin.CurrentVersion)
	}

	// Omitted version keeps the stored label.
	store2, svc2 := mk()
	detail2, err := svc2.AdminImport(context.Background(), adminCaller, ImportParams{ParseTaskID: "task-admin", PluginID: "skill-9"})
	if err != nil {
		t.Fatal(err)
	}
	_ = store2
	if detail2.Plugin.CurrentVersion == nil || *detail2.Plugin.CurrentVersion != oldVer {
		t.Fatalf("omitted version should keep the stored label %q, got %v", oldVer, detail2.Plugin.CurrentVersion)
	}
}

func TestAdminReuploadRetainsPlatformArtifactResourceContext(t *testing.T) {
	store, blobs, tasks, svc := adminImportFixtures(t)
	const id = "system-skill"
	data := "# original platform document"
	sum := sha256.Sum256([]byte(data))
	key := "platform/plugins/" + id + "/attachments/versions/" + hex.EncodeToString(sum[:])
	ref := assertedMarkdownRef(`{"object_key":"`+key+`"}`, data)
	upload := zipWith(t, map[string][]byte{"SKILL.md": []byte("---\nname: Admin Skill\ndescription: demo\nversion: 2.0.0\n---\n# stub"), "skill/ref.json": []byte(ref)})
	h := sha256.Sum256(upload)
	blobs.objects["tmp/admin.zip"] = upload
	blobs.objects[key] = []byte(data)
	tasks.task.FileSize = int64(len(upload))
	tasks.task.FileSHA256 = hex.EncodeToString(h[:])
	store.plugins[id] = &model.Plugin{ID: id, Name: "Admin Skill", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySystem, OwnerUID: "owner", Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: packageWith(rawAtt("SKILL.md", data), rawAtt("skill/ref.json", ref))}
	detail, err := svc.AdminImport(context.Background(), adminCaller, ImportParams{ParseTaskID: "task-admin", PluginID: id, Version: "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Plugin.SpaceID != nil || detail.Plugin.Visibility != model.PluginVisibilitySystem {
		t.Fatal("platform resource assigned a Space")
	}
	got, err := svc.versionFileBytes(context.Background(), detail.Plugin, &model.PluginVersion{Package: detail.Plugin.Package, AttachmentKeys: detail.Plugin.AttachmentKeys}, "SKILL.md")
	if err != nil || string(got) != data {
		t.Fatalf("platform attachment round trip: %q %v", got, err)
	}
}
