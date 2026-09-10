package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type fakeAdminService struct {
	caller          pluginsvc.Caller
	listType        model.PluginType
	listVis         model.PluginVisibility
	listParams      pluginsvc.ListParams
	list            []model.Plugin
	detail          *pluginsvc.Detail
	write           pluginsvc.WriteRequest
	importParams    pluginsvc.ContainerImportParams
	reuploadID      string
	skillParams     pluginsvc.ImportParams
	deletedID       string
	skillMD         string
	download        *pluginsvc.SkillPackageStream
	artifactID      string
	ratingID        string
	rating          *int
	maxArchiveBytes int64
	err             error
}

func (f *fakeAdminService) AdminList(_ context.Context, c pluginsvc.Caller, typ model.PluginType, vis model.PluginVisibility, p pluginsvc.ListParams) ([]model.Plugin, int64, error) {
	f.caller, f.listType, f.listVis, f.listParams = c, typ, vis, p
	return f.list, int64(len(f.list)), f.err
}
func (f *fakeAdminService) AdminDetail(_ context.Context, c pluginsvc.Caller, _ string, _ bool) (*pluginsvc.Detail, error) {
	f.caller = c
	return f.detail, f.err
}
func (f *fakeAdminService) AdminCreate(_ context.Context, c pluginsvc.Caller, r pluginsvc.WriteRequest) (*pluginsvc.Detail, error) {
	f.caller, f.write = c, r
	return f.detail, f.err
}
func (f *fakeAdminService) AdminImportContainer(_ context.Context, c pluginsvc.Caller, p pluginsvc.ContainerImportParams) (*pluginsvc.Detail, error) {
	f.caller, f.importParams = c, p
	return f.detail, f.err
}
func (f *fakeAdminService) AdminReuploadContainer(_ context.Context, c pluginsvc.Caller, id string, p pluginsvc.ContainerImportParams) (*pluginsvc.Detail, error) {
	f.caller, f.reuploadID, f.importParams = c, id, p
	return f.detail, f.err
}
func (f *fakeAdminService) AdminImport(_ context.Context, c pluginsvc.Caller, p pluginsvc.ImportParams) (*pluginsvc.Detail, error) {
	f.caller, f.skillParams = c, p
	return f.detail, f.err
}
func (f *fakeAdminService) AdminUpdate(_ context.Context, c pluginsvc.Caller, _ string, r pluginsvc.WriteRequest) (*pluginsvc.Detail, error) {
	f.caller, f.write = c, r
	return f.detail, f.err
}
func (f *fakeAdminService) AdminUpdateRating(_ context.Context, c pluginsvc.Caller, id string, rating *int) (*model.Plugin, error) {
	f.caller, f.ratingID, f.rating = c, id, rating
	if f.err != nil {
		return nil, f.err
	}
	return f.detail.Plugin, nil
}
func (f *fakeAdminService) AdminDelete(_ context.Context, c pluginsvc.Caller, id string) error {
	f.caller, f.deletedID = c, id
	return f.err
}
func (f *fakeAdminService) AdminSkillMarkdown(_ context.Context, c pluginsvc.Caller, id string) (string, error) {
	f.caller, f.artifactID = c, id
	return f.skillMD, f.err
}
func (f *fakeAdminService) AdminOpenSkillPackage(_ context.Context, c pluginsvc.Caller, id string) (*pluginsvc.SkillPackageStream, error) {
	f.caller, f.artifactID = c, id
	if f.err != nil {
		return nil, f.err
	}
	return f.download, nil
}

// MaxArchiveBytes returns the container upload ceiling the handler threads into
// readContainerParams; the default keeps the small test archives well under it.
func (f *fakeAdminService) MaxArchiveBytes() int64 {
	if f.maxArchiveBytes > 0 {
		return f.maxArchiveBytes
	}
	return 64 << 20
}

type fakeAdminCategories struct {
	listType   model.PluginType
	list       []model.PluginCategory
	created    *model.PluginCategory
	updated    *model.PluginCategory
	updateID   string
	writeName  string
	writeIcon  string
	writeTypes []model.PluginType
	writeSort  int
	deletedID  string
	err        error
}

func (f *fakeAdminCategories) AdminListCategories(_ context.Context, typ model.PluginType) ([]model.PluginCategory, error) {
	f.listType = typ
	return f.list, f.err
}

func (f *fakeAdminCategories) AdminCreateCategory(_ context.Context, name, iconKey string, pluginTypes []model.PluginType, sortOrder int) (*model.PluginCategory, error) {
	f.writeName, f.writeIcon, f.writeTypes, f.writeSort = name, iconKey, pluginTypes, sortOrder
	return f.created, f.err
}

func (f *fakeAdminCategories) AdminUpdateCategory(_ context.Context, id, name, iconKey string, pluginTypes []model.PluginType, sortOrder int) (*model.PluginCategory, error) {
	f.updateID, f.writeName, f.writeIcon, f.writeTypes, f.writeSort = id, name, iconKey, pluginTypes, sortOrder
	return f.updated, f.err
}

func (f *fakeAdminCategories) AdminDeleteCategory(_ context.Context, id string) error {
	f.deletedID = id
	return f.err
}

// adminTestEngine mounts the admin surface behind the dev admin authenticator,
// which stamps a fixed admin identity — the same trusted path production uses to
// admit marketAdmin/superAdmin, so no caller-supplied identity is trusted here.
func adminTestEngine(svc AdminService, cats AdminCategoryService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(logging.RequestID())
	adminAuth := marketmiddleware.NewAdminAuthenticator(false, nil, model.Identity{UID: "admin-1", Name: "Root"})
	NewAdmin(svc, cats).RegisterAdmin(r, adminAuth)
	return r
}

func TestAdminImportContainerForwardsArchiveAndAdminCaller(t *testing.T) {
	space := adminSpaceForTest()
	f := &fakeAdminService{detail: &pluginsvc.Detail{
		Plugin:    &model.Plugin{ID: "expert-1", Name: "E", Type: model.PluginTypeExpert, SpaceID: &space, Visibility: model.PluginVisibilityPublic, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), CreatedAt: time.Now(), UpdatedAt: time.Now()},
		Relations: []model.PluginRelation{},
	}}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "container.zip")
	part.Write([]byte("PK-fake-container-bytes"))
	_ = mw.WriteField("category_id", "cat-9")
	mw.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !f.caller.IsSystemAdmin || f.caller.UID != "admin-1" {
		t.Fatalf("import did not stamp a system-admin caller: %#v", f.caller)
	}
	if string(f.importParams.Archive) != "PK-fake-container-bytes" {
		t.Fatalf("archive not forwarded: %q", f.importParams.Archive)
	}
	if f.importParams.CategoryID == nil || *f.importParams.CategoryID != "cat-9" {
		t.Fatalf("category_id not forwarded: %#v", f.importParams.CategoryID)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"plugin_id":"expert-1"`)) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestAdminReuploadContainerForwardsPluginIDArchiveAndAdminCaller(t *testing.T) {
	space := adminSpaceForTest()
	f := &fakeAdminService{detail: &pluginsvc.Detail{
		Plugin:    &model.Plugin{ID: "expert-9", Name: "E", Type: model.PluginTypeExpert, SpaceID: &space, Visibility: model.PluginVisibilityPublic, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), CreatedAt: time.Now(), UpdatedAt: time.Now()},
		Relations: []model.PluginRelation{},
	}}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "container.zip")
	part.Write([]byte("PK-new-container-bytes"))
	_ = mw.WriteField("category_id", "cat-3")
	mw.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/container_reupload/expert-9", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !f.caller.IsSystemAdmin || f.caller.UID != "admin-1" {
		t.Fatalf("reupload did not stamp a system-admin caller: %#v", f.caller)
	}
	if f.reuploadID != "expert-9" {
		t.Fatalf("plugin_id from path not forwarded: %q", f.reuploadID)
	}
	if string(f.importParams.Archive) != "PK-new-container-bytes" {
		t.Fatalf("archive not forwarded: %q", f.importParams.Archive)
	}
	if f.importParams.CategoryID == nil || *f.importParams.CategoryID != "cat-3" {
		t.Fatalf("category_id not forwarded: %#v", f.importParams.CategoryID)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"plugin_id":"expert-9"`)) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

// stubRoleResolver resolves any non-empty token to a fixed-role identity, so a
// role-gate test can drive the real AdminAuthenticator.
type stubRoleResolver struct{ role string }

func (r stubRoleResolver) Resolve(_ context.Context, _ string) (model.Identity, error) {
	return model.Identity{UID: "u-1", Name: "U", Role: r.role, ContextIncluded: true}, nil
}

// reachAdminService records whether any admin operation was invoked; the
// role-gate test asserts it stays false when the gate rejects the caller.
type reachAdminService struct {
	fakeAdminService
	reached bool
}

func (f *reachAdminService) AdminReuploadContainer(ctx context.Context, c pluginsvc.Caller, id string, p pluginsvc.ContainerImportParams) (*pluginsvc.Detail, error) {
	f.reached = true
	return f.fakeAdminService.AdminReuploadContainer(ctx, c, id, p)
}

// TestAdminReuploadContainerRejectsNonAdminRole pins the container_reupload route
// behind the same RoleMarketAdmin gate as the rest of the admin plugin surface:
// a resolved identity without the role is refused with 403 FORBIDDEN and never
// reaches the service.
func TestAdminReuploadContainerRejectsNonAdminRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(logging.RequestID())
	adminAuth := marketmiddleware.NewAdminAuthenticator(true, stubRoleResolver{role: "user"}, model.Identity{})
	f := &reachAdminService{}
	NewAdmin(f, &fakeAdminCategories{}).RegisterAdmin(r, adminAuth)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "container.zip")
	part.Write([]byte("PK"))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/container_reupload/expert-9", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Token", "some-user-session")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte("FORBIDDEN")) {
		t.Fatalf("non-admin status=%d body=%s, want 403 FORBIDDEN", rec.Code, rec.Body.String())
	}
	if f.reached {
		t.Fatal("a non-admin role must not reach the reupload service")
	}
}

func TestAdminImportContainerRequiresFile(t *testing.T) {
	f := &fakeAdminService{}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("category_id", "cat-9")
	mw.Close()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"VALIDATION_ERROR"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func adminSpaceForTest() string { return "" }

func TestAdminSkillImportForwardsParamsAndAdminCaller(t *testing.T) {
	space := adminSpaceForTest()
	f := &fakeAdminService{detail: &pluginsvc.Detail{
		Plugin:    &model.Plugin{ID: "skill-1", Name: "Ops", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilityPublic, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), CreatedAt: time.Now(), UpdatedAt: time.Now()},
		Relations: []model.PluginRelation{},
	}}
	body := []byte(`{"parse_task_id":"task-1","name":"Ops","category_id":"cat-ops","tags":["deploy"],"version":"1.2.0","description":"An ops skill."}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/skill_import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !f.caller.IsSystemAdmin || f.caller.UID != "admin-1" {
		t.Fatalf("import did not stamp a system-admin caller: %#v", f.caller)
	}
	if f.skillParams.ParseTaskID != "task-1" || f.skillParams.PluginID != "" || f.skillParams.Name != "Ops" || f.skillParams.Version != "1.2.0" {
		t.Fatalf("params not forwarded: %#v", f.skillParams)
	}
	if f.skillParams.CategoryID == nil || *f.skillParams.CategoryID != "cat-ops" {
		t.Fatalf("category not forwarded: %#v", f.skillParams.CategoryID)
	}
	if len(f.skillParams.Tags) != 1 || f.skillParams.Tags[0] != "deploy" {
		t.Fatalf("tags not forwarded: %#v", f.skillParams.Tags)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"plugin_id":"skill-1"`)) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestAdminSkillReuploadForwardsPluginIDFromPath(t *testing.T) {
	space := adminSpaceForTest()
	f := &fakeAdminService{detail: &pluginsvc.Detail{
		Plugin:    &model.Plugin{ID: "skill-9", Name: "Ops", Type: model.PluginTypeSkill, SpaceID: &space, Visibility: model.PluginVisibilityPublic, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), CreatedAt: time.Now(), UpdatedAt: time.Now()},
		Relations: []model.PluginRelation{},
	}}
	body := []byte(`{"parse_task_id":"task-2","version":"2.0.0","changelog":"bump"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/skill_reupload/skill-9", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.skillParams.PluginID != "skill-9" || f.skillParams.ParseTaskID != "task-2" || f.skillParams.Version != "2.0.0" {
		t.Fatalf("reupload params not forwarded: %#v", f.skillParams)
	}
	if f.skillParams.Changelog == nil || *f.skillParams.Changelog != "bump" {
		t.Fatalf("changelog not forwarded: %#v", f.skillParams.Changelog)
	}
}

func TestAdminSkillImportConflictIs409(t *testing.T) {
	f := &fakeAdminService{err: pluginsvc.ErrConflict}
	body := []byte(`{"parse_task_id":"task-1","name":"Ops","version":"1.0.0"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/skill_import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"CONFLICT"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminListPassesVisibilityAndSystemAdminCaller(t *testing.T) {
	space := "space-x"
	f := &fakeAdminService{list: []model.Plugin{{ID: "plugin-1", Name: "Sys", Type: model.PluginTypeConnector, SpaceID: &space, Visibility: model.PluginVisibilitySystem, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), CreatedAt: time.Now(), UpdatedAt: time.Now()}}}
	rec := httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins?plugin_type=connector&visibility=system&q=foo", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !f.caller.IsSystemAdmin || f.caller.UID != "admin-1" {
		t.Fatalf("caller=%#v want system admin admin-1", f.caller)
	}
	if f.caller.SpaceID != "" {
		t.Fatalf("admin caller must carry no Space, got %q", f.caller.SpaceID)
	}
	if f.listType != model.PluginTypeConnector || f.listVis != model.PluginVisibilitySystem || f.listParams.Keyword != "foo" {
		t.Fatalf("type=%q vis=%q kw=%q", f.listType, f.listVis, f.listParams.Keyword)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"plugin_id":"plugin-1"`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"data"`)) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestAdminListRequiresPluginType(t *testing.T) {
	rec := httptest.NewRecorder()
	adminTestEngine(&fakeAdminService{}, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins", nil))
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"VALIDATION_ERROR"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminGetNotFoundIsSanitized(t *testing.T) {
	f := &fakeAdminService{err: pluginsvc.ErrNotFound}
	rec := httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/other", nil))
	if rec.Code != http.StatusNotFound || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"NOT_FOUND"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("space")) {
		t.Fatalf("existence leaked: %s", rec.Body.String())
	}
}

func TestAdminCreateStampsAdminCallerAndSanitizesInternalError(t *testing.T) {
	f := &fakeAdminService{err: errors.New("sql: secret DSN")}
	body := []byte(`{"plugin":{"plugin_name":"Sys","plugin_type":"connector","visibility":"system","tags":[],"manifest_json":{},"plugin_json":{}},"relations":[]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || bytes.Contains(rec.Body.Bytes(), []byte("secret DSN")) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !f.caller.IsSystemAdmin {
		t.Fatalf("create did not stamp a system-admin caller: %#v", f.caller)
	}
}

func TestAdminDeleteReturnsPluginID(t *testing.T) {
	f := &fakeAdminService{}
	rec := httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/plugins/plugin-9", nil))
	if rec.Code != http.StatusOK || f.deletedID != "plugin-9" {
		t.Fatalf("status=%d deletedID=%q body=%s", rec.Code, f.deletedID, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"plugin_id":"plugin-9"`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"deleted":true`)) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

// TestAdminSkillMarkdownReturnsContent proves the admin skill_md route admits a
// marketAdmin (not 403/404), forwards the plugin_id, and renders the SKILL.md as
// { "data": { "content": ... } }.
func TestAdminSkillMarkdownReturnsContent(t *testing.T) {
	f := &fakeAdminService{skillMD: "# Ops Skill\nbody"}
	rec := httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/skill-1/skill_md", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !f.caller.IsSystemAdmin || f.artifactID != "skill-1" {
		t.Fatalf("caller=%#v artifactID=%q", f.caller, f.artifactID)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"content":"# Ops Skill\nbody"`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"data"`)) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestAdminSkillMarkdownNotFoundIs404(t *testing.T) {
	f := &fakeAdminService{err: pluginsvc.ErrNotFound}
	rec := httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/other/skill_md", nil))
	if rec.Code != http.StatusNotFound || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"NOT_FOUND"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminDownloadStreamsZip proves the admin download route admits a marketAdmin,
// forwards the plugin_id, streams the reconstructed zip, and sets the zip content
// headers (application/zip + nosniff).
func TestAdminDownloadStreamsZip(t *testing.T) {
	f := &fakeAdminService{download: &pluginsvc.SkillPackageStream{FileName: "Ops.zip", Write: func(w io.Writer) error {
		_, err := w.Write([]byte("PK-zip-bytes"))
		return err
	}}}
	rec := httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/skill-9/download", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.artifactID != "skill-9" || !f.caller.IsSystemAdmin {
		t.Fatalf("caller=%#v artifactID=%q", f.caller, f.artifactID)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type=%q, want application/zip", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff header")
	}
	if rec.Body.String() != "PK-zip-bytes" {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestAdminDownloadNotFoundIs404(t *testing.T) {
	f := &fakeAdminService{err: pluginsvc.ErrNotFound}
	rec := httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/other/download", nil))
	if rec.Code != http.StatusNotFound || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"NOT_FOUND"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminCreateCategoryForwardsFieldsAndRendersDTO(t *testing.T) {
	cats := &fakeAdminCategories{created: &model.PluginCategory{ID: "cat-new", Name: "Ops", IconKey: "k", PluginTypes: json.RawMessage(`["expert","expert_team"]`), SortOrder: 5}}
	body := []byte(`{"name":"Ops","icon_key":"k","plugin_types":["expert","expert_team"],"sort_order":5}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugin_categories", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	adminTestEngine(&fakeAdminService{}, cats).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cats.writeName != "Ops" || cats.writeIcon != "k" || cats.writeSort != 5 || len(cats.writeTypes) != 2 {
		t.Fatalf("fields not forwarded: %#v", cats)
	}
	if cats.writeTypes[0] != model.PluginTypeExpert || cats.writeTypes[1] != model.PluginTypeExpertTeam {
		t.Fatalf("plugin_types not forwarded: %#v", cats.writeTypes)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"category_id":"cat-new"`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"plugin_types":["expert","expert_team"]`)) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestAdminCreateCategoryValidationErrorIs400(t *testing.T) {
	cats := &fakeAdminCategories{err: pluginsvc.ErrInvalidRequest}
	body := []byte(`{"name":"","plugin_types":[],"sort_order":0}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugin_categories", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	adminTestEngine(&fakeAdminService{}, cats).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"VALIDATION_ERROR"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminUpdateCategoryForwardsIDAndNotFoundIs404(t *testing.T) {
	cats := &fakeAdminCategories{err: pluginsvc.ErrNotFound}
	body := []byte(`{"name":"Ops","icon_key":"k","plugin_types":["expert"],"sort_order":1}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/plugin_categories/cat-1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	adminTestEngine(&fakeAdminService{}, cats).ServeHTTP(rec, req)
	if cats.updateID != "cat-1" {
		t.Fatalf("id not forwarded: %q", cats.updateID)
	}
	if rec.Code != http.StatusNotFound || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"NOT_FOUND"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminDeleteCategoryReturnsEmptyData(t *testing.T) {
	cats := &fakeAdminCategories{}
	rec := httptest.NewRecorder()
	adminTestEngine(&fakeAdminService{}, cats).ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/plugin_categories/cat-1", nil))
	if rec.Code != http.StatusOK || cats.deletedID != "cat-1" {
		t.Fatalf("status=%d deletedID=%q body=%s", rec.Code, cats.deletedID, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"data"`)) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestAdminDeleteCategoryInUseIsConflict(t *testing.T) {
	cats := &fakeAdminCategories{err: pluginsvc.ErrConflict}
	rec := httptest.NewRecorder()
	adminTestEngine(&fakeAdminService{}, cats).ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/plugin_categories/cat-1", nil))
	if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"CONFLICT"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminRoutesAreRoleGated proves the admin surface is behind the admin
// authenticator: with auth enabled and no token, every route is refused (Q10').
func TestAdminRoutesAreRoleGated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(logging.RequestID())
	// Enabled authenticator with a resolver that admits nothing → no token = 401.
	adminAuth := marketmiddleware.NewAdminAuthenticator(true, denyResolver{}, model.Identity{})
	NewAdmin(&fakeAdminService{}, &fakeAdminCategories{}).RegisterAdmin(r, adminAuth)

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/admin/plugins?plugin_type=skill"},
		{http.MethodPost, "/api/v1/admin/plugins"},
		{http.MethodPost, "/api/v1/admin/plugins/import"},
		{http.MethodPost, "/api/v1/admin/plugins/skill_import"},
		{http.MethodPost, "/api/v1/admin/plugins/skill_reupload/p1"},
		{http.MethodGet, "/api/v1/admin/plugins/p1"},
		{http.MethodGet, "/api/v1/admin/plugins/p1/skill_md"},
		{http.MethodGet, "/api/v1/admin/plugins/p1/download"},
		{http.MethodPatch, "/api/v1/admin/plugins/p1"},
		{http.MethodDelete, "/api/v1/admin/plugins/p1"},
		{http.MethodGet, "/api/v1/admin/plugin_categories?plugin_type=skill"},
		{http.MethodPost, "/api/v1/admin/plugin_categories"},
		{http.MethodPatch, "/api/v1/admin/plugin_categories/cat-1"},
		{http.MethodDelete, "/api/v1/admin/plugin_categories/cat-1"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d, want 401 (route not gated)", tc.method, tc.path, rec.Code)
		}
	}
}

type denyResolver struct{}

func (denyResolver) Resolve(context.Context, string) (model.Identity, error) {
	return model.Identity{}, nil
}

func TestAdminListCategoriesRendersSnakeCaseDTO(t *testing.T) {
	cats := &fakeAdminCategories{list: []model.PluginCategory{{ID: "cat-1", Name: "Ops", IconKey: "k", PluginTypes: json.RawMessage(`["connector"]`), SortOrder: 2, PluginCount: 3}}}
	rec := httptest.NewRecorder()
	adminTestEngine(&fakeAdminService{}, cats).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugin_categories?plugin_type=connector", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cats.listType != model.PluginTypeConnector {
		t.Fatalf("plugin_type not forwarded: %q", cats.listType)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"category_id":"cat-1"`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"plugin_count":3`)) {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"createdAt"`)) || bytes.Contains(rec.Body.Bytes(), []byte(`"iconKey"`)) {
		t.Fatalf("raw model camelCase leaked: %s", rec.Body.String())
	}
}
