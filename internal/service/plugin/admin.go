// Admin plugin operations for the marketplace-admin surface (/api/v1/admin/plugins).
// These run behind the admin role gate and operate cross-Space with no ownership
// check (repo Scope.Admin). System connectors follow the MCP convention
// (visibility=system, NULL Space); admin skills/experts follow the skill-admin
// convention (visibility=system, empty global Space). Callers reach these only
// through the admin-gated routes, so the route gate is the authorization; the
// service does not re-derive a Space from the caller.

package plugin

import (
	"context"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// adminGlobalSpace is the Space admin skills live in — empty, matching the
// legacy skill-admin GlobalTagSpaceID convention (global, space-less).
const adminGlobalSpace = ""

func adminScope(caller Caller) pluginrepo.Scope {
	return pluginrepo.Scope{CallerUID: caller.UID, Admin: true}
}

// validAdminListSort accepts the empty default plus the fixed sort whitelist the
// repository's listOrder recognizes, excluding "placement" (which needs a
// placement code the admin list never supplies).
func validAdminListSort(sort string) bool {
	switch sort {
	case "", "newest", "oldest", "updated", "name", "views", "installs", "downloads", "comprehensive":
		return true
	default:
		return false
	}
}

// AdminList lists plugins of one type across all Spaces, optionally narrowed to
// a visibility class (e.g. system connectors, system skills).
func (s *Service) AdminList(ctx context.Context, caller Caller, typ model.PluginType, visibility model.PluginVisibility, p ListParams) ([]model.Plugin, int64, error) {
	if !validPluginType(typ) {
		return nil, 0, ErrInvalidRequest
	}
	// Optional filters, but when supplied they must be known values — an unknown
	// visibility must not silently return an empty page, nor an unknown sort
	// silently fall back to the default (Q12').
	if visibility != "" && !validVisibility(visibility, true) {
		return nil, 0, ErrInvalidRequest
	}
	if !validAdminListSort(p.Sort) {
		return nil, 0, ErrInvalidRequest
	}
	if p.Limit < 0 || p.Limit > maxListLimit || p.Offset < 0 {
		return nil, 0, ErrInvalidRequest
	}
	tags, err := normalizeListTags(p.Tags)
	if err != nil {
		return nil, 0, err
	}
	items, total, err := s.repo.List(ctx, adminScope(caller), pluginrepo.ListFilter{
		Type: typ, Visibility: visibility, AllSpaces: true,
		CategoryID: p.CategoryID, Tags: tags, Keyword: p.Keyword, Sort: p.Sort, Limit: p.Limit, Offset: p.Offset,
	})
	if err != nil {
		return nil, 0, mapStoreError(err)
	}
	if err := s.fillMemberCounts(ctx, items); err != nil {
		return nil, 0, mapStoreError(err)
	}
	for i := range items {
		items[i].IconURL = s.resolveIcon(ctx, items[i].Icon)
	}
	return items, total, nil
}

// AdminDetail returns any plugin's full detail by id, ignoring Space scope.
func (s *Service) AdminDetail(ctx context.Context, caller Caller, pluginID string, includeRelations bool) (*Detail, error) {
	storageID, err := parseStorageID(pluginID)
	if err != nil {
		return nil, err
	}
	p, rels, err := s.repo.GetWithRelations(ctx, adminScope(caller), storageID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	p.IconURL = s.resolveIcon(ctx, p.Icon)
	if !includeRelations {
		rels = []model.PluginRelation{}
	} else {
		for i := range rels {
			rels[i].SourcePluginType = p.Type
		}
	}
	return &Detail{Plugin: p, Relations: rels}, nil
}

// conventionVisibility is the visibility an admin CREATE stamps: every plugin
// type an admin publishes is platform-global, so all of them get `system`
// (the unified "全平台可见" value). Update never forces a visibility — it
// preserves the row's existing one (see AdminUpdate) so a metadata edit cannot
// publish a tenant-private plugin.
func conventionVisibility(typ model.PluginType) (model.PluginVisibility, bool) {
	switch typ {
	case model.PluginTypeConnector, model.PluginTypeSkill, model.PluginTypeExpert, model.PluginTypeExpertTeam:
		return model.PluginVisibilitySystem, true
	default:
		return "", false
	}
}

// adminEffectiveWrite fixes the caller identity and Space and stamps the given
// visibility so buildWrite mints the admin conventions: system connectors (NULL
// Space) and system skills (empty global Space) on create; the row's preserved
// visibility on update. effectiveSpace is the Space storage-attachment keys are
// namespaced under and must be the ROW's real Space — for an AdminUpdate of a
// tenant-owned row that is old.SpaceID, not the empty global Space, otherwise
// CanonicalizeDocuments rejects every storage attachment (safeObjectSegment
// rejects the empty Space) and admins cannot edit any skill whose package spilled
// a file to object storage (P1-1). Returns the built plugin + relations.
func (s *Service) adminEffectiveWrite(ctx context.Context, caller Caller, pluginID string, req WriteRequest, visibility model.PluginVisibility, effectiveSpace string) (*model.Plugin, []model.PluginRelation, error) {
	if !conventionType(req.Type) {
		return nil, nil, ErrInvalidRequest
	}
	eff := caller
	eff.IsSystemAdmin = true // admins may mint/preserve system visibility
	eff.SpaceID = effectiveSpace
	// A preserved legacy `public` visibility is normalized to the unified `system`
	// global value so the write revalidates and the row stops carrying the retired
	// value (validVisibility rejects `public`).
	req.Visibility = model.NormalizeLegacyVisibility(visibility)
	p, rels, err := s.buildWrite(ctx, eff, pluginID, req, s.now(), true)
	if err != nil {
		return nil, nil, err
	}
	// System rows live outside the Space model (space_id=NULL); system admin
	// rows stay in the empty global Space.
	if req.Type == model.PluginTypeConnector {
		p.SpaceID = nil
	}
	// The marketplace-admin surface has no draft or review step — an admin create
	// IS the publish, which is why it also auto-attaches a visible default
	// placement. buildWrite mints the tenant default (draft), so re-stamp it here.
	// On an AdminUpdate the value is inert: the UPDATE statement never writes
	// listing_state, so an admin edit cannot delist or republish a row.
	p.ListingState = model.PluginListingStatePublished
	return p, rels, nil
}

func conventionType(typ model.PluginType) bool {
	switch typ {
	case model.PluginTypeConnector, model.PluginTypeSkill, model.PluginTypeExpert, model.PluginTypeExpertTeam:
		return true
	default:
		return false
	}
}

// defaultMarketPlacement is the visible "default"-placement row an admin create
// auto-attaches so the plugin appears in the tenant market without a publish. It
// carries no registered-category requirement — a plain visible placement (with
// the plugin's own category, which may be nil) is enough for the market list.
func defaultMarketPlacement(categoryID *string) model.PluginPlacement {
	return model.PluginPlacement{PlacementCode: "default", CategoryID: categoryID, Visible: true, SortOrder: 0}
}

// AdminCreate mints a new admin plugin (system connector or system skill).
func (s *Service) AdminCreate(ctx context.Context, caller Caller, req WriteRequest) (*Detail, error) {
	visibility, ok := conventionVisibility(req.Type)
	if !ok {
		return nil, ErrInvalidRequest
	}
	p, rels, err := s.adminEffectiveWrite(ctx, caller, "", req, visibility, adminGlobalSpace)
	if err != nil {
		return nil, err
	}
	p.ID = s.id()
	for i := range rels {
		rels[i].SourcePluginID = p.ID
	}
	audit := s.audit(caller, p.ID, "create", nil, p, s.now())
	m := mutation(*p, rels, audit)
	m.SnapshotVersion = true
	// Admin creates auto-attach a default visible placement so the plugin surfaces
	// in the tenant market immediately (the market always lists the "default"
	// placement); publish is not required. The placement copies the plugin's
	// category (may be nil — a null-category visible placement still lists).
	m.Placements = []model.PluginPlacement{defaultMarketPlacement(p.CategoryID)}
	sync, err := s.repo.Create(ctx, adminScope(caller), m)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if sync != nil && sync.NewVersionID != "" {
		p.CurrentVersionID = &sync.NewVersionID
	}
	return &Detail{Plugin: p, Relations: rels, RelationResult: relationResult(sync)}, nil
}

// applyStoredVersionRules is the admin-surface twin of the two version rules
// Service.update applies against the STORED label: the label may only move
// forward, and the row's own label is always accepted.
//
// Neither rule was here before, which predates the x.y.z tightening — but it is
// an omission, not a deliberate exemption. Service.update places the
// forward-only check ABOVE its IsSystemAdmin branch, so a super-admin editing
// through /plugins/upsert is already bound by it; and the escape hatch that
// branch does grant is enumerated there (edit an already-listed row's live
// content with no review request) and does not include moving a version
// backwards. The two admin routes were simply never visited.
//
// Why forward-only has to hold on the admin surface too: the label is not
// cosmetic. SubmitReview compares the applicant's label against the plugin's
// current_version, and publishedVersionLabels folds current_version into the set
// of labels the org has already seen. Dropping a LISTED plugin from 2.0.0 to
// 1.5.0 re-opens the entire range below 2.0.0 — the next reviewed upgrade can
// land at 1.6.0 and every installer watches the plugin go backwards, which is
// exactly what the forward-only rule exists to prevent. It does NOT corrupt
// plugin_versions: that table's `version` column is a per-plugin auto-increment
// counter, not this label, so no snapshot is shadowed or collided with.
//
// Why this does not cost the admin surface its data-repair role: the repair that
// actually comes up is a row stuck on a pre-tightening label (`v999`, `1.0`)
// being corrected to a real one, and versionNotRegressed already lets that
// through — an unparseable current label blocks nothing. What stays refused is a
// downgrade between two WELL-FORMED labels. That is a real if rare need with no
// route left, and the trade is taken deliberately: refusing fails loudly with a
// 400 naming the version field, while allowing it fails silently for every
// consumer of the plugin. A genuinely stuck well-formed label needs a DB fix.
func applyStoredVersionRules(req *WriteRequest, old *model.Plugin) error {
	if old == nil || old.CurrentVersion == nil {
		return nil
	}
	// Compared against the STORED label rather than the request's own history, and
	// only when a version was actually submitted: an omitted version keeps the
	// stored label (see the callers), so there is nothing to move.
	//
	// The ordering check is gated on the submitted label being WELL-FORMED, which
	// is the one place this reads differently from the tenant copy. The set of
	// accepted writes is identical either way — versionNotRegressed refuses a
	// malformed next, and buildWrite refuses it too unless it is the grandfathered
	// stored label, which versionNotRegressed lets through as "unchanged" — so the
	// only difference is WHICH refusal a caller sees. A malformed label is a format
	// problem, and reporting it as "version must not go backwards / use a higher
	// one" sends the caller after the wrong thing; letting it fall through to
	// buildWrite attributes it to the format gate that actually rejected it.
	if v := strings.TrimSpace(req.Version); v != "" && validVersion(v) && !versionNotRegressed(*old.CurrentVersion, v) {
		return ErrVersionRegressed
	}
	// The same grandfathering the tenant update performs. The admin UI is also
	// fetch-edit-save and echoes `version` back, so without this the tightened
	// x.y.z format 400s every save of an admin-managed row whose stored label
	// predates it (`1.0`, `v1.2.3`, `2.0.0-beta.1`) — permanently, since the label
	// can only be corrected by a save. The exemption is for an UNCHANGED label
	// only; buildWrite still refuses a malformed new one.
	req.grandfatheredVersion = *old.CurrentVersion
	return nil
}

// AdminUpdate updates any admin plugin by id, regardless of owner/Space. It
// PRESERVES the row's existing visibility, Space, and owner: an admin metadata
// edit must never force-publish a plugin (A1) — a tenant-private row stays
// private — and the owner/creator provenance is immutable.
//
// TODO: AdminUpdate does not (re)attach a default placement — a plugin that was
// never auto-placed (e.g. created before this change) stays out of the market
// until publish. Out of scope here; revisit if metadata-only edits must surface.
func (s *Service) AdminUpdate(ctx context.Context, caller Caller, pluginID string, req WriteRequest) (*Detail, error) {
	storageID, err := parseStorageID(pluginID)
	if err != nil {
		return nil, err
	}
	old, _, err := s.repo.GetWithRelations(ctx, adminScope(caller), storageID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	// An embedded child (a bundled skill / squad member) is owned by its container
	// graph and must be swapped only through a container reupload — a standalone
	// PATCH must not content-edit it out of band, so it is reported as not found.
	if old.IsEmbedded {
		return nil, ErrNotFound
	}
	if req.Type != old.Type {
		return nil, ErrInvalidRequest
	}
	// Storage-attachment keys are namespaced under the row's real Space, so
	// canonicalization must validate against it — not the empty global Space
	// (P1-1). System connectors are space-less (old.SpaceID nil → empty).
	effSpace := adminGlobalSpace
	if old.SpaceID != nil {
		effSpace = *old.SpaceID
	}
	// Fetch-edit-save: re-inject stored keys for unchanged storage attachments the
	// GET response returned keyless, so the round-trip is not rejected on write.
	req.Package = reinjectUpdateStorageKeys(req.Package, old.Package, old.AttachmentKeys)
	// Forward-only + grandfathering against the row's stored label; see the helper
	// for why the admin surface is not exempt from the first one.
	if err := applyStoredVersionRules(&req, old); err != nil {
		return nil, err
	}
	p, rels, err := s.adminEffectiveWrite(ctx, caller, storageID, req, old.Visibility, effSpace)
	if err != nil {
		return nil, err
	}
	p.CreatedAt, p.CurrentVersionID = old.CreatedAt, old.CurrentVersionID
	p.Rating = old.Rating // rating is preserved by the content-only SQL update
	// Keep the stored version label only when the admin edit omits a version; a
	// submitted version is applied (buildWrite already set it), mirroring the
	// tenant update — otherwise an admin edit that sends a new version returns 200
	// while silently not applying it.
	if strings.TrimSpace(req.Version) == "" {
		p.CurrentVersion = old.CurrentVersion
	}
	p.CreatorName, p.CreatedByType = old.CreatorName, old.CreatedByType
	p.CreatedByBotUID, p.CreatedByBotName = old.CreatedByBotUID, old.CreatedByBotName
	p.SpaceID = old.SpaceID   // preserve the row's existing Space on update
	p.OwnerUID = old.OwnerUID // owner provenance is immutable (Q7')
	// Publisher is display provenance; a metadata edit that omits it must not blank
	// a backfilled row's publisher, so fall back to the existing value.
	if strings.TrimSpace(req.Publisher) == "" {
		p.Publisher = old.Publisher
	}
	for i := range rels {
		rels[i].SourcePluginID = storageID
	}
	audit := s.audit(caller, storageID, "update", old, p, s.now())
	m := mutation(*p, rels, audit)
	m.SnapshotVersion = true
	// adminVersionGuard compared forward-only against the UNLOCKED `old` read; have
	// the repo restate it under the row lock. The admin is exempt from the listing
	// gates, not from version ordering — and this is the path that reaches an
	// already-listed row without EnforceListingGate, so leaving it unrestated is
	// what let a label raced past by an approval still land.
	m.EnforceForwardOnlyVersion = true
	sync, err := s.repo.Update(ctx, adminScope(caller), m)
	if err != nil {
		return nil, mapStoreError(err)
	}
	// The snapshot advanced current_version_id; stamp the new row id onto the
	// response so it agrees with the DB (a follow-up GET must not contradict it),
	// mirroring the tenant update path.
	if sync != nil && sync.NewVersionID != "" {
		p.CurrentVersionID = &sync.NewVersionID
	}
	return &Detail{Plugin: p, Relations: rels, RelationResult: relationResult(sync)}, nil
}

// AdminUpdateRating applies or clears the administrator rating without entering
// the content update pipeline. The repository performs the metadata write and
// audit append in one transaction, and returns the unchanged plugin projection.
func (s *Service) AdminUpdateRating(ctx context.Context, caller Caller, pluginID string, rating *int) (*model.Plugin, error) {
	storageID, err := parseStorageID(pluginID)
	if err != nil {
		return nil, err
	}
	if rating != nil && (*rating < 1 || *rating > 5) {
		return nil, ErrInvalidRequest
	}
	old, _, err := s.repo.GetWithRelations(ctx, adminScope(caller), storageID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	// Embedded children are owned by their container graph and are not standalone
	// admin mutation targets. Match the other mutators and conceal them as missing.
	if old.IsEmbedded {
		return nil, ErrNotFound
	}
	p, err := s.repo.UpdateRating(ctx, adminScope(caller), pluginrepo.RatingParams{
		PluginID: storageID, Rating: rating, OperatorID: caller.UID,
		OperatorName: caller.Name, RequestID: caller.RequestID,
	})
	if err != nil {
		return nil, mapStoreError(err)
	}
	p.IconURL = s.resolveIcon(ctx, p.Icon)
	return p, nil
}

// AdminImportContainer ingests an uploaded expert/expert_team container archive
// and stores it as the unified plugin graph (the expert/team plus its skills,
// members, and relations) under the admin conventions. It is the admin-surface
// name for ImportContainer, which already stamps the admin identity itself.
func (s *Service) AdminImportContainer(ctx context.Context, caller Caller, p ContainerImportParams) (*Detail, error) {
	return s.ImportContainer(ctx, caller, p)
}

// AdminReuploadContainer re-uploads an expert/expert_team container archive to
// rebuild an EXISTING plugin in place (preserving its plugin_id, visibility,
// Space, owner, and market placement) under the admin conventions. It is the
// admin-surface name for ReuploadContainer, which already stamps the admin
// identity itself.
func (s *Service) AdminReuploadContainer(ctx context.Context, caller Caller, pluginID string, p ContainerImportParams) (*Detail, error) {
	return s.ReuploadContainer(ctx, caller, pluginID, p)
}

// AdminDelete soft-deletes any plugin by id, regardless of owner/Space. An
// expert/expert_team top owns embedded children (an expert's bundled skills; a
// squad's member experts and their skills), so it is torn down through DeleteGraph
// — top plus every embedded descendant in one transaction — otherwise the children
// would be orphaned (live, is_embedded=1, unreachable). DeleteGraph derives the
// embedded child set under the top's lock, so a delete racing a concurrent reupload
// cannot orphan a child; a standalone catalog skill merely referenced by the top
// (is_embedded=0) is not collected and survives. Connectors and skills carry no
// embedded children and take the single-row Delete.
func (s *Service) AdminDelete(ctx context.Context, caller Caller, pluginID string) error {
	storageID, err := parseStorageID(pluginID)
	if err != nil {
		return err
	}
	old, _, err := s.repo.GetWithRelations(ctx, adminScope(caller), storageID)
	if err != nil {
		return mapStoreError(err)
	}
	audit := s.audit(caller, storageID, "delete", old, nil, s.now())
	if old.Type == model.PluginTypeExpert || old.Type == model.PluginTypeExpertTeam {
		return mapStoreError(s.repo.DeleteGraph(ctx, adminScope(caller), storageID, audit.OperatorID, audit.OperatorName, audit.RequestID, audit.Remark))
	}
	return mapStoreError(s.repo.Delete(ctx, adminScope(caller), storageID, audit.OperatorID, audit.OperatorName, audit.RequestID, audit.Remark))
}
