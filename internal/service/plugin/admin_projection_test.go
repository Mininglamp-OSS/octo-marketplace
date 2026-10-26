package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func projectionPtr[T any](v T) *T { return &v }

var adminSystemCaller = Caller{UID: adminCaller.UID, Name: adminCaller.Name, RequestID: adminCaller.RequestID, IsSystemAdmin: true}

// adminProjectionFixture adds the optional A21 read surfaces to the standard
// service fake: one batched version/release metadata read, one batched Connector
// editing digest read, and the effective-content read.
type adminProjectionFixture struct {
	*fakeStore
	metas          map[string]pluginrepo.PluginReadMeta
	digests        map[string]pluginrepo.ConnectorContentDigests
	effective      map[string]*model.Plugin
	effectiveCalls []string
}

func (f *adminProjectionFixture) ListPluginReadMetas(_ context.Context, _ pluginrepo.Scope, ids []string) (map[string]pluginrepo.PluginReadMeta, error) {
	out := map[string]pluginrepo.PluginReadMeta{}
	for _, id := range ids {
		if meta, ok := f.metas[id]; ok {
			out[id] = meta
		}
	}
	return out, nil
}

func (f *adminProjectionFixture) ListConnectorContentDigests(_ context.Context, _ pluginrepo.Scope, ids []string) (map[string]pluginrepo.ConnectorContentDigests, error) {
	out := map[string]pluginrepo.ConnectorContentDigests{}
	for _, id := range ids {
		if digest, ok := f.digests[id]; ok {
			out[id] = digest
		}
	}
	return out, nil
}

func (f *adminProjectionFixture) GetConnectorEffective(_ context.Context, _ pluginrepo.Scope, id string) (*model.Plugin, error) {
	f.effectiveCalls = append(f.effectiveCalls, id)
	p := f.effective[id]
	if p == nil {
		return nil, pluginrepo.ErrVersionBaselineUnavailable
	}
	copy := *p
	return &copy, nil
}

func newAdminProjectionService(f *adminProjectionFixture) *Service {
	return New(f)
}

func projectionPlugin(typ model.PluginType, current, published, label string, revision uint64) model.Plugin {
	space := testCaller.SpaceID
	p := model.Plugin{
		ID: "plugin-1", Name: "Plugin", Type: typ, SpaceID: &space,
		OwnerUID: testCaller.UID, Visibility: model.PluginVisibilitySpace,
		ListingState: model.PluginListingStatePublished, Status: 1,
		Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`),
		EditRevision: 4,
	}
	if current != "" {
		p.CurrentVersionID = projectionPtr(current)
	}
	if label != "" {
		p.CurrentVersion = projectionPtr(label)
	}
	if published != "" {
		p.PublishedVersionID = projectionPtr(published)
	}
	if revision > 0 {
		p.RevisionNo = projectionPtr(revision)
	}
	return p
}

func TestAdminListDefaultsToEditingProjection(t *testing.T) {
	current, published, label := "v2", "v1", "2.0.0"
	item := projectionPlugin(model.PluginTypeSkill, current, "", label, 0)
	meta := pluginrepo.PluginReadMeta{
		CurrentVersionID:    &current,
		CurrentVersionLabel: &label,
		CurrentRevisionNo:   projectionPtr(uint64(2)),
		PublishedVersionID:  &published,
	}
	f := &adminProjectionFixture{
		fakeStore: &fakeStore{list: []model.Plugin{item}},
		metas:     map[string]pluginrepo.PluginReadMeta{"plugin-1": meta},
	}
	items, total, err := newAdminProjectionService(f).AdminList(context.Background(), adminCaller, model.PluginTypeSkill, "", ListParams{})
	if err != nil {
		t.Fatalf("AdminList: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("items=%d total=%d", len(items), total)
	}
	got := items[0]
	if got.View != model.PluginViewEditing {
		t.Fatalf("view=%q want editing", got.View)
	}
	if got.SelectedVersionID == nil || *got.SelectedVersionID != current {
		t.Fatalf("selected_version_id=%v want %s", got.SelectedVersionID, current)
	}
	if got.CurrentVersionID == nil || *got.CurrentVersionID != current {
		t.Fatalf("current_version_id=%v want %s", got.CurrentVersionID, current)
	}
	if got.VersionLabel == nil || *got.VersionLabel != label {
		t.Fatalf("version_label=%v want %s", got.VersionLabel, label)
	}
	if got.RevisionNo == nil || *got.RevisionNo != 2 {
		t.Fatalf("revision_no=%v want 2", got.RevisionNo)
	}
	if !got.HasUnpublishedChanges {
		t.Fatal("current v2 != published v1 must report unpublished changes")
	}
	if !got.IsPublished {
		t.Fatal("published_version_id set must report is_published")
	}
	if got.DeclaredVisibility != model.PluginVisibilitySpace {
		t.Fatalf("declared_visibility=%q want space", got.DeclaredVisibility)
	}
	if got.PublishedVersionID == nil || *got.PublishedVersionID != published {
		t.Fatalf("published pointer not retained internally: %v", got.PublishedVersionID)
	}
	if !f.listFilter.AllSpaces {
		t.Fatalf("admin list must keep AllSpaces: %#v", f.listFilter)
	}
}

func TestAdminListRejectsNonEditingView(t *testing.T) {
	for _, view := range []string{"published", "effective", "bogus"} {
		t.Run(view, func(t *testing.T) {
			f := &adminProjectionFixture{fakeStore: &fakeStore{}}
			_, _, err := newAdminProjectionService(f).AdminList(context.Background(), adminCaller, model.PluginTypeSkill, "", ListParams{View: view})
			var fieldErr *ReviewFieldError
			if !errors.As(err, &fieldErr) || fieldErr.Field != "view" || fieldErr.Reason != "view_invalid" {
				t.Fatalf("err=%v want view/view_invalid", err)
			}
		})
	}
}

func TestAdminListConnectorUsesCompleteEditingDigest(t *testing.T) {
	editDigest := "sha256:editing"
	effectiveDigest := "sha256:effective"
	item := projectionPlugin(model.PluginTypeConnector, "", "", "", 0)
	// The summary row deliberately has no package; the digest must come from the
	// batched complete-payload read, not the summary row.
	item.Package = nil
	revision := uint64(3)
	meta := pluginrepo.PluginReadMeta{EffectiveRevision: &revision, EffectiveDigest: &effectiveDigest}
	f := &adminProjectionFixture{
		fakeStore: &fakeStore{list: []model.Plugin{item}},
		metas:     map[string]pluginrepo.PluginReadMeta{"plugin-1": meta},
		digests:   map[string]pluginrepo.ConnectorContentDigests{"plugin-1": {EditingDigest: editDigest, EffectiveDigest: &effectiveDigest}},
	}
	items, _, err := newAdminProjectionService(f).AdminList(context.Background(), adminCaller, model.PluginTypeConnector, "", ListParams{})
	if err != nil {
		t.Fatalf("AdminList: %v", err)
	}
	got := items[0]
	if got.View != model.PluginViewEditing {
		t.Fatalf("view=%q want editing", got.View)
	}
	if got.CurrentVersionID != nil || got.SelectedVersionID != nil || got.VersionLabel != nil || got.RevisionNo != nil {
		t.Fatalf("connector must omit version identity: %#v", got)
	}
	if got.EffectiveRevision == nil || *got.EffectiveRevision != revision {
		t.Fatalf("effective_revision=%v want %d", got.EffectiveRevision, revision)
	}
	if got.EffectiveDigest == nil || *got.EffectiveDigest != effectiveDigest {
		t.Fatalf("effective_digest=%v want %s", got.EffectiveDigest, effectiveDigest)
	}
	if !got.IsPublished {
		t.Fatal("connector with an effective row must report is_published")
	}
	if !got.HasUnpublishedChanges {
		t.Fatal("editing digest differs from effective digest must report a change")
	}

	f.digests["plugin-1"] = pluginrepo.ConnectorContentDigests{EditingDigest: effectiveDigest, EffectiveDigest: &effectiveDigest}
	items, _, err = newAdminProjectionService(f).AdminList(context.Background(), adminCaller, model.PluginTypeConnector, "", ListParams{})
	if err != nil {
		t.Fatalf("AdminList (matching digest): %v", err)
	}
	if items[0].HasUnpublishedChanges {
		t.Fatal("matching complete digests must not report unpublished changes")
	}
}

func TestAdminDetailVersionedEditingProjection(t *testing.T) {
	current, published, label := "v3", "v2", "3.0.0"
	item := projectionPlugin(model.PluginTypeExpert, current, "", label, 0)
	meta := pluginrepo.PluginReadMeta{
		CurrentVersionID:    &current,
		CurrentVersionLabel: &label,
		CurrentRevisionNo:   projectionPtr(uint64(3)),
		PublishedVersionID:  &published,
	}
	editingDigest, differentEffective := "sha256:editing", "sha256:effective"
	f := &adminProjectionFixture{
		fakeStore: &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": &item}},
		metas:     map[string]pluginrepo.PluginReadMeta{"plugin-1": meta},
		digests:   map[string]pluginrepo.ConnectorContentDigests{"plugin-1": {EditingDigest: editingDigest, EffectiveDigest: &differentEffective}},
	}
	d, err := newAdminProjectionService(f).AdminDetail(context.Background(), adminCaller, "plugin-1", true, "")
	if err != nil {
		t.Fatalf("AdminDetail: %v", err)
	}
	if d.Plugin.View != model.PluginViewEditing ||
		d.Plugin.SelectedVersionID == nil || *d.Plugin.SelectedVersionID != current ||
		d.Plugin.VersionLabel == nil || *d.Plugin.VersionLabel != label ||
		d.Plugin.RevisionNo == nil || *d.Plugin.RevisionNo != 3 ||
		!d.Plugin.HasUnpublishedChanges || !d.Plugin.IsPublished {
		t.Fatalf("unexpected editing projection: %#v", d.Plugin)
	}
}

func TestAdminDetailConnectorEffectiveUsesEffectiveContent(t *testing.T) {
	effectiveDigest := "sha256:effective-content"
	revision := uint64(7)
	editing := projectionPlugin(model.PluginTypeConnector, "", "", "", 0)
	editing.Package = json.RawMessage(`{"attachments":[{"path":"mcp.json","content_type":"raw","raw_content":"{\"editing\":true}"}]}`)
	effective := editing
	effective.Package = json.RawMessage(`{"attachments":[{"path":"mcp.json","content_type":"raw","raw_content":"{\"effective\":true}"}]}`)
	effective.EffectiveRevision = &revision
	effective.EffectiveDigest = &effectiveDigest
	effective.Visibility = model.PluginVisibilitySystem
	f := &adminProjectionFixture{
		fakeStore: &fakeStore{
			plugins:   map[string]*model.Plugin{"plugin-1": &editing},
			relations: map[string][]model.PluginRelation{"plugin-1": {{SourcePluginID: "plugin-1", TargetPluginID: "target", TargetPluginType: model.PluginTypeConnector, Type: "expert_connector"}}},
		},
		metas:     map[string]pluginrepo.PluginReadMeta{"plugin-1": {EffectiveRevision: &revision, EffectiveDigest: &effectiveDigest}},
		effective: map[string]*model.Plugin{"plugin-1": &effective},
	}
	d, err := newAdminProjectionService(f).AdminDetail(context.Background(), adminCaller, "plugin-1", true, "effective")
	if err != nil {
		t.Fatalf("AdminDetail effective: %v", err)
	}
	got := d.Plugin
	if got.View != model.PluginViewEffective {
		t.Fatalf("view=%q want effective", got.View)
	}
	if string(got.Package) != string(effective.Package) {
		t.Fatalf("effective view served editing content: %s", got.Package)
	}
	if got.SelectedVersionID != nil || got.VersionLabel != nil || got.RevisionNo != nil || got.CurrentVersionID != nil {
		t.Fatalf("connector effective must omit version identity: %#v", got)
	}
	if got.EffectiveRevision == nil || *got.EffectiveRevision != revision ||
		got.EffectiveDigest == nil || *got.EffectiveDigest != effectiveDigest {
		t.Fatalf("effective metadata missing: %#v", got)
	}
	if !got.IsPublished {
		t.Fatal("effective connector must report is_published")
	}
	if !got.HasUnpublishedChanges {
		t.Fatal("effective payload differs from editing content must report a change")
	}
	if got.DeclaredVisibility != model.PluginVisibilitySystem {
		t.Fatalf("declared_visibility=%q want effective system audience", got.DeclaredVisibility)
	}
	if len(f.effectiveCalls) != 1 || f.effectiveCalls[0] != "plugin-1" {
		t.Fatalf("effective read calls=%v", f.effectiveCalls)
	}
	if len(d.Relations) != 1 || d.Relations[0].SourcePluginType != model.PluginTypeConnector {
		t.Fatalf("relations lost or mis-stamped: %#v", d.Relations)
	}
}

func TestAdminDetailRejectsEffectiveForVersionedAndPublishedView(t *testing.T) {
	item := projectionPlugin(model.PluginTypeSkill, "v1", "v1", "1.0.0", 1)
	f := &adminProjectionFixture{
		fakeStore: &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": &item}},
		effective: map[string]*model.Plugin{"plugin-1": &item},
	}
	for _, view := range []string{"effective", "published", "bogus"} {
		t.Run(view, func(t *testing.T) {
			_, err := newAdminProjectionService(f).AdminDetail(context.Background(), adminCaller, "plugin-1", true, view)
			var fieldErr *ReviewFieldError
			if !errors.As(err, &fieldErr) || fieldErr.Field != "view" || fieldErr.Reason != "view_invalid" {
				t.Fatalf("err=%v want view/view_invalid", err)
			}
		})
	}
	if len(f.effectiveCalls) != 0 {
		t.Fatalf("effective must not be read for a versioned resource: %v", f.effectiveCalls)
	}
}

func TestAdminDetailConnectorEditingReportsEffectiveMetadata(t *testing.T) {
	revision := uint64(4)
	effectiveDigest := "sha256:old-effective"
	item := projectionPlugin(model.PluginTypeConnector, "", "", "", 0)
	meta := pluginrepo.PluginReadMeta{EffectiveRevision: &revision, EffectiveDigest: &effectiveDigest}
	editingDigest, differentEffective := "sha256:editing", "sha256:effective"
	f := &adminProjectionFixture{
		fakeStore: &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": &item}},
		metas:     map[string]pluginrepo.PluginReadMeta{"plugin-1": meta},
		digests:   map[string]pluginrepo.ConnectorContentDigests{"plugin-1": {EditingDigest: editingDigest, EffectiveDigest: &differentEffective}},
	}
	d, err := newAdminProjectionService(f).AdminDetail(context.Background(), adminCaller, "plugin-1", true, "")
	if err != nil {
		t.Fatalf("AdminDetail: %v", err)
	}
	if d.Plugin.View != model.PluginViewEditing {
		t.Fatalf("view=%q want editing", d.Plugin.View)
	}
	if d.Plugin.EffectiveRevision == nil || *d.Plugin.EffectiveRevision != revision {
		t.Fatalf("effective revision not exposed on editing detail: %v", d.Plugin.EffectiveRevision)
	}
	if !d.Plugin.HasUnpublishedChanges {
		t.Fatal("editing connector with a different effective payload must report a change")
	}
}

type adminSkillVersionFixture struct {
	*adminProjectionFixture
	versions    map[string]*model.PluginVersion
	versionArgs []string
}

func (f *adminSkillVersionFixture) GetVersionState(context.Context, pluginrepo.Scope, string) (*model.PluginVersionState, error) {
	return &model.PluginVersionState{}, nil
}

func (f *adminSkillVersionFixture) GetManagedVersion(_ context.Context, _ pluginrepo.Scope, _ string, versionID string) (*model.PluginVersion, error) {
	f.versionArgs = append(f.versionArgs, versionID)
	v := f.versions[versionID]
	if v == nil {
		return nil, pluginrepo.ErrNotFound
	}
	return v, nil
}

func (f *adminSkillVersionFixture) ListManagedVersions(context.Context, pluginrepo.Scope, string, int, int) ([]model.PluginVersion, int64, error) {
	return nil, 0, ErrInvalidRequest
}

func (f *adminSkillVersionFixture) PublishManaged(context.Context, pluginrepo.Scope, pluginrepo.PublishParams) (*model.Plugin, error) {
	return nil, ErrInvalidRequest
}

func (f *adminSkillVersionFixture) AdoptPublishedBaseline(context.Context, pluginrepo.Scope, pluginrepo.AdoptParams) (*model.Plugin, error) {
	return nil, ErrInvalidRequest
}

func skillVersion(id, content string) *model.PluginVersion {
	label := "1.0.0"
	pkg, _ := json.Marshal(map[string]any{"attachments": []any{map[string]any{
		"path": "SKILL.md", "content_type": "raw", "mime_type": "text/markdown", "raw_content": content,
	}}})
	return &model.PluginVersion{
		ID: id, PluginID: "plugin-1", PluginType: model.PluginTypeSkill,
		Version: "1", DeclaredVersion: &label, Manifest: json.RawMessage(`{}`),
		Package: pkg, Relations: json.RawMessage(`[]`),
		Release: &model.PluginVersionRelease{PluginID: "plugin-1", VersionID: id, VersionLabel: label},
	}
}

func newAdminSkillVersionService(f *adminSkillVersionFixture) *Service {
	return New(f).WithVersionManagement(true)
}

func TestAdminSkillMarkdownDefaultsToCurrentAndReportsVersion(t *testing.T) {
	current, old := "v-current", "v-old"
	plugin := projectionPlugin(model.PluginTypeSkill, current, "", "1.0.0", 0)
	plugin.Package = json.RawMessage(`{"attachments":[{"path":"SKILL.md","content_type":"raw","raw_content":"editing row must not be served"}]}`)
	f := &adminSkillVersionFixture{
		adminProjectionFixture: &adminProjectionFixture{fakeStore: &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": &plugin}}},
		versions: map[string]*model.PluginVersion{
			current: skillVersion(current, "current content"),
			old:     skillVersion(old, "historical content"),
		},
	}
	svc := newAdminSkillVersionService(f)
	result, err := svc.AdminSkillMarkdown(context.Background(), adminSystemCaller, "plugin-1", "")
	if err != nil {
		t.Fatalf("AdminSkillMarkdown: %v", err)
	}
	if result.Content != "current content" || result.VersionID != current {
		t.Fatalf("result=%+v want current snapshot", result)
	}
	if len(f.versionArgs) != 1 || f.versionArgs[0] != current {
		t.Fatalf("version args=%v want [%s]", f.versionArgs, current)
	}

	// An explicit version must win over current and over published.
	result, err = svc.AdminSkillMarkdown(context.Background(), adminSystemCaller, "plugin-1", old)
	if err != nil {
		t.Fatalf("AdminSkillMarkdown explicit: %v", err)
	}
	if result.Content != "historical content" || result.VersionID != old {
		t.Fatalf("result=%+v want explicit historical snapshot", result)
	}
	if f.versionArgs[len(f.versionArgs)-1] != old {
		t.Fatalf("explicit version not forwarded: %v", f.versionArgs)
	}
}

func TestAdminSkillPackageReportsActualVersion(t *testing.T) {
	current := "v-current"
	plugin := projectionPlugin(model.PluginTypeSkill, current, "", "1.0.0", 0)
	f := &adminSkillVersionFixture{
		adminProjectionFixture: &adminProjectionFixture{fakeStore: &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": &plugin}}},
		versions:               map[string]*model.PluginVersion{current: skillVersion(current, "current content")},
	}
	stream, err := newAdminSkillVersionService(f).AdminOpenSkillPackage(context.Background(), adminSystemCaller, "plugin-1", "")
	if err != nil {
		t.Fatalf("AdminOpenSkillPackage: %v", err)
	}
	if stream.VersionID != current {
		t.Fatalf("stream version=%q want %s", stream.VersionID, current)
	}
	if stream.FileName != "Plugin.zip" {
		t.Fatalf("filename=%q", stream.FileName)
	}
	if err := stream.Write(io.Discard); err != nil {
		t.Fatalf("write zip: %v", err)
	}
}

func TestAdminSkillMarkdownRequiresCurrentSnapshotWhenVersioned(t *testing.T) {
	plugin := projectionPlugin(model.PluginTypeSkill, "", "", "1.0.0", 0)
	f := &adminSkillVersionFixture{
		adminProjectionFixture: &adminProjectionFixture{fakeStore: &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": &plugin}}},
		versions:               map[string]*model.PluginVersion{},
	}
	if _, err := newAdminSkillVersionService(f).AdminSkillMarkdown(context.Background(), adminSystemCaller, "plugin-1", ""); !errors.Is(err, ErrVersionBaselineUnavailable) {
		t.Fatalf("err=%v want ErrVersionBaselineUnavailable", err)
	}
}

func TestAdminVersionReadsRejectConnectorAsUnsupportedType(t *testing.T) {
	connector := projectionPlugin(model.PluginTypeConnector, "", "", "", 0)
	f := &adminSkillVersionFixture{
		adminProjectionFixture: &adminProjectionFixture{fakeStore: &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": &connector}}},
		versions:               map[string]*model.PluginVersion{},
	}
	svc := newAdminSkillVersionService(f)
	for name, call := range map[string]func() error{
		"a22": func() error {
			_, err := svc.ManagedVersion(context.Background(), adminSystemCaller, "plugin-1", "v1", true)
			return err
		},
		"a23": func() error {
			_, err := svc.AdminSkillMarkdown(context.Background(), adminSystemCaller, "plugin-1", "")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			var fieldErr *ReviewFieldError
			if !errors.As(err, &fieldErr) || fieldErr.Field != "plugin_type" || fieldErr.Reason != "unsupported_type" {
				t.Fatalf("err=%v want plugin_type/unsupported_type", err)
			}
		})
	}
}

func TestManagedVersionUsesExactVersionID(t *testing.T) {
	plugin := projectionPlugin(model.PluginTypeSkill, "v-current", "", "1.0.0", 0)
	version := skillVersion("v-old", "old")
	f := &adminSkillVersionFixture{
		adminProjectionFixture: &adminProjectionFixture{fakeStore: &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": &plugin}}},
		versions:               map[string]*model.PluginVersion{"v-old": version},
	}
	got, err := newAdminSkillVersionService(f).ManagedVersion(context.Background(), adminSystemCaller, "plugin-1", "v-old", true)
	if err != nil {
		t.Fatalf("ManagedVersion: %v", err)
	}
	if got.ID != "v-old" || len(f.versionArgs) != 1 || f.versionArgs[0] != "v-old" {
		t.Fatalf("exact version not read: got=%q args=%v", got.ID, f.versionArgs)
	}
}
