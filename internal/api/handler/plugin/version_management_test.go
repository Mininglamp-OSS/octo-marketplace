package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

type versionServiceFake struct {
	*fakeService
	version                                  model.PluginVersion
	file                                     []byte
	versionErr                               error
	gotPlugin, gotVersion, gotLabel, gotPath string
	gotCaller                                pluginsvc.Caller
}

func (f *versionServiceFake) CurrentDetail(context.Context, pluginsvc.Caller, string) (*pluginsvc.Detail, error) {
	return f.detail, f.versionErr
}
func (f *versionServiceFake) ManagedVersion(_ context.Context, c pluginsvc.Caller, pid, vid string, _ bool) (*model.PluginVersion, error) {
	f.gotCaller = c
	f.gotPlugin = pid
	f.gotVersion = vid
	return &f.version, f.versionErr
}
func (f *versionServiceFake) HistoricalVersions(context.Context, pluginsvc.Caller, string, int, int, bool) ([]model.PluginVersion, int64, error) {
	return []model.PluginVersion{f.version}, 1, f.versionErr
}
func (f *versionServiceFake) VersionFile(_ context.Context, c pluginsvc.Caller, pid, vid, p string, _ bool) ([]byte, error) {
	f.gotCaller = c
	f.gotPlugin = pid
	f.gotVersion = vid
	f.gotPath = p
	return f.file, f.versionErr
}
func (f *versionServiceFake) AdminPublishVersion(context.Context, pluginsvc.Caller, string) (*pluginsvc.Detail, error) {
	return f.detail, f.versionErr
}

func TestHistoricalVersionWireDoesNotExposeStorageKeys(t *testing.T) {
	key := "plugins/private-space/attachments/versions/private-object"
	rawRef, _ := json.Marshal(map[string]any{"object_key": key, "zip_object_key": key, "file_url": key, "file_name": "skill.zip"})
	pkg, _ := json.Marshal(map[string]any{"attachments": []any{map[string]any{"path": "skill/ref.json", "content_type": "raw", "raw_content": string(rawRef)}, map[string]any{"path": "file.bin", "content_type": "storage", "storage_uri": key}}})
	label := "1.0.0"
	f := &versionServiceFake{fakeService: &fakeService{}, version: model.PluginVersion{ID: "version-1", PluginID: "plugin-1", DeclaredVersion: &label, Manifest: json.RawMessage(`{}`), Package: pkg, AttachmentKeys: json.RawMessage(`{"file.bin":"` + key + `"}`), Artifacts: json.RawMessage(`[{"object_key":"` + key + `"}]`), Relations: json.RawMessage(`[]`), Release: &model.PluginVersionRelease{VersionLabel: label, ReleasedBy: "owner", ReleasedAt: time.Now()}}}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/plugin-1/versions/version-1", nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{key, `"attachment_keys"`, `"artifacts"`, `"storage_uri"`, `\"object_key\"`, `\"zip_object_key\"`, `\"file_url\"`} {
		if strings.Contains(body, secret) {
			t.Fatalf("private location exposed: %s", body)
		}
	}
	if !strings.Contains(body, `"declared_version":"1.0.0"`) || !strings.Contains(body, `"release"`) {
		t.Fatalf("missing version management metadata: %s", body)
	}
	if f.gotCaller.UID != "user-1" || f.gotCaller.SpaceID != "space-a" || f.gotPlugin != "plugin-1" || f.gotVersion != "version-1" {
		t.Fatalf("scope/IDs=%+v", f)
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("historical content can be cached publicly")
	}
}

func TestVersionFileWireCommitsOnlySuccessfulBytes(t *testing.T) {
	f := &versionServiceFake{fakeService: &fakeService{}, file: []byte("original bytes")}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/p1/versions/v1/file?path=assets%2Fold.bin", nil))
	if rec.Code != 200 || rec.Body.String() != "original bytes" || f.gotPath != "assets/old.bin" {
		t.Fatalf("response=%d %s path=%q", rec.Code, rec.Body.String(), f.gotPath)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "old.bin") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers=%v", rec.Header())
	}
	f.versionErr = pluginsvc.ErrNotFound
	rec = httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/p1/versions/v1/file?path=secret", nil))
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "original bytes") {
		t.Fatalf("committed bytes before error: %d %s", rec.Code, rec.Body.String())
	}
}

func TestVersionManagementRoutesFailClosedWithoutCapability(t *testing.T) {
	rec := httptest.NewRecorder()
	testEngine(&fakeService{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/p1/versions/v1", nil))
	if rec.Code != 404 {
		t.Fatalf("unsupported version route=%d %s", rec.Code, rec.Body.String())
	}
}

type adminHistoricalFake struct {
	*fakeAdminService
	*versionServiceFake
	helperVersion string
}

func (f *adminHistoricalFake) AdminVersionSkillMarkdown(_ context.Context, c pluginsvc.Caller, _, vid string) (string, error) {
	f.helperVersion = vid
	f.gotCaller = c
	return "historical", nil
}
func (f *adminHistoricalFake) AdminOpenVersionSkillPackage(_ context.Context, c pluginsvc.Caller, _, vid string) (*pluginsvc.SkillPackageStream, error) {
	f.helperVersion = vid
	f.gotCaller = c
	return &pluginsvc.SkillPackageStream{FileName: "history.zip", Write: func(w io.Writer) error { _, e := io.WriteString(w, "zip"); return e }}, nil
}
func TestAdminHistoricalContractAndRemovedOperations(t *testing.T) {
	label := "1.0.0"
	f := &adminHistoricalFake{fakeAdminService: &fakeAdminService{}, versionServiceFake: &versionServiceFake{fakeService: &fakeService{}, version: model.PluginVersion{ID: "v1", PluginID: "skill", PluginType: model.PluginTypeSkill, Version: "1", DeclaredVersion: &label, Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{"attachments":[{"path":"SKILL.md","mime_type":"text/markdown"}]}`), Relations: json.RawMessage(`[]`)}}}
	r := adminTestEngine(f, nil)
	for _, tc := range []struct{ method, path string }{{"GET", "/versions"}, {"GET", "/versions/v1/file"}, {"POST", "/versions/v1/restore"}, {"POST", "/publish"}} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, "/api/v1/admin/plugins/skill"+tc.path, nil))
		if rec.Code != 404 {
			t.Fatalf("removed operation %s: %d", tc.path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/admin/plugins/skill/versions/v1", nil))
	if rec.Code != 200 {
		t.Fatalf("detail %d %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if e := json.Unmarshal(rec.Body.Bytes(), &envelope); e != nil {
		t.Fatal(e)
	}
	for _, k := range []string{"plugin_id", "version_id", "revision_no", "version_label", "files", "relations", "relation_digest"} {
		if _, ok := envelope.Data[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
	for _, k := range []string{"release", "version", "published_version_id", "plugin_json"} {
		if _, ok := envelope.Data[k]; ok {
			t.Errorf("unexpected %s", k)
		}
	}
	for _, route := range []string{"skill_md", "download"} {
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/admin/plugins/skill/"+route+"?version_id=v1", nil))
		if rec.Code != 200 || f.helperVersion != "v1" || !f.gotCaller.IsSystemAdmin {
			t.Fatalf("helper %s: %d %s", route, rec.Code, rec.Body.String())
		}
	}
}
