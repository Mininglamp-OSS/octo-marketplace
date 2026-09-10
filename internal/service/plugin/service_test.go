package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

type fakeStore struct {
	plugins        map[string]*model.Plugin
	relations      map[string][]model.PluginRelation
	getIDs         []string
	listScope      pluginrepo.Scope
	listFilter     pluginrepo.ListFilter
	create         *model.Plugin
	createScope    pluginrepo.Scope
	createRels     []model.PluginRelation
	createPlace    []model.PluginPlacement
	createAudit    model.PluginAuditLog
	createSnapshot bool
	graphNodes     []pluginrepo.Mutation
	graphScope     pluginrepo.Scope
	graphErr       error
	rebuildTop     *pluginrepo.Mutation
	rebuildChild   []pluginrepo.Mutation
	rebuildOldIDs  []string
	rebuildScope   pluginrepo.Scope
	rebuildErr     error
	update         *model.Plugin
	updateRels     []model.PluginRelation
	updateSnapshot bool
	updateErr      error
	ratingParams   pluginrepo.RatingParams
	ratingScope    pluginrepo.Scope
	deleteScope    pluginrepo.Scope
	deleteID       string
	deleteGraphID  string
	deleteChildIDs []string
	versionID      string
	versions       []model.PluginVersion
	scopeAware     bool
	versionTotal   int64
	list           []model.Plugin
	memberCounts   map[string]int
	memberCountIDs []string
	// declaredCounts overrides the unfiltered declared-relation count per plugin;
	// unset entries default to len(relations[id]) so declared==visible and the
	// install dependency-visibility guard stays inert for existing tests.
	declaredCounts map[string]int
	tags           []model.TagFilter
	tagFilter      pluginrepo.TagListFilter
	err            error
	// review carries the review-request half of the fake (see review_fake_test.go).
	review reviewFake
	// listing carries the publish/delist half (see review_fake_test.go).
	listing         listingFake
	reviewPolicy    model.PluginReviewPolicy
	reviewPolicyErr error
	reviewPolicySet *bool
}

func (f *fakeStore) GetReviewPolicy(_ context.Context, _ pluginrepo.Scope) (model.PluginReviewPolicy, error) {
	if f.reviewPolicyErr != nil {
		return model.PluginReviewPolicy{}, f.reviewPolicyErr
	}
	return f.reviewPolicy, nil
}

func (f *fakeStore) UpsertReviewPolicy(_ context.Context, _ pluginrepo.Scope, enabled bool, _, _ string) (model.PluginReviewPolicy, error) {
	f.reviewPolicySet = &enabled
	if f.reviewPolicyErr != nil {
		return model.PluginReviewPolicy{}, f.reviewPolicyErr
	}
	return model.PluginReviewPolicy{IsAutoApproveEnabled: enabled}, nil
}

func (f *fakeStore) List(_ context.Context, s pluginrepo.Scope, filter pluginrepo.ListFilter) ([]model.Plugin, int64, error) {
	f.listScope = s
	f.listFilter = filter
	return f.list, int64(len(f.list)), f.err
}
func (f *fakeStore) ListTags(_ context.Context, _ pluginrepo.Scope, filter pluginrepo.TagListFilter) ([]model.TagFilter, error) {
	f.tagFilter = filter
	return f.tags, f.err
}
func (f *fakeStore) GetWithRelations(_ context.Context, sc pluginrepo.Scope, id string) (*model.Plugin, []model.PluginRelation, error) {
	f.getIDs = append(f.getIDs, id)
	if f.err != nil {
		return nil, nil, f.err
	}
	p := f.plugins[id]
	if p == nil {
		return nil, nil, pluginrepo.ErrNotFound
	}
	// Optional visibilitySQL emulation: a space/private row is visible only to a
	// caller in its own Space, unless the scope is Admin (cross-Space). Lets a
	// test reproduce the admin-vs-tenant relation-target scope divergence.
	if f.scopeAware && !sc.Admin {
		spaceScoped := p.Visibility == model.PluginVisibilitySpace || p.Visibility == model.PluginVisibilityPrivate
		if spaceScoped && (p.SpaceID == nil || *p.SpaceID != sc.SpaceID) {
			return nil, nil, pluginrepo.ErrNotFound
		}
	}
	copy := *p
	rels := append([]model.PluginRelation(nil), f.relations[id]...)
	return &copy, rels, nil
}
func (f *fakeStore) Create(_ context.Context, s pluginrepo.Scope, m pluginrepo.Mutation) (*pluginrepo.RelationSync, error) {
	p := m.Plugin
	f.create, f.createRels, f.createScope = &p, m.Relations, s
	f.createPlace = m.Placements
	f.createSnapshot = m.SnapshotVersion
	f.createAudit = model.PluginAuditLog{OperatorID: m.OperatorID, OperatorName: m.OperatorName, RequestID: m.RequestID, Remark: m.Remark}
	if f.err != nil {
		return nil, f.err
	}
	created := make([]string, 0, len(m.Relations))
	for i := range m.Relations {
		if m.Relations[i].ID == "" {
			m.Relations[i].ID = "relation-created"
		}
		created = append(created, m.Relations[i].ID)
	}
	f.createRels = m.Relations
	sync := &pluginrepo.RelationSync{Created: created, Updated: []string{}, Deleted: []string{}, Relations: m.Relations}
	if m.SnapshotVersion {
		sync.NewVersionID = "ver-snap"
	}
	return sync, nil
}
func (f *fakeStore) Update(_ context.Context, _ pluginrepo.Scope, m pluginrepo.Mutation) (*pluginrepo.RelationSync, error) {
	p := m.Plugin
	f.update, f.updateRels = &p, m.Relations
	f.updateSnapshot = m.SnapshotVersion
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if f.err != nil {
		return nil, f.err
	}
	sync := &pluginrepo.RelationSync{Created: []string{}, Updated: []string{}, Deleted: []string{}, Relations: m.Relations}
	if m.SnapshotVersion {
		sync.NewVersionID = "ver-snap"
	}
	return sync, nil
}
func (f *fakeStore) UpdateRating(_ context.Context, s pluginrepo.Scope, p pluginrepo.RatingParams) (*model.Plugin, error) {
	f.ratingScope, f.ratingParams = s, p
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if f.err != nil {
		return nil, f.err
	}
	plugin := f.plugins[p.PluginID]
	if plugin == nil {
		return nil, pluginrepo.ErrNotFound
	}
	copy := *plugin
	copy.Rating = p.Rating
	return &copy, nil
}
func (f *fakeStore) CreateGraph(_ context.Context, s pluginrepo.Scope, nodes []pluginrepo.Mutation) ([]*pluginrepo.RelationSync, error) {
	f.graphNodes, f.graphScope = nodes, s
	if f.graphErr != nil {
		return nil, f.graphErr
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.plugins == nil {
		f.plugins = map[string]*model.Plugin{}
	}
	if f.relations == nil {
		f.relations = map[string][]model.PluginRelation{}
	}
	syncs := make([]*pluginrepo.RelationSync, len(nodes))
	rc := 0
	for i := range nodes {
		p := nodes[i].Plugin
		stored := p
		f.plugins[p.ID] = &stored
		rels := nodes[i].Relations
		created := make([]string, 0, len(rels))
		for j := range rels {
			if rels[j].ID == "" {
				rc++
				rels[j].ID = "relation-" + strconv.Itoa(rc)
			}
			created = append(created, rels[j].ID)
		}
		f.relations[p.ID] = append([]model.PluginRelation(nil), rels...)
		syncs[i] = &pluginrepo.RelationSync{Created: created, Updated: []string{}, Deleted: []string{}, Relations: rels}
		if nodes[i].SnapshotVersion {
			syncs[i].NewVersionID = "ver-snap"
		}
	}
	return syncs, nil
}
func (f *fakeStore) Delete(_ context.Context, s pluginrepo.Scope, id, _, _, _ string, _ *string) error {
	f.deleteScope, f.deleteID = s, id
	return f.err
}
func (f *fakeStore) DeleteGraph(_ context.Context, s pluginrepo.Scope, topID string, _, _, _ string, _ *string) error {
	// Emulate the repo's in-tx derivation: the embedded child set is resolved from
	// the committed graph here, NOT supplied by the caller, so these fakes exercise
	// the same derivation the real DeleteGraph performs under the top's lock.
	f.deleteScope, f.deleteGraphID, f.deleteChildIDs = s, topID, f.collectEmbeddedChildren(topID)
	return f.err
}
func (f *fakeStore) RebuildGraph(_ context.Context, s pluginrepo.Scope, top pluginrepo.Mutation, children []pluginrepo.Mutation) (*pluginrepo.RelationSync, error) {
	// Emulate the repo's in-tx derivation: derive the previous embedded child set
	// from the committed graph BEFORE the new children overwrite it, mirroring the
	// real RebuildGraph which resolves it under the top's FOR UPDATE lock instead of
	// trusting a caller-supplied pre-parse snapshot.
	oldChildIDs := f.collectEmbeddedChildren(top.Plugin.ID)
	f.rebuildTop, f.rebuildChild, f.rebuildOldIDs, f.rebuildScope = &top, children, oldChildIDs, s
	if f.rebuildErr != nil {
		return nil, f.rebuildErr
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.plugins == nil {
		f.plugins = map[string]*model.Plugin{}
	}
	if f.relations == nil {
		f.relations = map[string][]model.PluginRelation{}
	}
	// Persist the new children and the rebuilt top so a follow-up read reflects
	// the swap; soft-delete the old children by dropping them from the store.
	for i := range children {
		p := children[i].Plugin
		stored := p
		f.plugins[p.ID] = &stored
		f.relations[p.ID] = append([]model.PluginRelation(nil), children[i].Relations...)
	}
	tp := top.Plugin
	f.plugins[tp.ID] = &tp
	for _, id := range oldChildIDs {
		delete(f.plugins, id)
		delete(f.relations, id)
	}
	rels := top.Relations
	created := make([]string, 0, len(rels))
	for j := range rels {
		if rels[j].ID == "" {
			rels[j].ID = "relation-rebuilt-" + strconv.Itoa(j+1)
		}
		created = append(created, rels[j].ID)
	}
	f.relations[tp.ID] = append([]model.PluginRelation(nil), rels...)
	sync := &pluginrepo.RelationSync{Created: created, Updated: []string{}, Deleted: []string{}, Relations: rels}
	if top.SnapshotVersion {
		sync.NewVersionID = "ver-snap"
	}
	return sync, nil
}

// collectEmbeddedChildren mirrors the repository's in-tx collectEmbeddedChildren:
// it resolves the top's embedded descendants from the fake's committed graph
// (plugins + relations), collecting only is_embedded targets so a standalone
// catalog row the top merely references is never torn down. RebuildGraph and
// DeleteGraph call it so the service tests exercise derivation-from-committed-
// state instead of a caller-supplied child list.
func (f *fakeStore) collectEmbeddedChildren(topID string) []string {
	top := f.plugins[topID]
	if top == nil {
		return nil
	}
	embeddedTargets := func(source, relType string) []string {
		var out []string
		for _, r := range f.relations[source] {
			if r.Type != relType {
				continue
			}
			if t := f.plugins[r.TargetPluginID]; t != nil && t.IsEmbedded {
				out = append(out, r.TargetPluginID)
			}
		}
		return out
	}
	switch top.Type {
	case model.PluginTypeExpert:
		return embeddedTargets(topID, "expert_skill")
	case model.PluginTypeExpertTeam:
		var ids []string
		for _, member := range embeddedTargets(topID, "expert_team_expert") {
			ids = append(ids, member)
			ids = append(ids, embeddedTargets(member, "expert_skill")...)
		}
		return ids
	}
	return nil
}

func (f *fakeStore) ListVersions(_ context.Context, _ pluginrepo.Scope, id string, _, _ int) ([]model.PluginVersion, int64, error) {
	f.versionID = id
	return f.versions, f.versionTotal, f.err
}
func (f *fakeStore) CountMemberRelations(_ context.Context, teamIDs []string) (map[string]int, error) {
	f.memberCountIDs = teamIDs
	if f.memberCounts == nil {
		return map[string]int{}, f.err
	}
	return f.memberCounts, f.err
}
func (f *fakeStore) CountDeclaredRelations(_ context.Context, id string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	if n, ok := f.declaredCounts[id]; ok {
		return n, nil
	}
	return len(f.relations[id]), nil
}

func fixedService(f *fakeStore) *Service {
	ids := []string{"plugin-new", "audit-new", "relation-new", "version-new", "placement-new"}
	n := 0
	return New(f).WithRuntime(func() string { x := ids[n%len(ids)]; n++; return x }, func() time.Time { return time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC) })
}

var testCaller = Caller{UID: "user-1", Name: "Alice", SpaceID: "space-a", RequestID: "request-1"}

func validRequest() WriteRequest {
	manifest := json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Example Plugin","plugin_type":"expert","name":"example-plugin","description":"An example plugin.","labels":["one","two"],"examples":[]}`)
	pkg := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"AGENTS.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Example Plugin"}]}`)
	return WriteRequest{Name: "  Example Plugin  ", Type: model.PluginTypeExpert, Visibility: model.PluginVisibilityPrivate, Tags: json.RawMessage(`["one","one","two"]`), Manifest: manifest, Package: pkg}
}

func connectorRequest(config json.RawMessage) WriteRequest {
	manifest := json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Example Plugin","plugin_type":"connector","name":"example-plugin","description":"An example plugin.","labels":["one","two"],"examples":[],"config":` + string(config) + `}`)
	pkg := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","connector":{"type":"mcp","source":"connector.example-plugin"},"attachments":[{"path":"mcp.json","content_type":"raw","mime_type":"application/json","raw_content":"{\"mcpServers\":{}}"}]}`)
	return WriteRequest{Name: "Example Plugin", Type: model.PluginTypeConnector, Visibility: model.PluginVisibilityPrivate, Tags: json.RawMessage(`["one","two"]`), Manifest: manifest, Package: pkg}
}

func TestCreateValidatesAndCanonicalizes(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{}}
	_, err := fixedService(f).Create(context.Background(), testCaller, validRequest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantManifest := `{"$schema":"cowork-plugin-manifest-2.0.json","description":"An example plugin.","examples":[],"labels":["one","two"],"name":"example-plugin","plugin_name":"Example Plugin","plugin_type":"expert"}`
	if f.create.Name != "Example Plugin" || string(f.create.Manifest) != wantManifest {
		t.Fatalf("created = %#v", f.create)
	}
	wantManifestHash, _ := hashForTest(json.RawMessage(wantManifest))
	if f.create.ManifestHash != wantManifestHash {
		t.Fatalf("manifest hash = %q, want %q", f.create.ManifestHash, wantManifestHash)
	}
	if f.create.PluginHash == "" || f.create.PluginHash == f.create.ManifestHash {
		t.Fatalf("combined plugin hash = %q", f.create.PluginHash)
	}
	if string(f.create.Tags) != `["one","two"]` {
		t.Fatalf("tags = %s", f.create.Tags)
	}
}

// TestCreateAndUpdateFlagVersionSnapshot locks that the tenant save paths request
// a per-save version snapshot from the repository.
func TestCreateAndUpdateFlagVersionSnapshot(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{}}
	svc := fixedService(f)
	if _, err := svc.Create(context.Background(), testCaller, validRequest()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !f.createSnapshot {
		t.Fatal("tenant Create did not flag SnapshotVersion")
	}

	f.plugins["plugin-1"] = &model.Plugin{ID: "plugin-1", Type: model.PluginTypeExpert, OwnerUID: testCaller.UID, SpaceID: &testCaller.SpaceID}
	if _, err := svc.Update(context.Background(), testCaller, "plugin-1", validRequest()); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !f.updateSnapshot {
		t.Fatal("tenant Update did not flag SnapshotVersion")
	}
}

// TestCreateAttachesDefaultPlacement locks that a tenant Create is
// self-sufficient for market visibility: it hands the store exactly one visible
// "default" placement carrying the plugin's own category, so the plugin surfaces
// in scene-scoped lists without a separate publish call.
func TestCreateAttachesDefaultPlacement(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{}}
	category := "cat-1"
	req := validRequest()
	req.CategoryID = &category
	if _, err := fixedService(f).Create(context.Background(), testCaller, req); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(f.createPlace) != 1 {
		t.Fatalf("placements = %#v, want exactly one default placement", f.createPlace)
	}
	pl := f.createPlace[0]
	if pl.PlacementCode != "default" || !pl.Visible || pl.CategoryID == nil || *pl.CategoryID != category {
		t.Fatalf("placement = %#v, want default+visible carrying the plugin category", pl)
	}
}

func TestListRejectsUnsupportedSort(t *testing.T) {
	svc := fixedService(&fakeStore{})
	for _, params := range []ListParams{{Sort: "surprise"}, {Sort: "placement"}} {
		if _, _, err := svc.List(context.Background(), testCaller, params); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("List(%#v) error = %v, want ErrInvalidRequest", params, err)
		}
	}
}

func TestListFillsTeamMemberCountsAndKeepsIcons(t *testing.T) {
	f := &fakeStore{
		list: []model.Plugin{
			{ID: "team-1", Type: model.PluginTypeExpertTeam},
			{ID: "skill-1", Type: model.PluginTypeSkill, Icon: "https://cdn.example.com/icon.png"},
		},
		memberCounts: map[string]int{"team-1": 4},
	}
	items, _, err := fixedService(f).List(context.Background(), testCaller, ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.memberCountIDs) != 1 || f.memberCountIDs[0] != "team-1" {
		t.Fatalf("member count lookup = %#v", f.memberCountIDs)
	}
	if items[0].MemberCount != 4 || items[1].MemberCount != 0 {
		t.Fatalf("member counts = %d,%d", items[0].MemberCount, items[1].MemberCount)
	}
	// Without storage configured, http(s) icons pass through unchanged.
	if items[1].IconURL != "https://cdn.example.com/icon.png" {
		t.Fatalf("icon_url = %q", items[1].IconURL)
	}
}

func TestCreateStoresIconAndMaterializesConnectorToolCount(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{}}
	req := connectorRequest(json.RawMessage(`{}`))
	req.Icon = " https://cdn.example.com/icon.png "
	var pkg map[string]any
	if err := json.Unmarshal(req.Package, &pkg); err != nil {
		t.Fatal(err)
	}
	tools := map[string]any{"path": "connector/tools.json", "content_type": "raw", "mime_type": "application/json", "raw_content": `[{"name":"a"},{"name":"b"},{"name":"c"}]`}
	pkg["attachments"] = append(pkg["attachments"].([]any), tools)
	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	req.Package = raw
	if _, err := fixedService(f).Create(context.Background(), testCaller, req); err != nil {
		t.Fatal(err)
	}
	if f.create.Icon != "https://cdn.example.com/icon.png" {
		t.Fatalf("icon = %q", f.create.Icon)
	}
	if f.create.ToolCount != 3 {
		t.Fatalf("tool_count = %d", f.create.ToolCount)
	}
}

func TestListNormalizesAndBoundsTagFilters(t *testing.T) {
	f := &fakeStore{}
	svc := fixedService(f)
	if _, _, err := svc.List(context.Background(), testCaller, ListParams{Tags: []string{" a ", "", "b", "a"}}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(f.listFilter.Tags) != 2 || f.listFilter.Tags[0] != "a" || f.listFilter.Tags[1] != "b" {
		t.Fatalf("tags = %#v", f.listFilter.Tags)
	}
	over := make([]string, maxListTags+1)
	for i := range over {
		over[i] = "t" + strconv.Itoa(i)
	}
	for _, tags := range [][]string{over, {strings.Repeat("x", maxTagBytes+1)}, {"bad\xff"}} {
		if _, _, err := svc.List(context.Background(), testCaller, ListParams{Tags: tags}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("List(tags=%d) error = %v, want ErrInvalidRequest", len(tags), err)
		}
	}
}

func TestListTagsValidatesAndForwardsScopedFilter(t *testing.T) {
	f := &fakeStore{tags: []model.TagFilter{{Name: "dev", Count: 3}}}
	svc := fixedService(f)
	tags, err := svc.ListTags(context.Background(), testCaller, TagListParams{PlacementCode: " default ", Type: model.PluginTypeConnector, Keyword: " de ", Mine: true, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].Name != "dev" {
		t.Fatalf("tags = %#v", tags)
	}
	want := pluginrepo.TagListFilter{PlacementCode: "default", Type: model.PluginTypeConnector, Keyword: "de", Mine: true, Limit: 5}
	if f.tagFilter != want {
		t.Fatalf("filter = %#v", f.tagFilter)
	}
	if _, err := svc.ListTags(context.Background(), testCaller, TagListParams{Type: "bogus"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("type err = %v", err)
	}
	if _, err := svc.ListTags(context.Background(), testCaller, TagListParams{Limit: maxListLimit + 1}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("limit err = %v", err)
	}
	if _, err := svc.ListTags(context.Background(), Caller{}, TagListParams{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("caller err = %v", err)
	}
}

func TestCreateRejectsInvalidFieldsAndJSONShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WriteRequest)
	}{
		{"type", func(r *WriteRequest) { r.Type = "unknown" }},
		{"visibility", func(r *WriteRequest) { r.Visibility = "world" }},
		{"system visibility", func(r *WriteRequest) { r.Visibility = model.PluginVisibilitySystem }},
		{"empty name", func(r *WriteRequest) { r.Name = " " }},
		{"javascript icon", func(r *WriteRequest) { r.Icon = "javascript:alert(1)" }},
		{"data icon", func(r *WriteRequest) { r.Icon = "data:text/html;base64,x" }},
		{"traversal icon", func(r *WriteRequest) { r.Icon = "icons/../../etc/passwd" }},
		{"oversized icon", func(r *WriteRequest) { r.Icon = "https://cdn.example.com/" + strings.Repeat("x", maxIconBytes) }},
		{"manifest array", func(r *WriteRequest) { r.Manifest = json.RawMessage(`[]`) }},
		{"outer manifest name mismatch", func(r *WriteRequest) { r.Name = "Other" }},
		{"outer manifest type mismatch", func(r *WriteRequest) { r.Type = model.PluginTypeSkill }},
		{"outer manifest tags mismatch", func(r *WriteRequest) { r.Tags = json.RawMessage(`["other"]`) }},
		{"package scalar", func(r *WriteRequest) { r.Package = json.RawMessage(`true`) }},
		{"package missing manifest", func(r *WriteRequest) {
			r.Package = json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[]}`)
		}},
		{"tags object", func(r *WriteRequest) { r.Tags = json.RawMessage(`{}`) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validRequest()
			tt.mutate(&r)
			_, err := fixedService(&fakeStore{}).Create(context.Background(), testCaller, r)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestCreateRejectsPublicVisibilityForNonAdmin(t *testing.T) {
	// A tenant caller (IsSystemAdmin=false) may not self-publish a globally
	// visible plugin; public/system are admin-only on the tenant path. Mirrors the
	// import rule (TestImportRejectsPublicVisibility) and the legacy skill service.
	for _, vis := range []model.PluginVisibility{model.PluginVisibilityPublic, model.PluginVisibilitySystem} {
		req := validRequest()
		req.Visibility = vis
		if _, err := fixedService(&fakeStore{}).Create(context.Background(), testCaller, req); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("visibility=%s non-admin create err = %v, want ErrInvalidRequest", vis, err)
		}
	}
	// An admin caller (IsSystemAdmin=true) may set the unified `system` global
	// value, but NOT `public` — public is retired on the write path (validVisibility
	// rejects it for everyone), so even a systemAdmin upsert cannot mint one.
	admin := testCaller
	admin.IsSystemAdmin = true
	req := validRequest()
	req.Visibility = model.PluginVisibilitySystem
	if _, err := fixedService(&fakeStore{plugins: map[string]*model.Plugin{}}).Create(context.Background(), admin, req); err != nil {
		t.Fatalf("admin system create err = %v", err)
	}
	req.Visibility = model.PluginVisibilityPublic
	if _, err := fixedService(&fakeStore{}).Create(context.Background(), admin, req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("admin public create err = %v, want ErrInvalidRequest (public retired on write)", err)
	}
}

func TestCreateStampsTrustedIdentity(t *testing.T) {
	f := &fakeStore{}
	caller := testCaller
	caller.BotUID, caller.BotName = "bot-1", "Builder"
	if _, err := fixedService(f).Create(context.Background(), caller, validRequest()); err != nil {
		t.Fatal(err)
	}
	if f.create.OwnerUID != caller.UID || f.create.SpaceID == nil || *f.create.SpaceID != caller.SpaceID || f.create.CreatorName != caller.Name {
		t.Fatalf("identity not stamped: %#v", f.create)
	}
	if f.create.CreatedByType != "bot" || f.create.CreatedByBotUID == nil || *f.create.CreatedByBotUID != "bot-1" {
		t.Fatalf("bot provenance = %#v", f.create)
	}
	if f.createAudit.OperatorID != caller.UID || f.createAudit.OperatorName != caller.Name || f.createAudit.RequestID != caller.RequestID {
		t.Fatalf("audit identity = %#v", f.createAudit)
	}
}

func TestCrossSpaceNotFoundPropagatesWithoutLeak(t *testing.T) {
	f := &fakeStore{err: pluginrepo.ErrNotFound}
	if _, err := fixedService(f).Detail(context.Background(), testCaller, "plugin-other", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("detail err = %v", err)
	}
	if err := fixedService(f).Delete(context.Background(), testCaller, "plugin-other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete err = %v", err)
	}
}

func TestRelationSourceTargetValidation(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{"skill-1": {ID: "skill-1", Type: model.PluginTypeSkill}}}
	r := validRequest()
	r.Relations = []RelationRequest{{TargetPluginID: "skill-1", Type: "expert_skill"}}
	if _, err := fixedService(f).Create(context.Background(), testCaller, r); err != nil {
		t.Fatalf("valid relation: %v", err)
	}
	r.Relations[0].Data = nil
	r.Type = model.PluginTypeConnector
	if _, err := fixedService(f).Create(context.Background(), testCaller, r); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid source err = %v", err)
	}
}

func TestVersionListPropagatesExactTotalsAndNotFound(t *testing.T) {
	f := &fakeStore{
		plugins:      map[string]*model.Plugin{"plugin-1": {ID: "plugin-1", Type: model.PluginTypeExpert}},
		versions:     []model.PluginVersion{{ID: "version-1"}},
		versionTotal: 42,
	}
	svc := fixedService(f)
	versions, versionTotal, err := svc.ListVersions(context.Background(), testCaller, "plugin-1", 20, 40)
	if err != nil || len(versions) != 1 || versionTotal != 42 {
		t.Fatalf("versions=%#v total=%d err=%v", versions, versionTotal, err)
	}
	f.err = pluginrepo.ErrNotFound
	if _, _, err := svc.ListVersions(context.Background(), testCaller, "foreign", 20, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("version not found err=%v", err)
	}
}

func TestDeleteConflictMapsToServiceError(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{
		"plugin-1": {ID: "plugin-1", Type: model.PluginTypeExpert, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), ListingState: model.PluginListingStateDraft},
	}}
	f.err = pluginrepo.ErrConflict
	if err := fixedService(f).Delete(context.Background(), testCaller, "plugin-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Delete conflict = %v, want ErrConflict", err)
	}
}

func TestCreateRejectsSubmittedRelationIDs(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{"target-1": {ID: "target-1", Type: model.PluginTypeSkill}}}
	req := validRequest()
	req.Relations = []RelationRequest{{ID: "rel-1", TargetPluginID: "target-1", Type: "expert_skill"}}
	if _, err := fixedService(f).Create(context.Background(), testCaller, req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestUpdateAcceptsRelationIDAndMatchingSourceWireID(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{
		"plugin-1": {ID: "plugin-1", Name: "Example Plugin", Type: model.PluginTypeExpert, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID)},
		"target-1": {ID: "target-1", Type: model.PluginTypeSkill},
	}}
	req := validRequest()
	req.Relations = []RelationRequest{{ID: "rel-1", SourcePluginID: "plugin-1", TargetPluginID: "target-1", Type: "expert_skill"}}
	v, err := fixedService(f).Update(context.Background(), testCaller, "plugin-1", req)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Relations) != 1 || v.Relations[0].ID != "rel-1" {
		t.Fatalf("relations = %#v", v.Relations)
	}
	if v.RelationResult == nil {
		t.Fatal("missing relation result")
	}
}

// TestUpdateWithoutVersionKeepsCurrentVersion pins that a metadata edit which
// omits the optional version does NOT reset an imported plugin's declared
// current_version to the "1.0.0" default — every existing client omits the field.
func TestUpdateWithoutVersionKeepsCurrentVersion(t *testing.T) {
	ver, verID := "2.4.0", "ver-1"
	f := &fakeStore{plugins: map[string]*model.Plugin{
		"plugin-1": {ID: "plugin-1", Name: "Example Plugin", Type: model.PluginTypeExpert, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), CurrentVersion: &ver, CurrentVersionID: &verID},
	}}
	req := validRequest() // carries no Version
	if _, err := fixedService(f).Update(context.Background(), testCaller, "plugin-1", req); err != nil {
		t.Fatal(err)
	}
	if f.update == nil || f.update.CurrentVersion == nil || *f.update.CurrentVersion != ver {
		t.Fatalf("current_version not preserved on version-less update: %#v", f.update)
	}
}

func TestUpdateRejectsForeignRelationSourceWireID(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{
		"plugin-1": {ID: "plugin-1", Name: "Example Plugin", Type: model.PluginTypeExpert, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID)},
		"target-1": {ID: "target-1", Type: model.PluginTypeSkill},
	}}
	req := validRequest()
	req.Relations = []RelationRequest{{SourcePluginID: "another-plugin", TargetPluginID: "target-1", Type: "expert_skill"}}
	if _, err := fixedService(f).Update(context.Background(), testCaller, "plugin-1", req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestCreateReturnsRelationResultWithGeneratedIDs(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{"target-1": {ID: "target-1", Type: model.PluginTypeSkill}}}
	req := validRequest()
	req.Relations = []RelationRequest{{TargetPluginID: "target-1", Type: "expert_skill"}}
	v, err := fixedService(f).Create(context.Background(), testCaller, req)
	if err != nil {
		t.Fatal(err)
	}
	if v.RelationResult == nil || len(v.RelationResult.Created) != 1 || v.RelationResult.Created[0] != "relation-created" {
		t.Fatalf("relation result = %#v", v.RelationResult)
	}
	if len(v.Relations) != 1 || v.Relations[0].ID != "relation-created" {
		t.Fatalf("relations = %#v", v.Relations)
	}
}

// TestDeleteExpertRemovesEmbeddedChildrenNotStandalone is the P1-1 tenant
// regression: a tenant deleting their own expert routes through DeleteGraph under
// the TENANT scope and tears down its embedded bundled skills, while a standalone
// catalog skill (is_embedded=0) merely referenced by the same expert survives.
func TestDeleteExpertRemovesEmbeddedChildrenNotStandalone(t *testing.T) {
	expert := &model.Plugin{ID: "expert-1", Name: "E", Type: model.PluginTypeExpert, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), Visibility: model.PluginVisibilitySpace, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	embedded := &model.Plugin{ID: "skill-emb", Name: "Bundled", Type: model.PluginTypeSkill, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), Visibility: model.PluginVisibilitySpace, IsEmbedded: true, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	standalone := &model.Plugin{ID: "skill-std", Name: "Shared", Type: model.PluginTypeSkill, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), Visibility: model.PluginVisibilitySystem, IsEmbedded: false, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)}
	f := &fakeStore{
		plugins: map[string]*model.Plugin{"expert-1": expert, "skill-emb": embedded, "skill-std": standalone},
		relations: map[string][]model.PluginRelation{"expert-1": {
			{ID: "r1", SourcePluginID: "expert-1", TargetPluginID: "skill-emb", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", Status: 1},
			{ID: "r2", SourcePluginID: "expert-1", TargetPluginID: "skill-std", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill", Status: 1},
		}},
	}
	if err := fixedService(f).Delete(context.Background(), testCaller, "expert-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if f.deleteGraphID != "expert-1" {
		t.Fatalf("tenant expert must delete through DeleteGraph, got graph id %q (deleteID=%q)", f.deleteGraphID, f.deleteID)
	}
	if f.deleteScope.Admin {
		t.Fatal("tenant delete must run under the tenant scope, not admin")
	}
	if len(f.deleteChildIDs) != 1 || f.deleteChildIDs[0] != "skill-emb" {
		t.Fatalf("child ids = %#v, want only the embedded [skill-emb]", f.deleteChildIDs)
	}
}

// TestDeleteRequiresDelistFirst pins the symmetric gate with Service.update:
// a published, org-visible plugin cannot be soft-deleted by its author, because
// that would bypass the Delist Space-admin takedown gate (listing.go:141-148)
// irreversibly. An admin must Delist first, returning the row to draft; then
// the author can delete it.
func TestDeleteRequiresDelistFirst(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{
		"plugin-1": {ID: "plugin-1", Type: model.PluginTypeSkill, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStatePublished},
	}}
	err := fixedService(f).Delete(context.Background(), testCaller, "plugin-1")
	if !errors.Is(err, ErrListedRequiresReview) {
		t.Fatalf("Delete of published+space = %v, want ErrListedRequiresReview; author must not remove a live org plugin unilaterally", err)
	}
	if f.deleteID != "" || f.deleteGraphID != "" {
		t.Fatalf("repo.Delete/DeleteGraph were called (id=%q graph=%q); gate must fire BEFORE the repo", f.deleteID, f.deleteGraphID)
	}
}

// TestDeleteOfAPublishedExpertGraphAlsoRequiresDelistFirst covers the graph
// shape flagged in the blocker brief: expert/expert_team tops route through
// DeleteGraph, which is the MOST exposed shape (graph roots almost never have
// incoming live relations, so rejectLiveIncomingRelations never fires). The
// gate must run before the type switch so DeleteGraph is covered too.
func TestDeleteOfAPublishedExpertGraphAlsoRequiresDelistFirst(t *testing.T) {
	expert := &model.Plugin{ID: "expert-1", Type: model.PluginTypeExpert, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStatePublished}
	f := &fakeStore{plugins: map[string]*model.Plugin{"expert-1": expert}}
	err := fixedService(f).Delete(context.Background(), testCaller, "expert-1")
	if !errors.Is(err, ErrListedRequiresReview) {
		t.Fatalf("Delete of a published expert = %v, want ErrListedRequiresReview; the graph path must be gated too", err)
	}
	if f.deleteGraphID != "" {
		t.Fatalf("repo.DeleteGraph was called for %q; gate must fire BEFORE the type switch", f.deleteGraphID)
	}
}

// TestDeletePositiveRegressions cover the cases that MUST keep working:
// private+published (nobody else can read it, no review channel exists),
// space+draft (never listed), and space+delisted (an admin already took it
// down; the author can now clean it up). These pin that the gate is exactly
// (published AND space), not "published alone" or "space alone".
func TestDeletePositiveRegressions(t *testing.T) {
	cases := []struct {
		name string
		p    *model.Plugin
	}{
		{"private_published", &model.Plugin{ID: "p1", Type: model.PluginTypeSkill, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), Visibility: model.PluginVisibilityPrivate, ListingState: model.PluginListingStatePublished}},
		{"space_draft", &model.Plugin{ID: "p2", Type: model.PluginTypeSkill, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft}},
		{"space_delisted", &model.Plugin{ID: "p3", Type: model.PluginTypeSkill, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDelisted}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{plugins: map[string]*model.Plugin{tc.p.ID: tc.p}}
			if err := fixedService(f).Delete(context.Background(), testCaller, tc.p.ID); err != nil {
				t.Fatalf("Delete = %v, want nil; %s rows must stay deletable by the owner", err, tc.name)
			}
			if f.deleteID != tc.p.ID {
				t.Fatalf("deleteID = %q, want %q", f.deleteID, tc.p.ID)
			}
		})
	}
}

// TestSystemAdminCanDeleteListedPlugin pins the platform-operator escape hatch:
// matching Service.update, a system admin is exempt from the tenant gate so an
// operator can still remove abusive content immediately without a round-trip
// through Delist.
func TestSystemAdminCanDeleteListedPlugin(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{
		"plugin-1": {ID: "plugin-1", Type: model.PluginTypeSkill, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStatePublished},
	}}
	admin := testCaller
	admin.IsSystemAdmin = true
	if err := fixedService(f).Delete(context.Background(), admin, "plugin-1"); err != nil {
		t.Fatalf("admin Delete of listed plugin = %v, want nil; the platform-operator escape hatch must survive", err)
	}
}

// TestUpdateForwardsOwnEmbeddedChildEdge is the P0-1 service-level regression: the
// service must not itself block resubmitting an edge to an embedded child on an
// Update — it forwards the edge to the repo, which enforces the ALREADY-owns
// exemption. (A NEW edge to a foreign embedded child is rejected at the repo
// boundary; see the repo-level tests.)
func TestUpdateForwardsOwnEmbeddedChildEdge(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{
		"expert-1":  {ID: "expert-1", Name: "Example Plugin", Type: model.PluginTypeExpert, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID)},
		"skill-emb": {ID: "skill-emb", Type: model.PluginTypeSkill, IsEmbedded: true},
	}}
	req := validRequest()
	req.Relations = []RelationRequest{{ID: "rel-1", SourcePluginID: "expert-1", TargetPluginID: "skill-emb", Type: "expert_skill"}}
	if _, err := fixedService(f).Update(context.Background(), testCaller, "expert-1", req); err != nil {
		t.Fatalf("service Update blocked its own embedded-child edge: %v", err)
	}
	if len(f.updateRels) != 1 || f.updateRels[0].TargetPluginID != "skill-emb" {
		t.Fatalf("embedded-child edge not forwarded to repo: %#v", f.updateRels)
	}
}

// TestUpdateRejectsEmbeddedChildOutOfBand is the tenant-side embedded-guard
// regression (service.go): a bundled skill / squad member (is_embedded=1) is
// owned by its container graph and may be content-swapped only through a
// container reupload. Even the OWNER hitting the standalone /plugins upsert path
// on an embedded child gets ErrNotFound — the row is invisible to out-of-band
// edits — and the write never reaches the repo. Mirrors AdminUpdate's guard.
func TestUpdateRejectsEmbeddedChildOutOfBand(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{
		"skill-emb": {ID: "skill-emb", Name: "Bundled", Type: model.PluginTypeSkill, OwnerUID: testCaller.UID, SpaceID: stringPtr(testCaller.SpaceID), Visibility: model.PluginVisibilitySpace, IsEmbedded: true, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)},
	}}
	if _, err := fixedService(f).Update(context.Background(), testCaller, "skill-emb", validRequest()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for an embedded child", err)
	}
	if f.update != nil {
		t.Fatalf("an embedded-child update must not reach the repo: %#v", f.update)
	}
}

func TestValidRelationTypeMirrorsLibEndpointMatrix(t *testing.T) {
	valid := []struct {
		relation string
		source   model.PluginType
		target   model.PluginType
	}{
		{"expert_team_expert", model.PluginTypeExpertTeam, model.PluginTypeExpert},
		{"expert_skill", model.PluginTypeExpert, model.PluginTypeSkill},
		{"expert_connector", model.PluginTypeExpert, model.PluginTypeConnector},
	}
	for _, tt := range valid {
		if !validRelationType(tt.relation, tt.source, tt.target) {
			t.Fatalf("%s %s->%s rejected", tt.relation, tt.source, tt.target)
		}
	}
	invalid := []struct {
		relation string
		source   model.PluginType
		target   model.PluginType
	}{
		{"expert_team_member", model.PluginTypeExpertTeam, model.PluginTypeExpert}, // retired name
		{"plugin_dependency", model.PluginTypeExpert, model.PluginTypeSkill},       // dropped enum
		{"expert_skill", model.PluginTypeExpertTeam, model.PluginTypeSkill},        // teams reach skills via members
		{"expert_skill", model.PluginTypeExpert, model.PluginTypeConnector},
		{"expert_team_expert", model.PluginTypeExpert, model.PluginTypeExpert},
		{"garbage", model.PluginTypeExpert, model.PluginTypeSkill},
	}
	for _, tt := range invalid {
		if validRelationType(tt.relation, tt.source, tt.target) {
			t.Fatalf("%s %s->%s accepted", tt.relation, tt.source, tt.target)
		}
	}
}

// TestCreateStampsNewCurrentVersionID pins that a snapshotting create returns the
// new plugin_versions id on the response plugin, so the write response's
// current_version_id matches the DB (and a subsequent GET) rather than being nil.
func TestCreateStampsNewCurrentVersionID(t *testing.T) {
	f := &fakeStore{plugins: map[string]*model.Plugin{}}
	detail, err := fixedService(f).Create(context.Background(), testCaller, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if detail.Plugin.CurrentVersionID == nil || *detail.Plugin.CurrentVersionID != "ver-snap" {
		t.Fatalf("create response current_version_id = %v, want ver-snap", detail.Plugin.CurrentVersionID)
	}
}
