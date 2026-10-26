package plugin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func adminProjectionPlugin(typ model.PluginType) model.Plugin {
	current := "00000000-0000-4000-8000-000000000002"
	published := "00000000-0000-4000-8000-000000000001"
	label := "2.0.0"
	revision := uint64(2)
	return model.Plugin{
		ID: "plugin-1", Name: "Plugin", Type: typ,
		Visibility: model.PluginVisibilitySpace,
		Tags:       json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`),
		EditRevision: 5, CurrentVersionID: &current, CurrentVersion: &label,
		PublishedVersionID: &published,
		View:               model.PluginViewEditing,
		SelectedVersionID:  &current, VersionLabel: &label, RevisionNo: &revision,
		HasUnpublishedChanges: true, IsPublished: true,
		DeclaredVisibility: model.PluginVisibilitySpace,
		CreatedAt:          time.Now(), UpdatedAt: time.Now(),
	}
}

func decodeAdminData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body=%s: %v", rec.Body.String(), err)
	}
	return envelope.Data
}

func decodeAdminListData(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body=%s: %v", rec.Body.String(), err)
	}
	return envelope.Data
}

func TestAdminListForwardsEditingViewAndExposesProjection(t *testing.T) {
	item := adminProjectionPlugin(model.PluginTypeSkill)
	f := &fakeAdminService{list: []model.Plugin{item}}
	rec := httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins?plugin_type=skill&view=editing", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.listParams.View != "editing" {
		t.Fatalf("view not forwarded: %#v", f.listParams)
	}
	items := decodeAdminListData(t, rec)
	if len(items) != 1 {
		t.Fatalf("items=%v", items)
	}
	data := items[0]
	if data["view"] != "editing" {
		t.Fatalf("view=%v", data["view"])
	}
	if got, _ := data["selected_version_id"].(string); got != *item.SelectedVersionID {
		t.Fatalf("selected_version_id=%v want %s", data["selected_version_id"], *item.SelectedVersionID)
	}
	if got, _ := data["version_label"].(string); got != *item.VersionLabel {
		t.Fatalf("version_label=%v", data["version_label"])
	}
	if got, _ := data["current_version_id"].(string); got != *item.CurrentVersionID {
		t.Fatalf("current_version_id=%v", data["current_version_id"])
	}
	if got, _ := data["revision_no"].(float64); got != float64(*item.RevisionNo) {
		t.Fatalf("revision_no=%v", data["revision_no"])
	}
	if data["has_unpublished_changes"] != true || data["is_published"] != true {
		t.Fatalf("projection booleans missing: %v", data)
	}
	if data["declared_visibility"] != "space" {
		t.Fatalf("declared_visibility=%v", data["declared_visibility"])
	}
	if _, present := data["published_version_id"]; present {
		t.Fatalf("Admin DTO leaked published_version_id: %s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("published_version_id")) {
		t.Fatalf("Admin DTO leaked published_version_id: %s", rec.Body.String())
	}
}

func TestAdminDetailForwardsEffectiveViewForConnector(t *testing.T) {
	connector := adminProjectionPlugin(model.PluginTypeConnector)
	revision := uint64(9)
	digest := "sha256:effective"
	connector.View = model.PluginViewEffective
	connector.CurrentVersionID = nil
	connector.SelectedVersionID = nil
	connector.VersionLabel = nil
	connector.RevisionNo = nil
	connector.EffectiveRevision = &revision
	connector.EffectiveDigest = &digest
	connector.PublishedVersionID = nil
	f := &fakeAdminService{detail: &pluginsvc.Detail{Plugin: &connector, Relations: []model.PluginRelation{}}}
	rec := httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/plugin-1?view=effective&include_relations=false", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.detailView != "effective" || f.includeRels {
		t.Fatalf("view/include_relations not forwarded: view=%q include=%v", f.detailView, f.includeRels)
	}
	data := decodeAdminData(t, rec)
	pluginData, _ := data["plugin"].(map[string]any)
	if pluginData["view"] != "effective" {
		t.Fatalf("view=%v", pluginData["view"])
	}
	if pluginData["effective_revision"] != float64(revision) || pluginData["effective_digest"] != digest {
		t.Fatalf("effective fields missing: %v", pluginData)
	}
	for _, absent := range []string{"selected_version_id", "version_label", "current_version_id", "revision_no", "published_version_id"} {
		if _, present := pluginData[absent]; present {
			t.Fatalf("connector effective must omit %s: %s", absent, rec.Body.String())
		}
	}
	if pluginData["is_published"] != true {
		t.Fatalf("is_published=%v", pluginData["is_published"])
	}
}

func TestAdminInvalidViewMapsToViewInvalid(t *testing.T) {
	f := &fakeAdminService{err: &pluginsvc.ReviewFieldError{Field: "view", Reason: "view_invalid", Err: pluginsvc.ErrInvalidRequest}}
	for _, path := range []string{
		"/api/v1/admin/plugins?plugin_type=skill&view=published",
		"/api/v1/admin/plugins/plugin-1?view=effective",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var envelope struct {
				Error struct {
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Details["field"] != "view" || envelope.Error.Details["reason"] != "view_invalid" {
				t.Fatalf("details=%v", envelope.Error.Details)
			}
		})
	}
}

func TestAdminSkillMarkdownExposesActualVersionID(t *testing.T) {
	f := &fakeAdminService{skillMD: "# Skill", skillMDVersion: "v-curr"}
	rec := httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/plugin-1/skill_md", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.artifactVersion != "" {
		t.Fatalf("default A23 must not request a version: %q", f.artifactVersion)
	}
	data := decodeAdminData(t, rec)
	if data["content"] != "# Skill" || data["version_id"] != "v-curr" {
		t.Fatalf("data=%v", data)
	}

	f = &fakeAdminService{skillMD: "# Old", skillMDVersion: "v-old"}
	rec = httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/plugin-1/skill_md?version_id=v-old", nil))
	if rec.Code != http.StatusOK || f.artifactVersion != "v-old" {
		t.Fatalf("explicit version not forwarded: status=%d version=%q", rec.Code, f.artifactVersion)
	}
	if decodeAdminData(t, rec)["version_id"] != "v-old" {
		t.Fatalf("actual version not returned: %s", rec.Body.String())
	}
}

func TestAdminDownloadExposesActualVersionHeader(t *testing.T) {
	f := &fakeAdminService{download: &pluginsvc.SkillPackageStream{FileName: "Skill.zip", VersionID: "v-curr", Write: func(w io.Writer) error {
		_, err := w.Write([]byte("PK"))
		return err
	}}}
	rec := httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/plugin-1/download", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Plugin-Version-Id"); got != "v-curr" {
		t.Fatalf("version header=%q want v-curr", got)
	}
	if f.artifactVersion != "" {
		t.Fatalf("default A23 must not request a version: %q", f.artifactVersion)
	}

	f = &fakeAdminService{download: &pluginsvc.SkillPackageStream{FileName: "Skill.zip", VersionID: "v-old", Write: func(w io.Writer) error {
		_, err := w.Write([]byte("PK"))
		return err
	}}}
	rec = httptest.NewRecorder()
	adminTestEngine(f, &fakeAdminCategories{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/plugin-1/download?version_id=v-old", nil))
	if rec.Code != http.StatusOK || f.artifactVersion != "v-old" {
		t.Fatalf("explicit version not forwarded: status=%d version=%q", rec.Code, f.artifactVersion)
	}
	if got := rec.Header().Get("X-Plugin-Version-Id"); got != "v-old" {
		t.Fatalf("version header=%q want v-old", got)
	}
}
