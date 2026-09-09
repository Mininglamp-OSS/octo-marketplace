package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// legacyVersionLabels are labels the pre-tightening pattern accepted and that
// therefore exist in stored rows and in shipped SKILL.md frontmatter. None of
// them passes validVersion today, so every surface that re-reads one has to say
// what it does with it — silently 400ing a save is the one answer that strands
// the row.
var legacyVersionLabels = []string{"1.0", "v1.2.3", "2.0.0-beta.1", "v999", "1.0.0lll"}

// TestAdminUpdateAcceptsTheRowsStoredLegacyVersionLabel is the admin half of the
// grandfathering the tenant Service.update already performs. The admin UI is
// fetch-edit-save and echoes `version` back unchanged, so without
// req.grandfatheredVersion buildWrite's format gate rejects every save of a row
// minted before the format was tightened — permanently, because the only way to
// correct the label is a save. The stored label must survive the round-trip.
func TestAdminUpdateAcceptsTheRowsStoredLegacyVersionLabel(t *testing.T) {
	for _, legacy := range legacyVersionLabels {
		t.Run(legacy, func(t *testing.T) {
			space := adminGlobalSpace
			stored := legacy
			existing := &model.Plugin{ID: "skill-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySystem, CurrentVersion: &stored, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
			f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
			req := adminSkillRequest()
			req.Version = legacy // the GET echoed straight back

			detail, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", req)
			if err != nil {
				t.Fatalf("AdminUpdate with the stored legacy label %q: %v", legacy, err)
			}
			if f.update == nil || f.update.CurrentVersion == nil || *f.update.CurrentVersion != legacy {
				t.Fatalf("persisted current_version = %v, want the untouched stored label %q", f.update, legacy)
			}
			if detail.Plugin.CurrentVersion == nil || *detail.Plugin.CurrentVersion != legacy {
				t.Fatalf("response current_version = %v, want %q", detail.Plugin.CurrentVersion, legacy)
			}
		})
	}
}

// TestAdminUpdateStillRejectsANewMalformedVersion keeps the exemption narrow:
// it covers an UNCHANGED label only. An admin who types a different malformed
// label is writing a NEW unorderable value, which is exactly what the tightened
// format exists to stop, so the write must still fail and never reach the store.
func TestAdminUpdateStillRejectsANewMalformedVersion(t *testing.T) {
	space := adminGlobalSpace
	stored := "1.0"
	existing := &model.Plugin{ID: "skill-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySystem, CurrentVersion: &stored, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
	req := adminSkillRequest()
	req.Version = "2.0" // a DIFFERENT malformed label, not the grandfathered one

	if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest for a new malformed label", err)
	}
	if f.update != nil {
		t.Fatalf("a rejected version must not reach the store: %#v", f.update)
	}
}

// TestAdminUpdateGrandfatheringIsNotCallerSupplied is the boundary check on the
// exemption: grandfatheredVersion is read from the STORED row, never from the
// request, so a caller cannot smuggle a malformed label past the format gate by
// sending one that matches nothing on disk.
func TestAdminUpdateGrandfatheringIsNotCallerSupplied(t *testing.T) {
	space := adminGlobalSpace
	stored := "3.2.1"
	existing := &model.Plugin{ID: "skill-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySystem, CurrentVersion: &stored, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
	req := adminSkillRequest()
	req.Version = "v9"

	if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest: a well-formed stored label grandfathers nothing", err)
	}
	if f.update != nil {
		t.Fatalf("a rejected version must not reach the store: %#v", f.update)
	}
}

// TestImportFallsBackWhenThePackageDeclaresALegacyVersion covers the upload
// half. The version can come from the uploaded zip's own SKILL.md frontmatter
// rather than from the caller; failing the whole upload over a field the author
// never typed — and cannot edit without repackaging — is not a usable answer, so
// a package-derived label that no longer parses falls back to the default.
func TestImportFallsBackWhenThePackageDeclaresALegacyVersion(t *testing.T) {
	for _, legacy := range legacyVersionLabels {
		t.Run(legacy, func(t *testing.T) {
			store, _, tasks, svc := importFixtures(t)
			tasks.task.ResultVersion = legacy

			// No Version in the request: the label is the package's, not the caller's.
			if _, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1"}); err != nil {
				t.Fatalf("Import of a package declaring %q: %v", legacy, err)
			}
			if store.create == nil || store.create.CurrentVersion == nil || *store.create.CurrentVersion != defaultCurrentVersion {
				t.Fatalf("current_version = %v, want the %q default", store.create, defaultCurrentVersion)
			}
			// The row's label and the label baked into the shipped SKILL.md are the
			// same string, so a later reupload does not disagree with the row.
			if pkg := string(store.create.Package); !strings.Contains(pkg, "version: "+defaultCurrentVersion) {
				t.Fatalf("shipped SKILL.md frontmatter does not carry %q: %s", defaultCurrentVersion, pkg)
			}
		})
	}
}

// TestImportRejectsACallerSubmittedLegacyVersion is the other side of that
// decision: a label the caller typed IS validated, because that one they can
// fix. It is reported as a field error so the form can mark the version input
// rather than collapsing to {"field":"body"} — writeServiceError turns a
// *ReviewFieldError into details.field/details.reason. The guard runs before the
// parse task is consumed, so the upload stays retryable.
func TestImportRejectsACallerSubmittedLegacyVersion(t *testing.T) {
	for _, legacy := range legacyVersionLabels {
		t.Run(legacy, func(t *testing.T) {
			store, _, tasks, svc := importFixtures(t)

			_, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", Version: legacy})
			var fieldErr *ReviewFieldError
			if !errors.As(err, &fieldErr) {
				t.Fatalf("err = %v, want *ReviewFieldError for a submitted %q", err, legacy)
			}
			if fieldErr.Field != "version" || fieldErr.Reason != "invalid" {
				t.Fatalf("field error = %#v, want field=version reason=invalid", fieldErr)
			}
			if store.create != nil {
				t.Fatalf("a rejected version must not create a row: %#v", store.create)
			}
			if len(tasks.consumed) != 0 {
				t.Fatalf("parse task consumed on a validation refusal, so the upload is not retryable: %#v", tasks.consumed)
			}
		})
	}
}

// TestImportUsesTheSubmittedVersionOverALegacyPackageLabel guards the fallback
// against over-reaching: the package's unusable label is replaced, but a
// well-formed label the caller did submit is still what lands on the row.
func TestImportUsesTheSubmittedVersionOverALegacyPackageLabel(t *testing.T) {
	store, _, tasks, svc := importFixtures(t)
	tasks.task.ResultVersion = "1.0"

	if _, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", Version: "3.1.4"}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if store.create == nil || store.create.CurrentVersion == nil || *store.create.CurrentVersion != "3.1.4" {
		t.Fatalf("current_version = %v, want the submitted 3.1.4 rather than the default", store.create)
	}
}

// ─── The row's own stored label on the skill REUPLOAD path ──────────────────
//
// resolveImportFields validates a caller-SUBMITTED version strictly, before
// either update path can grandfather it. That collides with how a reupload form
// is actually driven: octo-web's edit modal seeds the version input from the
// row's current_version (EditSkillModal, setVersion(skill.version)), and its
// patch bump returns the label unchanged for anything with fewer than three
// dot-parts — so a row stored at `1.0` reuploads with `1.0` in the field. The
// exemption every other save path grants has to reach this path too, on both
// surfaces, or the reupload route is the one place a legacy-labeled skill stays
// unsavable.

// TestImportReuploadAcceptsTheRowsStoredLegacyVersionLabel is the tenant half.
// Service.update would grandfather the label, but resolveImportFields refused it
// first, 400ing on a value the row already holds.
func TestImportReuploadAcceptsTheRowsStoredLegacyVersionLabel(t *testing.T) {
	for _, legacy := range legacyVersionLabels {
		t.Run(legacy, func(t *testing.T) {
			store, _, tasks, svc := importFixtures(t)
			space, stored := "space-a", legacy
			// Private/draft: a LISTED org-visible row cannot be reuploaded at all.
			store.plugins["skill-1"] = &model.Plugin{ID: "skill-1", Name: "Existing", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilityPrivate, CurrentVersion: &stored, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{"attachments":[]}`)}

			// The prefilled form echoes the row's own label straight back.
			if _, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", PluginID: "skill-1", Version: legacy}); err != nil {
				t.Fatalf("reupload echoing the stored label %q: %v", legacy, err)
			}
			if store.update == nil || store.update.CurrentVersion == nil || *store.update.CurrentVersion != legacy {
				t.Fatalf("persisted current_version = %v, want the untouched stored label %q", store.update, legacy)
			}
			if len(tasks.released) != 0 {
				t.Fatalf("parse task released, so the reupload did not land: %#v", tasks.released)
			}
		})
	}
}

// TestAdminSkillReuploadAcceptsTheRowsStoredLegacyVersionLabel is the admin
// half, and the reason the fix could not live in import.go alone:
// adminImportConsumedTask reaches buildWrite through adminEffectiveWrite, which
// never set grandfatheredVersion, so relaxing resolveImportFields on its own
// would have converted the typed version field error into a bare
// ErrInvalidRequest ({"field":"body"}) — a worse answer than the original 400.
func TestAdminSkillReuploadAcceptsTheRowsStoredLegacyVersionLabel(t *testing.T) {
	for _, legacy := range legacyVersionLabels {
		t.Run(legacy, func(t *testing.T) {
			store, _, _, svc := adminImportFixtures(t)
			tenant, stored := "tenant-space", legacy
			store.plugins["skill-9"] = &model.Plugin{ID: "skill-9", Name: "Old", Type: model.PluginTypeSkill, OwnerUID: "tenant-user", SpaceID: &tenant, Visibility: model.PluginVisibilityPrivate, CurrentVersion: &stored, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{"attachments":[]}`)}

			if _, err := svc.AdminImport(context.Background(), adminCaller, ImportParams{ParseTaskID: "task-admin", PluginID: "skill-9", Version: legacy}); err != nil {
				t.Fatalf("admin reupload echoing the stored label %q: %v", legacy, err)
			}
			if store.update == nil || store.update.CurrentVersion == nil || *store.update.CurrentVersion != legacy {
				t.Fatalf("persisted current_version = %v, want the untouched stored label %q", store.update, legacy)
			}
		})
	}
}

// TestImportReuploadStillRejectsALegacyLabelThatIsNotTheRowsOwn keeps the
// exemption at byte-equality with the STORED label rather than "malformed labels
// are fine on a reupload". A caller who types some OTHER unorderable label is
// minting a new one, which is what the tightened format exists to stop, and it
// must still come back as a version field error the form can mark.
func TestImportReuploadStillRejectsALegacyLabelThatIsNotTheRowsOwn(t *testing.T) {
	store, _, tasks, svc := importFixtures(t)
	space, stored := "space-a", "1.0"
	store.plugins["skill-1"] = &model.Plugin{ID: "skill-1", Name: "Existing", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilityPrivate, CurrentVersion: &stored, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{"attachments":[]}`)}

	_, err := svc.Import(context.Background(), testCaller, ImportParams{ParseTaskID: "task-1", PluginID: "skill-1", Version: "2.0"})
	var fieldErr *ReviewFieldError
	if !errors.As(err, &fieldErr) || fieldErr.Field != "version" {
		t.Fatalf("err = %v, want a version *ReviewFieldError: only the row's OWN label is grandfathered", err)
	}
	if store.update != nil {
		t.Fatalf("a rejected version must not reach the store: %#v", store.update)
	}
	// The guard still runs before the parse task is consumed, so the upload is
	// retryable after the caller fixes the field.
	if len(tasks.consumed) != 0 {
		t.Fatalf("parse task consumed on a validation refusal: %#v", tasks.consumed)
	}
}

// ─── Forward-only on the admin surfaces ─────────────────────────────────────
//
// Service.update applies "the label may only move forward" ABOVE its
// IsSystemAdmin branch, so a super-admin on /plugins/upsert is already bound by
// it. AdminUpdate and the admin skill reupload had no such check at all, so the
// same operator could walk a version backwards through the admin route.

// TestAdminUpdateRefusesAVersionThatMovesBackwards is the gap itself. The label
// is not cosmetic: SubmitReview compares against current_version and
// publishedVersionLabels folds it into the set the org has already seen, so
// dropping a listed plugin to a lower label re-opens the whole range beneath it
// and the next approved upgrade can land below what the organization installed.
func TestAdminUpdateRefusesAVersionThatMovesBackwards(t *testing.T) {
	space, stored := adminGlobalSpace, "2.0.0"
	existing := &model.Plugin{ID: "skill-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySystem, CurrentVersion: &stored, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
	req := adminSkillRequest()
	req.Version = "1.5.0"

	if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", req); !errors.Is(err, ErrVersionRegressed) {
		t.Fatalf("err = %v, want ErrVersionRegressed: the admin surface is not exempt from forward-only", err)
	}
	if f.update != nil {
		t.Fatalf("a regressed version must not reach the store: %#v", f.update)
	}
}

// TestAdminSkillReuploadRefusesAVersionThatMovesBackwards covers the admin
// twin — closing AdminUpdate alone would have left the same move available one
// endpoint over. The consumed parse task is released so the upload stays
// retryable at a corrected label.
func TestAdminSkillReuploadRefusesAVersionThatMovesBackwards(t *testing.T) {
	store, _, tasks, svc := adminImportFixtures(t)
	tenant, stored := "tenant-space", "2.0.0"
	store.plugins["skill-9"] = &model.Plugin{ID: "skill-9", Name: "Old", Type: model.PluginTypeSkill, OwnerUID: "tenant-user", SpaceID: &tenant, Visibility: model.PluginVisibilityPrivate, CurrentVersion: &stored, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{"attachments":[]}`)}

	_, err := svc.AdminImport(context.Background(), adminCaller, ImportParams{ParseTaskID: "task-admin", PluginID: "skill-9", Version: "1.0.0"})
	if !errors.Is(err, ErrVersionRegressed) {
		t.Fatalf("err = %v, want ErrVersionRegressed", err)
	}
	if store.update != nil {
		t.Fatalf("a regressed version must not reach the store: %#v", store.update)
	}
	if len(tasks.released) != 1 {
		t.Fatalf("released = %#v, want the task released so the upload is retryable", tasks.released)
	}
}

// TestAdminUpdateStillRepairsAnUnorderableStoredLabel is why forward-only does
// not cost the admin surface its data-repair role, and the reason the rule was
// judged affordable here at all. versionNotRegressed treats an unparseable
// CURRENT label as blocking nothing, so the repair that actually comes up — a
// row stranded on a pre-tightening label, corrected to a real one — still goes
// through. Only a downgrade between two WELL-FORMED labels stays refused.
func TestAdminUpdateStillRepairsAnUnorderableStoredLabel(t *testing.T) {
	for _, legacy := range legacyVersionLabels {
		t.Run(legacy, func(t *testing.T) {
			space, stored := adminGlobalSpace, legacy
			existing := &model.Plugin{ID: "skill-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySystem, CurrentVersion: &stored, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
			f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
			req := adminSkillRequest()
			req.Version = "1.2.0" // lower than "2.0.0-beta.1" and "v999" would sort, if they sorted

			if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", req); err != nil {
				t.Fatalf("repairing the unorderable stored label %q: %v", legacy, err)
			}
			if f.update == nil || f.update.CurrentVersion == nil || *f.update.CurrentVersion != "1.2.0" {
				t.Fatalf("persisted current_version = %v, want the repaired 1.2.0", f.update)
			}
		})
	}
}
