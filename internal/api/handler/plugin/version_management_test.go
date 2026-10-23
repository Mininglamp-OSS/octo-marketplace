package plugin

import (
	"bytes"
	"context"
	"encoding/json"
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
func (f *versionServiceFake) RestoreVersion(_ context.Context, c pluginsvc.Caller, pid, vid, label string, _ bool) (*pluginsvc.Detail, error) {
	f.gotCaller = c
	f.gotPlugin = pid
	f.gotVersion = vid
	f.gotLabel = label
	return f.detail, f.versionErr
}
func (f *versionServiceFake) AdminPublishVersion(context.Context, pluginsvc.Caller, string) (*pluginsvc.Detail, error) {
	return f.detail, f.versionErr
}

func TestHistoricalVersionWireDoesNotExposeStorageKeys(t *testing.T) {
	key := "plugins/private-space/attachments/versions/private-object"
	rawRef, _ := json.Marshal(map[string]any{"object_key": key, "zip_object_key": key, "file_url": key, "file_name": "skill.zip"})
	pkg, _ := json.Marshal(map[string]any{"attachments": []any{map[string]any{"path": "skill/ref.json", "content_type": "raw", "raw_content": string(rawRef)}, map[string]any{"path": "file.bin", "content_type": "storage", "storage_uri": key}}})
	label := "1.0.0"
	source := "source-version"
	f := &versionServiceFake{fakeService: &fakeService{}, version: model.PluginVersion{ID: "version-1", PluginID: "plugin-1", DeclaredVersion: &label, RestoredFromVersionID: &source, Manifest: json.RawMessage(`{}`), Package: pkg, AttachmentKeys: json.RawMessage(`{"file.bin":"` + key + `"}`), Artifacts: json.RawMessage(`[{"object_key":"` + key + `"}]`), Relations: json.RawMessage(`[]`), Release: &model.PluginVersionRelease{VersionLabel: label, ReleasedBy: "owner", ReleasedAt: time.Now()}}}
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
	if !strings.Contains(body, `"declared_version":"1.0.0"`) || !strings.Contains(body, `"release"`) || !strings.Contains(body, `"restored_from_version_id":"source-version"`) {
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

func TestRestoreVersionAcceptsOnlyNewLabelAndTrustedIdentity(t *testing.T) {
	f := &versionServiceFake{fakeService: &fakeService{detail: &pluginsvc.Detail{Plugin: &model.Plugin{ID: "plugin-1", Type: model.PluginTypeExpert}, Relations: []model.PluginRelation{}}}}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/plugins/plugin-1/versions/old/restore", bytes.NewBufferString(`{"version":"3.0.0"}`)))
	if rec.Code != 200 || f.gotLabel != "3.0.0" || f.gotVersion != "old" || f.gotCaller.UID != "user-1" {
		t.Fatalf("restore=%d %s %+v", rec.Code, rec.Body.String(), f)
	}
	for _, body := range []string{`{"version":"4.0.0","owner_id":"attacker"}`, `{"version":"4.0.0","space_id":"other"}`} {
		rec = httptest.NewRecorder()
		testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/plugins/plugin-1/versions/old/restore", bytes.NewBufferString(body)))
		if rec.Code != 400 {
			t.Fatalf("client identity accepted: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestVersionManagementRoutesFailClosedWithoutCapability(t *testing.T) {
	rec := httptest.NewRecorder()
	testEngine(&fakeService{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/p1/versions/v1", nil))
	if rec.Code != 404 {
		t.Fatalf("unsupported version route=%d %s", rec.Code, rec.Body.String())
	}
}
