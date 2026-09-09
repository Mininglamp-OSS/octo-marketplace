// Admin skill import turns a completed legacy upload-parse task into a unified
// skill plugin under the admin conventions (visibility=system, empty global
// Space, a default visible market placement), rather than the legacy skill
// table. It mirrors the tenant Import's package pipeline (readVerifiedUpload →
// rewrite → flat attachment tree → WriteRequest, shared via buildImportedSkillWrite)
// but persists cross-Space (adminScope) and never publishes: create attaches the
// placement to the create mutation like AdminCreate, and reupload replaces the
// package while preserving the row's visibility/Space/owner/creator like
// AdminUpdate. Callers reach this only through the admin-gated route, so the
// route gate is the authorization.

package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
)

// AdminImport creates (no PluginID) or reuploads (PluginID set) a skill plugin
// from a completed admin upload-parse task. The category is threaded through
// as-is (a plugin_categories id); lockPluginCategory validates it against the
// unified table and type, so an invalid id still fails cleanly.
func (s *Service) AdminImport(ctx context.Context, caller Caller, p ImportParams) (*Detail, error) {
	if strings.TrimSpace(caller.UID) == "" {
		return nil, ErrInvalidRequest
	}
	if s.parseTasks == nil || s.storage == nil {
		return nil, errors.New("plugin import is not configured")
	}
	task, err := s.parseTasks.GetParseTask(ctx, strings.TrimSpace(p.ParseTaskID))
	if err != nil {
		return nil, fmt.Errorf("load parse task: %w", err)
	}
	// The admin uploaded the task themselves, so ownership is the caller UID;
	// completion and reupload-binding collapse to one error so a caller cannot
	// probe another admin's uploads. The admin surface carries no Space, so no
	// Space match is required (admin uploads live in the empty global Space).
	if task == nil || task.OwnerID != caller.UID || task.Status != "success" || task.SkillID != "" {
		return nil, ErrInvalidParseTask
	}

	// Resolve the reupload target cross-Space (adminScope), matching AdminUpdate.
	// The object namespace and canonicalization Space must be the ROW's real
	// Space (P1-1), not the empty global Space, or a spilled storage attachment
	// would be rejected.
	updateID := ""
	var oldPlugin *model.Plugin
	effSpace := adminGlobalSpace
	if strings.TrimSpace(p.PluginID) != "" {
		updateID, err = parseStorageID(p.PluginID)
		if err != nil {
			return nil, err
		}
		old, _, err := s.repo.GetWithRelations(ctx, adminScope(caller), updateID)
		if err != nil {
			return nil, mapStoreError(err)
		}
		if old.Type != model.PluginTypeSkill {
			return nil, ErrNotFound
		}
		// A bundled (embedded) skill is owned by its container graph; it may be
		// swapped only through a container reupload, never a standalone skill
		// reupload — reported as not found, matching ReuploadContainer/AdminUpdate.
		if old.IsEmbedded {
			return nil, ErrNotFound
		}
		oldPlugin = old
		if old.SpaceID != nil {
			effSpace = *old.SpaceID
		}
	}

	// Resolve import fields as system admin; the row visibility is decided by the
	// admin conventions below (system on create, preserved on reupload), never by
	// a caller-supplied visibility.
	p.Visibility = ""
	fields, err := resolveImportFields(p, task, true, oldPlugin)
	if err != nil {
		return nil, err
	}

	// Consume first: the optimistic status flip is the duplicate-import lock.
	// Every failure below releases it (best-effort) so the upload is retryable.
	if err := s.parseTasks.MarkParseTaskConsumed(ctx, task.ID, caller.UID, caller.SpaceID, ""); err != nil {
		if errors.Is(err, skillrepo.ErrParseTaskAlreadyConsumed) {
			return nil, ErrInvalidParseTask
		}
		return nil, fmt.Errorf("consume parse task: %w", err)
	}
	detail, err := s.adminImportConsumedTask(ctx, caller, task, fields, updateID, oldPlugin, effSpace)
	if err != nil {
		_ = s.parseTasks.ReleaseConsumedParseTask(context.WithoutCancel(ctx), task.ID)
		return nil, err
	}
	return detail, nil
}

func (s *Service) adminImportConsumedTask(ctx context.Context, caller Caller, task *skillrepo.ParseTaskRow, f *importFields, updateID string, oldPlugin *model.Plugin, effSpace string) (*Detail, error) {
	// A package-only reupload omits the icon; keep the existing row's icon rather
	// than clearing it on the full-replace import.
	if updateID != "" && f.icon == "" && oldPlugin != nil {
		f.icon = oldPlugin.Icon
	}
	// Admin skills live in the empty global Space, so a fresh create namespaces
	// its object tree there; a reupload uses the row's real Space (effSpace). The
	// empty global Space cannot spill a binary (safeObjectSegment rejects it) —
	// admin skills stay all-text, the same constraint as AdminCreate — so
	// requireSafeSpace is false and buildSkillAttachmentTree rejects only a
	// genuine binary spill.
	objectSpace := effSpace
	if updateID == "" {
		objectSpace = adminGlobalSpace
	}
	req, pluginID, uploaded, err := s.buildImportedSkillWrite(ctx, objectSpace, updateID, task, f, false)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if updateID == "" {
		// CREATE: mint under the admin conventions (system, empty global Space)
		// and reserve the package's baked-in ID as the row ID.
		p, rels, err := s.adminEffectiveWrite(ctx, caller, "", *req, model.PluginVisibilitySystem, adminGlobalSpace)
		if err != nil {
			s.deleteObjects(ctx, uploaded...)
			return nil, err
		}
		p.ID = pluginID
		for i := range rels {
			rels[i].SourcePluginID = p.ID
		}
		audit := s.audit(caller, p.ID, "create", nil, p, now)
		m := mutation(*p, rels, audit)
		m.SnapshotVersion = true // a save is a version, like every other write surface
		// Auto-attach a default visible placement so the admin skill surfaces in
		// the tenant market without a publish, exactly like AdminCreate.
		m.Placements = []model.PluginPlacement{defaultMarketPlacement(p.CategoryID)}
		sync, err := s.repo.Create(ctx, adminScope(caller), m)
		if err != nil {
			s.deleteObjects(ctx, uploaded...)
			return nil, mapStoreError(err)
		}
		if sync != nil && sync.NewVersionID != "" {
			p.CurrentVersionID = &sync.NewVersionID
		}
		return &Detail{Plugin: p, Relations: rels, RelationResult: relationResult(sync)}, nil
	}

	// REUPLOAD: replace the package while preserving the row's visibility, Space,
	// owner, and creation provenance — never force-publish or flip visibility.
	//
	// The same two stored-label version rules AdminUpdate applies. The
	// grandfathering half is what makes resolveImportFields' stored-label
	// exemption actually reach the store on this path: buildWrite would otherwise
	// still refuse the row's own pre-tightening label, and as a bare
	// ErrInvalidRequest ({"field":"body"}) rather than the version field error the
	// tenant path produces. The forward-only half closes the admin twin of the
	// AdminUpdate gap — reuploading at a lower label is the same regression.
	if err := applyStoredVersionRules(req, oldPlugin); err != nil {
		s.deleteObjects(ctx, uploaded...)
		return nil, err
	}
	p, rels, err := s.adminEffectiveWrite(ctx, caller, updateID, *req, oldPlugin.Visibility, effSpace)
	if err != nil {
		s.deleteObjects(ctx, uploaded...)
		return nil, err
	}
	p.CreatedAt = oldPlugin.CreatedAt
	// Seed current_version_id from the old row so a no-op reupload (deduped
	// snapshot) still returns a valid pointer; a real snapshot overrides it below.
	p.CurrentVersionID = oldPlugin.CurrentVersionID
	// Keep the stored version label only when the reupload omits a version; a
	// submitted version is applied (buildWrite set it), mirroring AdminUpdate.
	if strings.TrimSpace(req.Version) == "" {
		p.CurrentVersion = oldPlugin.CurrentVersion
	}
	p.CreatorName, p.CreatedByType = oldPlugin.CreatorName, oldPlugin.CreatedByType
	p.CreatedByBotUID, p.CreatedByBotName = oldPlugin.CreatedByBotUID, oldPlugin.CreatedByBotName
	p.SpaceID = oldPlugin.SpaceID   // preserve the row's existing Space
	p.OwnerUID = oldPlugin.OwnerUID // owner provenance is immutable
	// The import WriteRequest carries no publisher, so preserve the existing one
	// rather than blanking a backfilled row's publisher on a package-only reupload.
	if strings.TrimSpace(req.Publisher) == "" {
		p.Publisher = oldPlugin.Publisher
	}
	for i := range rels {
		rels[i].SourcePluginID = updateID
	}
	audit := s.audit(caller, updateID, "update", oldPlugin, p, now)
	m := mutation(*p, rels, audit)
	m.SnapshotVersion = true // a package-only reupload is a new version snapshot
	sync, err := s.repo.Update(ctx, adminScope(caller), m)
	if err != nil {
		s.deleteObjects(ctx, uploaded...)
		return nil, mapStoreError(err)
	}
	// The snapshot advanced current_version_id; stamp the new row id onto the
	// response so it agrees with the DB, like the other write surfaces.
	if sync != nil && sync.NewVersionID != "" {
		p.CurrentVersionID = &sync.NewVersionID
	}
	return &Detail{Plugin: p, Relations: rels, RelationResult: relationResult(sync)}, nil
}
