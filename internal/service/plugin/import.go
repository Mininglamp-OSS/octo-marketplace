// Plugin import turns a completed legacy upload-parse task into a skill
// Plugin: the rewritten package zip is expanded into a flat attachment tree
// (one attachment per file — text inlined as raw, binary/oversize files
// uploaded to the managed plugins/<space>/attachments/ prefix under
// deterministic keys and referenced as storage attachments). Everything funnels
// through Create/Update, so validation, hashing, and audit are
// identical to a direct upsert.

package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
)

// ErrInvalidParseTask covers absent, foreign, unfinished, and already-consumed
// parse tasks with one answer so import cannot probe other users' uploads.
var ErrInvalidParseTask = errors.New("invalid or consumed parse task")

// ParseTaskStore is the legacy upload-parse pipeline surface import consumes;
// *skillrepo.Repo satisfies it.
type ParseTaskStore interface {
	GetParseTask(ctx context.Context, id string) (*skillrepo.ParseTaskRow, error)
	MarkParseTaskConsumed(ctx context.Context, id, ownerID, spaceID, skillID string) error
	ReleaseConsumedParseTask(ctx context.Context, id string) error
}

// WithParseTasks wires the parse-task source (chainable at construction).
func (s *Service) WithParseTasks(p ParseTaskStore) *Service {
	s.parseTasks = p
	return s
}

type ImportParams struct {
	ParseTaskID string
	// PluginID selects an existing caller-owned skill Plugin to update; empty
	// creates a new one.
	PluginID    string
	PluginName  string
	Name        string
	Description string
	CategoryID  *string
	Tags        []string
	Visibility  model.PluginVisibility
	Icon        string
	Version     string
	Changelog   *string
}

func (s *Service) Import(ctx context.Context, caller Caller, p ImportParams) (*Detail, error) {
	if validateCaller(caller) != nil {
		return nil, ErrInvalidRequest
	}
	if s.parseTasks == nil || s.storage == nil {
		return nil, errors.New("plugin import is not configured")
	}
	task, err := s.parseTasks.GetParseTask(ctx, strings.TrimSpace(p.ParseTaskID))
	if err != nil {
		return nil, fmt.Errorf("load parse task: %w", err)
	}
	// Ownership, Space, completion, and reupload-binding checks collapse to one
	// error so a caller cannot distinguish foreign tasks from missing ones.
	if task == nil || task.OwnerID != caller.UID || task.SpaceID != caller.SpaceID || task.Status != "success" || task.SkillID != "" {
		return nil, ErrInvalidParseTask
	}

	updateID := ""
	var oldPlugin *model.Plugin
	if strings.TrimSpace(p.PluginID) != "" {
		updateID, err = parseStorageID(p.PluginID)
		if err != nil {
			return nil, err
		}
		old, _, err := s.repo.GetWithRelations(ctx, scope(caller), updateID)
		if err != nil {
			return nil, mapStoreError(err)
		}
		// A bundled skill / squad member (is_embedded=1) is owned by its container
		// graph and must be swapped only through a container reupload — a standalone
		// skill re-import must not content-edit it out of band, matching AdminUpdate.
		if old.OwnerUID != caller.UID || old.Type != model.PluginTypeSkill || old.IsEmbedded {
			return nil, ErrNotFound
		}
		oldPlugin = old
	}

	fields, err := resolveImportFields(p, task, caller.IsSystemAdmin, oldPlugin)
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
	detail, err := s.importConsumedTask(ctx, caller, task, fields, updateID, oldPlugin)
	if err != nil {
		_ = s.parseTasks.ReleaseConsumedParseTask(context.WithoutCancel(ctx), task.ID)
		return nil, err
	}
	return detail, nil
}

// importFields are the resolved (request value falling back to parse result)
// document fields of one import.
type importFields struct {
	pluginName  string
	name        string
	description string
	version     string
	// versionSubmitted records whether the caller explicitly sent a version (vs
	// the resolved fallback to the parse result or the "1.0.0" default). The
	// reupload path keeps the old current_version label only when no version was
	// submitted, mirroring the tenant/admin update paths.
	versionSubmitted bool
	tags             []string
	visibility       model.PluginVisibility
	categoryID       *string
	icon             string
	changelog        *string
}

func resolveImportFields(p ImportParams, task *skillrepo.ParseTaskRow, systemAdmin bool, old *model.Plugin) (*importFields, error) {
	f := &importFields{
		pluginName:       strings.TrimSpace(p.PluginName),
		name:             strings.TrimSpace(p.Name),
		description:      strings.TrimSpace(p.Description),
		changelog:        trimOptional(p.Changelog),
		version:          strings.TrimSpace(p.Version),
		versionSubmitted: strings.TrimSpace(p.Version) != "",
		visibility:       p.Visibility,
		categoryID:       trimOptional(p.CategoryID),
		icon:             strings.TrimSpace(p.Icon),
	}
	// A package-only reupload replaces the package/tags but must preserve the
	// row's curated market identity. The display name (plugin.Name column), the
	// machine name (manifest name), the description, and the category live on the
	// existing row, NOT the freshly-parsed package — so on a reupload prefer the
	// old row over the package's parse result whenever the client omits a field.
	// Without this an omitted name silently resets the skill's display name to
	// the package's, the description to the package's, and the category to NULL;
	// the two-step edit's follow-up metadata PATCH cannot be relied on to repair
	// it (a transient failure — or the consumed parse task blocking a retry —
	// leaves a corrupted row with no undo). The follow-up PATCH still applies any
	// real edits the operator made.
	if old != nil {
		if f.pluginName == "" {
			f.pluginName = old.Name
		}
		if f.name == "" {
			f.name = manifestName(old.Manifest)
		}
		if f.description == "" {
			f.description = manifestDescription(old.Manifest)
		}
		if f.categoryID == nil {
			f.categoryID = old.CategoryID
		}
		// Preserve the row's visibility on a package-only reupload that omits it,
		// rather than letting it default to `space` below — otherwise a private
		// skill silently widens to Space-visible on reupload. Tenant path only; the
		// admin import forces its own visibility via adminEffectiveWrite.
		if !systemAdmin && f.visibility == "" {
			f.visibility = old.Visibility
		}
	}
	if f.name == "" {
		f.name = strings.TrimSpace(task.ResultName)
	}
	if f.pluginName == "" {
		f.pluginName = f.name
	}
	if f.description == "" && task.ResultDescription != nil {
		f.description = strings.TrimSpace(*task.ResultDescription)
	}
	if f.version == "" {
		f.version = strings.TrimSpace(task.ResultVersion)
	}
	if f.version == "" {
		f.version = "1.0.0"
	}
	f.tags = p.Tags
	if f.tags == nil && len(task.ResultTags) > 0 {
		_ = json.Unmarshal(task.ResultTags, &f.tags)
	}
	if f.visibility == "" {
		// Default to the narrowest intent. This used to default to `space` and get
		// clamped to `private` afterwards, which amounted to the same thing; now that
		// the declared value survives, the default has to be the safe one directly.
		f.visibility = model.PluginVisibilityPrivate
	}
	// Match the legacy skill upload rule: uploads never publish publicly.
	if f.visibility == model.PluginVisibilityPublic {
		return nil, ErrInvalidRequest
	}
	if !validName(f.pluginName) || f.name == "" || !validVisibility(f.visibility, systemAdmin) {
		return nil, ErrInvalidRequest
	}
	// The version may come from the uploaded package's own frontmatter rather than
	// from the caller. Since the format was tightened to x.y.z, a zip declaring
	// `1.0` or `1.0.0-beta.1` — accepted for as long as those packages have
	// existed — would fail the whole upload over a field the author did not type.
	//
	// A caller-SUBMITTED label is still validated, because that one they can fix.
	// A package-derived one falls back to the default: the upload is the point, and
	// the author can set a real label when they publish.
	//
	// The one exception is the label the row ALREADY holds. A reupload form is
	// prefilled from the row (octo-web's edit modal seeds the version input from
	// current_version), so on a package-only reupload the "submitted" label is
	// routinely the row's own — and on a row minted before the tightening it no
	// longer parses. Refusing it here would contradict the exemption every other
	// save path grants (see WriteRequest.grandfatheredVersion) and would leave the
	// reupload route as the single place a legacy-labeled skill stays unsavable,
	// 400ing on a value the row already stores.
	if f.versionSubmitted {
		if !validVersion(f.version) && !isStoredVersionLabel(old, f.version) {
			return nil, &ReviewFieldError{Field: "version", Reason: "invalid"}
		}
	} else if !validVersion(f.version) {
		f.version = defaultCurrentVersion
	}
	// An upload lands as a DRAFT whatever visibility it declares (buildWrite stamps
	// listing_state), so the declared value is kept rather than clamped: an author
	// who uploads intending 仅本组织可见 says so once, here, and Publish routes it
	// through review.
	//
	// A re-import keeps whatever visibility the plugin already has, because
	// silently rewriting the intent of a row mid-edit is surprising. It does NOT
	// let the re-import through: `Service.update` refuses a PUBLISHED org-visible
	// plugin outright with ErrListedRequiresReview (409), so re-importing a listed
	// plugin never replaces live content — the author submits a review request
	// (skills via `parse_task_id`) and approval is what swaps it. Re-importing a
	// draft or delisted row still replaces it directly; nobody else can read it.
	// Tests: TestReimportOfAListedPluginIsRefused,
	// TestReimportPreservesTheExistingVisibility.
	if !systemAdmin && old != nil && old.Visibility != "" {
		f.visibility = old.Visibility
	}
	return f, nil
}

// isStoredVersionLabel reports whether v is byte-for-byte (modulo surrounding
// space) the version label the row already holds.
//
// Read from the STORED row, never from the request, so a caller cannot smuggle a
// malformed label past the format gate by asserting it is grandfathered — the
// same property WriteRequest.grandfatheredVersion relies on.
func isStoredVersionLabel(old *model.Plugin, v string) bool {
	return old != nil && old.CurrentVersion != nil &&
		strings.TrimSpace(*old.CurrentVersion) == strings.TrimSpace(v)
}

func (s *Service) importConsumedTask(ctx context.Context, caller Caller, task *skillrepo.ParseTaskRow, f *importFields, updateID string, oldPlugin *model.Plugin) (*Detail, error) {
	// A package-only re-upload omits the icon (nothing to change); the import is
	// a full replace, so fall back to the existing row's icon rather than
	// clearing it. A fresh create has no prior icon, so this only affects updates.
	if updateID != "" && f.icon == "" && oldPlugin != nil {
		f.icon = oldPlugin.Icon
	}
	req, pluginID, uploaded, err := s.buildImportedSkillWrite(ctx, caller.SpaceID, updateID, task, f, true)
	if err != nil {
		return nil, err
	}
	req.Changelog = f.changelog
	// req.Version is already set by buildImportWriteRequest to the submitted
	// version (empty when the caller omitted one) — do NOT force it to f.version
	// here: f.version always falls back to the package version or "1.0.0", which
	// would reset a reupload's stored label instead of letting Service.update keep
	// it. versionSubmitted gates this, mirroring the admin import twin.
	var detail *Detail
	if updateID == "" {
		// Persist under the reserved ID so the shipped SKILL.md frontmatter, the
		// spilled object namespace, and the row all agree on one plugin_id. The
		// create is a single transaction that snapshots the version and attaches the
		// default-scene placement itself, so no separate publish follows.
		detail, err = s.createWithID(ctx, caller, *req, pluginID)
	} else {
		// The update is a single transaction that snapshots the new version and
		// keeps the existing default placement's category in sync; a re-import is
		// just another save revision, so there is no version-string conflict to
		// pre-flight and no half-applied state to restore on failure.
		detail, err = s.update(ctx, caller, updateID, *req)
	}
	if err != nil {
		s.deleteObjects(ctx, uploaded...)
		return nil, err
	}
	return detail, nil
}

// buildImportedSkillWrite verifies the uploaded skill zip, reserves the plugin
// ID (reservedID for a reupload, freshly minted otherwise), rewrites the package
// under that ID, expands it into the flat attachment tree namespaced under
// objectSpace, and assembles the skill WriteRequest. It returns the reserved
// pluginID — baked into the shipped SKILL.md frontmatter and the spilled object
// keys, so the persisted row must carry the same one — plus the newly-uploaded
// object keys for rollback. Shared by the tenant Import and the admin skill
// import: requireSafeSpace enforces a valid managed-prefix Space up front (tenant
// callers always carry one), while the admin global-Space import passes false and
// relies on buildSkillAttachmentTree to reject only a binary that must spill.
func (s *Service) buildImportedSkillWrite(ctx context.Context, objectSpace, reservedID string, task *skillrepo.ParseTaskRow, f *importFields, requireSafeSpace bool) (*WriteRequest, string, []string, error) {
	zipData, err := s.readVerifiedUpload(ctx, task)
	if err != nil {
		return nil, "", nil, err
	}
	pluginID := reservedID
	if pluginID == "" {
		pluginID = s.id()
	}
	rewritten, err := skillsvc.RewriteZipPackage(bytes.NewReader(zipData), int64(len(zipData)), skillsvc.RewriteParams{
		Name:        f.name,
		Desc:        f.description,
		Version:     f.version,
		Tags:        f.tags,
		ID:          pluginID,
		RawMetadata: decodeMetadata(task.ResultMetadata),
	})
	if err != nil {
		return nil, "", nil, fmt.Errorf("rewrite skill package: %w", err)
	}
	if requireSafeSpace && !safeObjectSegment.MatchString(objectSpace) {
		return nil, "", nil, ErrInvalidRequest
	}
	// Expand the rewritten package into a flat attachment tree: text inline as
	// raw, binary/oversize spilled to the managed prefix under deterministic
	// keys. The rewritten SKILL.md (frontmatter injected) is the entry document.
	attachments, uploaded, _, err := s.buildSkillAttachmentTree(ctx, objectSpace, pluginID, rewritten.ZipBytes, rewritten.SkillMD, nil)
	if err != nil {
		return nil, "", nil, err
	}
	req, err := buildImportWriteRequest(f, attachments, reservedID != "")
	if err != nil {
		s.deleteObjects(ctx, uploaded...)
		return nil, "", nil, err
	}
	return req, pluginID, uploaded, nil
}

func decodeMetadata(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func (s *Service) deleteObjects(ctx context.Context, keys ...string) {
	cctx := context.WithoutCancel(ctx)
	for _, key := range keys {
		_ = s.storage.DeleteObject(cctx, key)
	}
}

// readVerifiedUpload downloads the parse task's temporary archive and verifies
// its recorded size and SHA-256 before any byte of it is trusted.
func (s *Service) readVerifiedUpload(ctx context.Context, task *skillrepo.ParseTaskRow) ([]byte, error) {
	if task.FileURL == "" || task.FileSize <= 0 || task.FileSHA256 == "" {
		return nil, ErrInvalidParseTask
	}
	if task.FileSize > s.maxArchiveBytes {
		return nil, ErrTooLarge
	}
	body, err := s.storage.GetObject(ctx, task.FileURL)
	if err != nil {
		return nil, fmt.Errorf("download uploaded archive: %w", err)
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, task.FileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read uploaded archive: %w", err)
	}
	if int64(len(data)) != task.FileSize {
		return nil, ErrInvalidParseTask
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), task.FileSHA256) {
		return nil, ErrInvalidParseTask
	}
	return data, nil
}

func buildImportWriteRequest(f *importFields, attachments []map[string]any, isUpdate bool) (*WriteRequest, error) {
	tagsJSON, err := canonicalJSONValue(nonNilTags(f.tags))
	if err != nil {
		return nil, ErrInvalidRequest
	}
	draft := map[string]any{
		"$schema":     manifestSchema,
		"plugin_name": f.pluginName,
		"plugin_type": string(model.PluginTypeSkill),
		"name":        f.name,
		"description": f.description,
		"labels":      nonNilTags(f.tags),
		"examples":    []any{},
	}
	draftRaw, err := canonicalJSONValue(draft)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	manifest, canonicalTags, err := CanonicalizeManifest(f.pluginName, model.PluginTypeSkill, tagsJSON, draftRaw)
	if err != nil {
		return nil, err
	}
	pkg, err := json.Marshal(map[string]any{"$schema": packageSchema, "attachments": attachments})
	if err != nil {
		return nil, ErrInvalidRequest
	}
	// Version selection is operation-aware: a CREATE stamps the resolved version
	// (submitted → parsed package version → "1.0.0"), so the row's current_version
	// matches the version baked into the shipped SKILL.md. An UPDATE stamps the
	// version only when the caller explicitly submitted one, leaving it empty
	// otherwise so Service.update keeps the row's existing label instead of
	// resetting a reupload to the package version or "1.0.0".
	version := f.version
	if isUpdate && !f.versionSubmitted {
		version = ""
	}
	return &WriteRequest{
		Name:       f.pluginName,
		Type:       model.PluginTypeSkill,
		CategoryID: f.categoryID,
		Tags:       canonicalTags,
		Icon:       f.icon,
		Visibility: f.visibility,
		Version:    version,
		Manifest:   manifest,
		Package:    pkg,
	}, nil
}

func nonNilTags(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}
