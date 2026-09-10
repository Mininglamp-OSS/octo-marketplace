package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

var adminCaller = Caller{UID: "admin-1", Name: "Root", RequestID: "req-admin"}

func adminSkillRequest() WriteRequest {
	manifest := json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Ops Skill","plugin_type":"skill","name":"ops-skill","description":"An ops skill.","labels":[],"examples":[]}`)
	pkg := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Ops Skill"}]}`)
	return WriteRequest{Name: "Ops Skill", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilityPrivate, Tags: json.RawMessage(`[]`), Manifest: manifest, Package: pkg}
}

// TestAdminListForwardsAdminScopeAllSpacesAndVisibility locks the cross-Space
// read: the admin scope drops per-Space predicates (Admin=true), AllSpaces is
// set, and the visibility narrow is forwarded verbatim.
func TestAdminListForwardsAdminScopeAllSpacesAndVisibility(t *testing.T) {
	f := &fakeStore{}
	_, _, err := fixedService(f).AdminList(context.Background(), adminCaller, model.PluginTypeConnector, model.PluginVisibilitySystem, ListParams{})
	if err != nil {
		t.Fatalf("AdminList: %v", err)
	}
	if !f.listScope.Admin || f.listScope.CallerUID != "admin-1" {
		t.Fatalf("listScope=%#v want admin caller", f.listScope)
	}
	if !f.listFilter.AllSpaces {
		t.Fatalf("AdminList must set AllSpaces: %#v", f.listFilter)
	}
	if f.listFilter.Visibility != model.PluginVisibilitySystem || f.listFilter.Type != model.PluginTypeConnector {
		t.Fatalf("listFilter=%#v", f.listFilter)
	}
}

func TestAdminListRejectsBadType(t *testing.T) {
	if _, _, err := fixedService(&fakeStore{}).AdminList(context.Background(), adminCaller, model.PluginType("bogus"), "", ListParams{}); err != ErrInvalidRequest {
		t.Fatalf("err=%v want ErrInvalidRequest", err)
	}
}

// TestAdminCreateConnectorMintsSystemNullSpace verifies the connector
// convention: visibility=system and a NULL Space (system rows live outside the
// Space model), written under the admin scope.
func TestAdminCreateConnectorMintsSystemNullSpace(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{}}
	config := json.RawMessage(`{"env_user_supplied":[],"headers_user_supplied":[]}`)
	if _, err := fixedService(f).AdminCreate(context.Background(), adminCaller, connectorRequest(config)); err != nil {
		t.Fatalf("AdminCreate: %v", err)
	}
	if f.create == nil {
		t.Fatal("no plugin created")
	}
	if f.create.Visibility != model.PluginVisibilitySystem {
		t.Fatalf("visibility=%q want system", f.create.Visibility)
	}
	if f.create.SpaceID != nil {
		t.Fatalf("connector Space=%v want NULL", *f.create.SpaceID)
	}
	if !f.createScope.Admin {
		t.Fatalf("create not under admin scope: %#v", f.createScope)
	}
}

// TestAdminCreateSkillMintsSystemGlobalSpace verifies the admin create
// convention: visibility=system (全平台可见) in the empty global Space.
func TestAdminCreateSkillMintsSystemGlobalSpace(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{}}
	if _, err := fixedService(f).AdminCreate(context.Background(), adminCaller, adminSkillRequest()); err != nil {
		t.Fatalf("AdminCreate: %v", err)
	}
	if f.create.Visibility != model.PluginVisibilitySystem {
		t.Fatalf("visibility=%q want system", f.create.Visibility)
	}
	if f.create.SpaceID == nil || *f.create.SpaceID != adminGlobalSpace {
		t.Fatalf("skill Space=%v want empty global", f.create.SpaceID)
	}
}

// TestAdminCreateAttachesDefaultVisiblePlacement is the market-visibility
// regression: an admin create must auto-attach a "default", visible placement
// (carrying the plugin's own category) so the plugin surfaces in the tenant
// market without a publish. Without the placement the tenant List's placement
// JOIN filters the plugin out entirely.
func TestAdminCreateAttachesDefaultVisiblePlacement(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{}}
	category := "cat-ops"
	req := adminSkillRequest()
	req.CategoryID = &category
	if _, err := fixedService(f).AdminCreate(context.Background(), adminCaller, req); err != nil {
		t.Fatalf("AdminCreate: %v", err)
	}
	if len(f.createPlace) != 1 {
		t.Fatalf("placements = %#v, want exactly one default placement", f.createPlace)
	}
	pl := f.createPlace[0]
	if pl.PlacementCode != "default" || !pl.Visible {
		t.Fatalf("placement = %#v, want default+visible", pl)
	}
	if pl.CategoryID == nil || *pl.CategoryID != category {
		t.Fatalf("placement category = %v, want the plugin's category %q", pl.CategoryID, category)
	}
}

// TestAdminCreateNullCategoryStillPlaces confirms a category-less admin create
// still auto-attaches a visible default placement (a null-category placement
// lists in the market) rather than skipping placement entirely.
func TestAdminCreateNullCategoryStillPlaces(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{}}
	if _, err := fixedService(f).AdminCreate(context.Background(), adminCaller, adminSkillRequest()); err != nil {
		t.Fatalf("AdminCreate: %v", err)
	}
	if len(f.createPlace) != 1 || f.createPlace[0].PlacementCode != "default" || !f.createPlace[0].Visible {
		t.Fatalf("placements = %#v, want one visible default placement", f.createPlace)
	}
	if f.createPlace[0].CategoryID != nil {
		t.Fatalf("placement category = %v, want nil", f.createPlace[0].CategoryID)
	}
}

// TestAdminUpdatePreservesPublisherWhenOmitted confirms an admin metadata edit
// that carries no publisher falls back to the row's existing one rather than
// blanking a backfilled skill's publisher (Octo-Q P1).
func TestAdminUpdatePreservesRatingInResponse(t *testing.T) {
	space := adminGlobalSpace
	rating := 4
	existing := &model.Plugin{ID: "skill-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySystem, Rating: &rating, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
	detail, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", adminSkillRequest())
	if err != nil {
		t.Fatalf("AdminUpdate: %v", err)
	}
	if detail.Plugin.Rating == nil || *detail.Plugin.Rating != rating {
		t.Fatalf("rating not preserved in response: %#v", detail.Plugin.Rating)
	}
}

func TestAdminUpdatePreservesPublisherWhenOmitted(t *testing.T) {
	space := adminGlobalSpace
	existing := &model.Plugin{ID: "skill-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySystem, Publisher: "Mininglamp", Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
	req := adminSkillRequest() // carries no publisher
	if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", req); err != nil {
		t.Fatalf("AdminUpdate: %v", err)
	}
	if f.update == nil || f.update.Publisher != "Mininglamp" {
		t.Fatalf("publisher not preserved: %#v", f.update)
	}
}

// TestAdminUpdateNormalizesLegacyPublicToSystem confirms an admin edit of a
// legacy public row does not fail validVisibility (which now rejects public) but
// normalizes the preserved visibility to the unified `system` value.
func TestAdminUpdateNormalizesLegacyPublicToSystem(t *testing.T) {
	space := adminGlobalSpace
	existing := &model.Plugin{ID: "skill-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilityPublic, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
	if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", adminSkillRequest()); err != nil {
		t.Fatalf("AdminUpdate of a legacy public row failed: %v", err)
	}
	if f.update == nil || f.update.Visibility != model.PluginVisibilitySystem {
		t.Fatalf("legacy public not normalized to system: %#v", f.update)
	}
}

// TestAdminUpdateAppliesSubmittedVersion pins that an admin edit which SENDS a
// version applies it (AdminUpdate previously restored the stored label
// unconditionally, silently discarding a submitted version and returning 200).
func TestAdminUpdateAppliesSubmittedVersion(t *testing.T) {
	space := adminGlobalSpace
	oldVer := "1.0.0"
	existing := &model.Plugin{ID: "skill-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySystem, CurrentVersion: &oldVer, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
	req := adminSkillRequest()
	req.Version = "3.0.0"
	if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", req); err != nil {
		t.Fatalf("AdminUpdate: %v", err)
	}
	if f.update == nil || f.update.CurrentVersion == nil || *f.update.CurrentVersion != "3.0.0" {
		t.Fatalf("submitted version not applied: %#v", f.update)
	}
}

// TestAdminCreateStampsNewCurrentVersionID is the N2-residual regression: a
// snapshotting admin create must return the new plugin_versions id on the
// response plugin (it advanced current_version_id in the DB), not a nil pointer
// that a follow-up GET contradicts.
func TestAdminCreateStampsNewCurrentVersionID(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{}}
	detail, err := fixedService(f).AdminCreate(context.Background(), adminCaller, adminSkillRequest())
	if err != nil {
		t.Fatalf("AdminCreate: %v", err)
	}
	if detail.Plugin.CurrentVersionID == nil || *detail.Plugin.CurrentVersionID != "ver-snap" {
		t.Fatalf("admin create response current_version_id = %v, want ver-snap", detail.Plugin.CurrentVersionID)
	}
}

// TestAdminUpdateStampsNewCurrentVersionID is the N2-residual regression on the
// admin edit path: an admin edit snapshots a new version, so the response must
// carry the NEW id, not the stale stored one.
func TestAdminUpdateStampsNewCurrentVersionID(t *testing.T) {
	space := adminGlobalSpace
	oldVerID := "ver-old"
	existing := &model.Plugin{ID: "skill-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySystem, CurrentVersionID: &oldVerID, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
	detail, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", adminSkillRequest())
	if err != nil {
		t.Fatalf("AdminUpdate: %v", err)
	}
	if detail.Plugin.CurrentVersionID == nil || *detail.Plugin.CurrentVersionID != "ver-snap" {
		t.Fatalf("admin update response current_version_id = %v, want the new ver-snap (not stale %q)", detail.Plugin.CurrentVersionID, oldVerID)
	}
}

// through the admin update path.
func TestAdminUpdateRejectsTypeChange(t *testing.T) {
	space := adminGlobalSpace
	existing := &model.Plugin{ID: "plugin-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilityPublic, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": existing}}
	req := connectorRequest(json.RawMessage(`{"env_user_supplied":[],"headers_user_supplied":[]}`))
	_, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "plugin-1", req)
	if err != ErrInvalidRequest {
		t.Fatalf("err=%v want ErrInvalidRequest", err)
	}
}

// TestAdminUpdatePreservesTenantVisibilityAndOwner is the A1 regression: an
// admin metadata edit of a tenant-private, cross-Space row must NOT force-flip
// it public, and must leave the owner provenance intact.
func TestAdminUpdatePreservesTenantVisibilityAndOwner(t *testing.T) {
	tenantSpace := "tenant-space"
	existing := &model.Plugin{ID: "skill-1", Name: "Tenant Skill", Type: model.PluginTypeSkill, OwnerUID: "tenant-user", SpaceID: &tenantSpace, Visibility: model.PluginVisibilityPrivate, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
	// The request even tries to set a public visibility; it must be ignored.
	req := adminSkillRequest()
	req.Visibility = model.PluginVisibilityPublic
	if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", req); err != nil {
		t.Fatalf("AdminUpdate: %v", err)
	}
	if f.update == nil {
		t.Fatal("no update issued")
	}
	if f.update.Visibility != model.PluginVisibilityPrivate {
		t.Fatalf("visibility force-flipped to %q; tenant-private row would be published", f.update.Visibility)
	}
	if f.update.SpaceID == nil || *f.update.SpaceID != tenantSpace {
		t.Fatalf("space not preserved: %v", f.update.SpaceID)
	}
	if f.update.OwnerUID != "tenant-user" {
		t.Fatalf("owner rewritten to %q, want tenant-user", f.update.OwnerUID)
	}
}

// TestAdminUpdateResolvesRelationTargetsUnderAdminScope is the regression for
// the admin relation-target scope loss: an admin edit of a space-scoped expert
// that relates to a space-scoped skill must validate the relation target
// cross-Space (admin scope). Under the tenant scope the target is invisible, so
// the edit would 404 (echo) or silently drop every edge (omit).
func TestAdminUpdateResolvesRelationTargetsUnderAdminScope(t *testing.T) {
	tenant := "tenant-space"
	expert := &model.Plugin{ID: "expert-1", Name: "E", Type: model.PluginTypeExpert, OwnerUID: "tenant-user", SpaceID: &tenant, Visibility: model.PluginVisibilitySpace, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	skill := &model.Plugin{ID: "skill-1", Name: "S", Type: model.PluginTypeSkill, OwnerUID: "tenant-user", SpaceID: &tenant, Visibility: model.PluginVisibilitySpace, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{
		scopeAware: true,
		plugins:    map[string]*model.Plugin{"expert-1": expert, "skill-1": skill},
		relations:  map[string][]model.PluginRelation{"expert-1": {{ID: "r1", SourcePluginID: "expert-1", TargetPluginID: "skill-1", Type: "expert_skill", Status: 1}}},
	}
	manifest := json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"E","plugin_type":"expert","name":"e","description":"d","labels":[],"examples":[]}`)
	pkg := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"AGENTS.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# doc"}]}`)
	req := WriteRequest{
		Name:       "E",
		Type:       model.PluginTypeExpert,
		Visibility: model.PluginVisibilitySpace,
		Tags:       json.RawMessage(`[]`),
		Manifest:   manifest,
		Package:    pkg,
		Relations:  []RelationRequest{{TargetPluginID: "skill-1", Type: "expert_skill", SortOrder: 0}},
	}
	if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "expert-1", req); err != nil {
		t.Fatalf("admin update echoing a space-scoped relation failed: %v", err)
	}
	// The relation survived to the repo — not dropped by a lost-scope 404.
	if f.update == nil || len(f.updateRels) != 1 || f.updateRels[0].TargetPluginID != "skill-1" {
		t.Fatalf("relation not carried to Update: %#v", f.updateRels)
	}
}

// TestAdminUpdateAcceptsStorageAttachmentUnderRowSpace is the P1-1 regression:
// an admin edit of a tenant-owned skill whose package carries a storage
// attachment (the normal shape after expand-skills spills a file to object
// storage) must canonicalize the storage key against the ROW's real Space, not
// the empty global Space. Before the fix the empty Space failed safeObjectSegment
// and the whole edit returned ErrInvalidRequest.
func TestAdminUpdateAcceptsStorageAttachmentUnderRowSpace(t *testing.T) {
	tenantSpace := "tenant-space"
	existing := &model.Plugin{ID: "skill-1", Name: "Tenant Skill", Type: model.PluginTypeSkill, OwnerUID: "tenant-user", SpaceID: &tenantSpace, Visibility: model.PluginVisibilityPrivate, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
	manifest := json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Tenant Skill","plugin_type":"skill","name":"tenant-skill","description":"d","labels":[],"examples":[]}`)
	pkg := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[` +
		`{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Tenant Skill"},` +
		`{"path":"assets/logo.bin","content_type":"storage","mime_type":"application/octet-stream","content_size":10,"content_hash":"sha256:0000000000000000000000000000000000000000000000000000000000000000","storage_uri":"plugins/tenant-space/attachments/skill-skill-1-deadbeefdeadbeef.bin"}` +
		`]}`)
	req := WriteRequest{Name: "Tenant Skill", Type: model.PluginTypeSkill, Tags: json.RawMessage(`[]`), Manifest: manifest, Package: pkg}
	if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", req); err != nil {
		t.Fatalf("AdminUpdate with storage attachment under row space failed: %v", err)
	}
	if f.update == nil || f.update.SpaceID == nil || *f.update.SpaceID != tenantSpace {
		t.Fatalf("row space not preserved: %#v", f.update)
	}
}

// TestAdminDeleteUsesAdminScope confirms delete runs cross-Space with no owner
// predicate (Scope.Admin), unlike the owner-gated Delete.
func TestAdminDeleteUsesAdminScope(t *testing.T) {
	space := adminGlobalSpace
	existing := &model.Plugin{ID: "plugin-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilityPublic, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": existing}}
	if err := fixedService(f).AdminDelete(context.Background(), adminCaller, "plugin-1"); err != nil {
		t.Fatalf("AdminDelete: %v", err)
	}
	if !f.deleteScope.Admin || f.deleteID != "plugin-1" {
		t.Fatalf("deleteScope=%#v id=%q", f.deleteScope, f.deleteID)
	}
	if f.deleteGraphID != "" {
		t.Fatalf("a skill must take the single-row Delete, not DeleteGraph: %q", f.deleteGraphID)
	}
}

// TestAdminDeleteExpertRemovesEmbeddedChildrenNotStandalone is the orphaned-child
// regression: deleting an expert top routes through DeleteGraph and tears down its
// embedded bundled skills, while a standalone catalog skill (is_embedded=0) merely
// referenced by the same expert is left intact.
func TestAdminDeleteExpertRemovesEmbeddedChildrenNotStandalone(t *testing.T) {
	space := "space-x"
	expert := &model.Plugin{ID: "expert-1", Name: "E", Type: model.PluginTypeExpert, SpaceID: &space, Visibility: model.PluginVisibilitySpace, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	embedded := &model.Plugin{ID: "skill-emb", Name: "Bundled", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySpace, IsEmbedded: true, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	standalone := &model.Plugin{ID: "skill-std", Name: "Shared", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySystem, IsEmbedded: false, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{
		plugins: map[string]*model.Plugin{"expert-1": expert, "skill-emb": embedded, "skill-std": standalone},
		relations: map[string][]model.PluginRelation{"expert-1": {
			{ID: "r1", SourcePluginID: "expert-1", TargetPluginID: "skill-emb", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", Status: 1},
			{ID: "r2", SourcePluginID: "expert-1", TargetPluginID: "skill-std", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", Status: 1},
		}},
	}
	if err := fixedService(f).AdminDelete(context.Background(), adminCaller, "expert-1"); err != nil {
		t.Fatalf("AdminDelete: %v", err)
	}
	if f.deleteGraphID != "expert-1" {
		t.Fatalf("expert must be deleted through DeleteGraph, got graph id %q", f.deleteGraphID)
	}
	if f.deleteID != "" {
		t.Fatalf("expert must not use the single-row Delete: %q", f.deleteID)
	}
	if len(f.deleteChildIDs) != 1 || f.deleteChildIDs[0] != "skill-emb" {
		t.Fatalf("child ids = %#v, want only the embedded [skill-emb]", f.deleteChildIDs)
	}
}

// TestAdminUpdateRejectsEmbeddedChild pins that a bundled skill / squad member
// (is_embedded=1) cannot be content-edited through the standalone PATCH surface —
// it is owned by its container graph and reported as not found.
func TestAdminUpdateRejectsEmbeddedChild(t *testing.T) {
	space := "space-x"
	embedded := &model.Plugin{ID: "skill-emb", Name: "Bundled", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilitySpace, IsEmbedded: true, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-emb": embedded}}
	if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-emb", adminSkillRequest()); err != ErrNotFound {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
	if f.update != nil {
		t.Fatal("an embedded child must not reach Update")
	}
}

// TestAdminUpdatePreservesCurrentVersion confirms the reupload/edit response
// carries the published version LABEL, not just its id.
func TestAdminUpdatePreservesCurrentVersion(t *testing.T) {
	space := adminGlobalSpace
	ver := "3.1.0"
	verID := "ver-9"
	existing := &model.Plugin{ID: "skill-1", Name: "Ops Skill", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilityPublic, CurrentVersionID: &verID, CurrentVersion: &ver, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": existing}}
	if _, err := fixedService(f).AdminUpdate(context.Background(), adminCaller, "skill-1", adminSkillRequest()); err != nil {
		t.Fatalf("AdminUpdate: %v", err)
	}
	if f.update == nil || f.update.CurrentVersion == nil || *f.update.CurrentVersion != ver {
		t.Fatalf("current_version not preserved: %#v", f.update)
	}
}
