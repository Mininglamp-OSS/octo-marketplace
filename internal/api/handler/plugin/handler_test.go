package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type fakeService struct {
	caller           pluginsvc.Caller
	write            pluginsvc.WriteRequest
	listParams       pluginsvc.ListParams
	list             []model.Plugin
	detail           *pluginsvc.Detail
	includeRelations bool
	err              error
	download         *pluginsvc.SkillPackageStream
	versions         []model.PluginVersion
	versionTotal     int64
	installID        string
	installParams    pluginsvc.InstallParams
	installOutcome   *pluginsvc.InstallOutcome
	importParams     pluginsvc.ImportParams
	skillMarkdown    string
	tagParams        pluginsvc.TagListParams
	tags             []model.TagFilter
	reviewPolicy     model.PluginReviewPolicy
	reviewPolicySet  *bool
	// review carries the review half of the fake (see review_fake_test.go).
	review reviewServiceFake
	// listing carries the publish/delist half (see listing_fake_test.go).
	listing listingServiceFake
}

func (f *fakeService) GetReviewPolicy(_ context.Context, c pluginsvc.Caller) (model.PluginReviewPolicy, error) {
	f.caller = c
	return f.reviewPolicy, f.err
}

func (f *fakeService) UpdateReviewPolicy(_ context.Context, c pluginsvc.Caller, enabled bool) (model.PluginReviewPolicy, error) {
	f.caller = c
	f.reviewPolicySet = &enabled
	return model.PluginReviewPolicy{IsAutoApproveEnabled: enabled}, f.err
}

func (f *fakeService) List(_ context.Context, c pluginsvc.Caller, p pluginsvc.ListParams) ([]model.Plugin, int64, error) {
	f.caller, f.listParams = c, p
	return f.list, int64(len(f.list)), f.err
}
func (f *fakeService) Detail(_ context.Context, c pluginsvc.Caller, _ string, includeRelations bool) (*pluginsvc.Detail, error) {
	f.caller, f.includeRelations = c, includeRelations
	return f.detail, f.err
}
func (f *fakeService) Create(_ context.Context, c pluginsvc.Caller, r pluginsvc.WriteRequest) (*pluginsvc.Detail, error) {
	f.caller, f.write = c, r
	return f.detail, f.err
}
func (f *fakeService) Update(_ context.Context, c pluginsvc.Caller, _ string, r pluginsvc.WriteRequest) (*pluginsvc.Detail, error) {
	f.caller, f.write = c, r
	return f.detail, f.err
}
func (f *fakeService) Delete(_ context.Context, c pluginsvc.Caller, _ string) error {
	f.caller = c
	return f.err
}
func (f *fakeService) ListVersions(context.Context, pluginsvc.Caller, string, int, int) ([]model.PluginVersion, int64, error) {
	return f.versions, f.versionTotal, f.err
}
func (f *fakeService) ListCategories(context.Context, pluginsvc.Caller, string, model.PluginType) ([]model.PluginCategory, error) {
	return nil, f.err
}
func (f *fakeService) Install(_ context.Context, c pluginsvc.Caller, pluginID string, p pluginsvc.InstallParams) (*pluginsvc.InstallOutcome, error) {
	f.caller, f.installID, f.installParams = c, pluginID, p
	if f.err != nil {
		return nil, f.err
	}
	return f.installOutcome, nil
}
func (f *fakeService) Import(_ context.Context, c pluginsvc.Caller, p pluginsvc.ImportParams) (*pluginsvc.Detail, error) {
	f.caller, f.importParams = c, p
	return f.detail, f.err
}
func (f *fakeService) SkillMarkdown(_ context.Context, c pluginsvc.Caller, _ string) (string, error) {
	f.caller = c
	return f.skillMarkdown, f.err
}
func (f *fakeService) OpenSkillPackage(_ context.Context, c pluginsvc.Caller, _ string) (*pluginsvc.SkillPackageStream, error) {
	f.caller = c
	return f.download, f.err
}
func (f *fakeService) ListTags(_ context.Context, c pluginsvc.Caller, p pluginsvc.TagListParams) ([]model.TagFilter, error) {
	f.caller, f.tagParams = c, p
	return f.tags, f.err
}

func testEngine(s Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(logging.RequestID())
	r.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, interfaceKey("marketplace.identity"), model.Identity{})
		_ = ctx
		c.Next()
	})
	// Use the real development authenticator so identity and Space are stamped by
	// the same trusted middleware path as production.
	auth := marketmiddleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Alice"}, "space-a")
	v1 := r.Group("/api/v1", auth.Handler())
	New(s).Register(v1)
	return r
}

type interfaceKey string

func TestUpsertUsesServerDerivedIdentityAndStandardEnvelope(t *testing.T) {
	space := "space-a"
	f := &fakeService{detail: &pluginsvc.Detail{Plugin: &model.Plugin{ID: "plugin-1", Name: "Demo", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilityPrivate, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), CreatedAt: time.Now(), UpdatedAt: time.Now()}}}
	body := []byte(`{"plugin":{"plugin_name":"Demo","plugin_type":"skill","visibility":"private","tags":[],"manifest_json":{},"plugin_json":{}},"relations":[]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/upsert", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	testEngine(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.caller.UID != "user-1" || f.caller.SpaceID != "space-a" || f.caller.Name != "Alice" {
		t.Fatalf("caller=%#v", f.caller)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"code":`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"data"`)) {
		t.Fatalf("nonstandard envelope: %s", rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"plugin_id":"plugin-1"`)) {
		t.Fatalf("unprefixed create response: %s", rec.Body.String())
	}
}

func TestUpsertRejectsClientIdentityFields(t *testing.T) {
	f := &fakeService{}
	rec := httptest.NewRecorder()
	body := []byte(`{"plugin":{"plugin_name":"Demo","plugin_type":"skill","visibility":"private","tags":[],"manifest_json":{},"plugin_json":{},"owner_id":"attacker"},"relations":[]}`)
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/plugins/upsert", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"VALIDATION_ERROR"`)) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestCrossSpaceNotFoundIsSanitized(t *testing.T) {
	f := &fakeService{err: pluginsvc.ErrNotFound}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/detail?plugin_id=other-space", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"NOT_FOUND"`)) || bytes.Contains(rec.Body.Bytes(), []byte("space")) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestInternalErrorIsSanitized(t *testing.T) {
	f := &fakeService{err: errors.New("sql: secret DSN")}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/detail?plugin_id=p1", nil))
	if rec.Code != http.StatusInternalServerError || bytes.Contains(rec.Body.Bytes(), []byte("secret DSN")) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHistoryListsUseExactRepositoryTotals(t *testing.T) {
	tests := []struct {
		name string
		url  string
		fake *fakeService
	}{
		{name: "versions", url: "/api/v1/plugins/versions?plugin_id=p1&page=2&page_size=10", fake: &fakeService{versions: []model.PluginVersion{{ID: "v1", Relations: json.RawMessage(`[]`)}}, versionTotal: 37}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			testEngine(tt.fake).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.url, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Pagination struct {
					Total int `json:"total"`
				} `json:"pagination"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Pagination.Total != 37 {
				t.Fatalf("pagination.total=%d want=37 body=%s", body.Pagination.Total, rec.Body.String())
			}
		})
	}
}

func TestListRequiresConfirmedQueryNames(t *testing.T) {
	f := &fakeService{}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins?plugin_type=skill", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"field":"scene_code"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListForwardsMineModeAndRepeatedTags(t *testing.T) {
	f := &fakeService{}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins?scene_code=loop&plugin_type=skill&mode=mine&tag=a&tag=b,%20c", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !f.listParams.Mine {
		t.Fatalf("mine not forwarded: %#v", f.listParams)
	}
	if len(f.listParams.Tags) != 3 || f.listParams.Tags[0] != "a" || f.listParams.Tags[1] != "b" || f.listParams.Tags[2] != "c" {
		t.Fatalf("tags=%#v", f.listParams.Tags)
	}
}

func TestListRejectsUnknownMode(t *testing.T) {
	f := &fakeService{}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins?scene_code=loop&plugin_type=skill&mode=theirs", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"field":"mode"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDetailEncodesPluginAndRelationWireIDs(t *testing.T) {
	space := "space-a"
	f := &fakeService{detail: &pluginsvc.Detail{
		Plugin:    &model.Plugin{ID: "expert-1", Type: model.PluginTypeExpert, SpaceID: &space, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)},
		Relations: []model.PluginRelation{{ID: "rel-1", SourcePluginID: "expert-1", SourcePluginType: model.PluginTypeExpert, TargetPluginID: "skill-1", TargetPluginType: model.PluginTypeSkill, Type: "expert_skill"}},
	}}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/detail?plugin_id=expert-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"plugin_id":"expert-1"`, `"source_plugin_id":"expert-1"`, `"target_plugin_id":"skill-1"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("missing %s in %s", want, rec.Body.String())
		}
	}
}

func TestDetailDefaultsRelationsAndSupportsFalse(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  bool
	}{{query: "", want: true}, {query: "&include_relations=false", want: false}} {
		f := &fakeService{detail: &pluginsvc.Detail{Plugin: &model.Plugin{}}}
		rec := httptest.NewRecorder()
		testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/detail?plugin_id=p1"+tc.query, nil))
		if rec.Code != http.StatusOK || f.includeRelations != tc.want {
			t.Fatalf("query=%q status=%d include_relations=%v body=%s", tc.query, rec.Code, f.includeRelations, rec.Body.String())
		}
	}
}

func TestInstallForwardsHeaderTokenAndReturnsTypedID(t *testing.T) {
	f := &fakeService{installOutcome: &pluginsvc.InstallOutcome{AgentID: "agent-9"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/install", strings.NewReader(`{"plugin_id":"p1","workspace_id":"ws-1","runtime_id":"rt-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("token", "octo-token")
	testEngine(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.installID != "p1" || f.installParams.WorkspaceID != "ws-1" || f.installParams.RuntimeID != "rt-1" || f.installParams.Token != "octo-token" {
		t.Fatalf("install forwarded = %q %#v", f.installID, f.installParams)
	}
	if !strings.Contains(rec.Body.String(), `"agent_id":"agent-9"`) || strings.Contains(rec.Body.String(), "squad_id") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestInstallMapsFleetUnavailability(t *testing.T) {
	f := &fakeService{err: expertsvc.ErrFleetNotConfigured}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/install", strings.NewReader(`{"plugin_id":"p1","workspace_id":"ws","runtime_id":"rt"}`))
	req.Header.Set("Content-Type", "application/json")
	testEngine(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"UPSTREAM_UNAVAILABLE"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestInstallSanitizesCrossSpaceNotFound(t *testing.T) {
	f := &fakeService{err: pluginsvc.ErrNotFound}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/install", strings.NewReader(`{"plugin_id":"other","workspace_id":"ws","runtime_id":"rt"}`))
	req.Header.Set("Content-Type", "application/json")
	testEngine(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"NOT_FOUND"`) || strings.Contains(rec.Body.String(), "space") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOldRESTRoutesAreNotRegistered(t *testing.T) {
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/plugins", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/api/v1/plugins/p1", nil),
		httptest.NewRequest(http.MethodPatch, "/api/v1/plugins/p1", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodDelete, "/api/v1/plugins/p1", nil),
	} {
		rec := httptest.NewRecorder()
		testEngine(&fakeService{}).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status=%d", req.Method, req.URL.Path, rec.Code)
		}
	}
}

func TestVersionDTOEncodesSnapshotRelationIDs(t *testing.T) {
	raw, err := json.Marshal([]model.PluginRelation{{ID: "rel-1", SourcePluginID: "expert-1", SourcePluginType: model.PluginTypeExpert, TargetPluginID: "connector-1", TargetPluginType: model.PluginTypeConnector, Type: "plugin_dependency", Data: json.RawMessage(`{"role":"tool"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	dto := versionDTO(model.PluginVersion{ID: "v1", PluginID: "expert-1", PluginType: model.PluginTypeExpert, Relations: raw})
	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"plugin_id":"expert-1"`, `"source_plugin_id":"expert-1"`, `"target_plugin_id":"connector-1"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %s in %s", want, encoded)
		}
	}
}

func TestListEmitsTypedDisplayFields(t *testing.T) {
	f := &fakeService{list: []model.Plugin{
		{ID: "conn-1", Name: "Conn", Type: model.PluginTypeConnector, Icon: "https://cdn.example.com/i.png", IconURL: "https://cdn.example.com/i.png", ToolCount: 5},
		{ID: "team-1", Name: "Team", Type: model.PluginTypeExpertTeam, MemberCount: 2},
	}}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins?scene_code=loop&plugin_type=connector", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	connector, team := body.Data[0], body.Data[1]
	if connector["icon_url"] != "https://cdn.example.com/i.png" || connector["tool_count"] != float64(5) {
		t.Fatalf("connector item = %#v", connector)
	}
	if _, leaked := connector["member_count"]; leaked {
		t.Fatalf("member_count leaked onto connector: %#v", connector)
	}
	if team["member_count"] != float64(2) {
		t.Fatalf("team item = %#v", team)
	}
	if _, leaked := team["tool_count"]; leaked {
		t.Fatalf("tool_count leaked onto team: %#v", team)
	}
}

func TestListUsesOffsetEnvelope(t *testing.T) {
	f := &fakeService{list: []model.Plugin{{ID: "p1", Name: "One", Type: model.PluginTypeSkill}}}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins?scene_code=loop&plugin_type=skill&page=2&page_size=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Pagination struct {
			Page     int `json:"page"`
			PageSize int `json:"page_size"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Pagination.Page != 2 || body.Pagination.PageSize != 10 {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"plugin_id":"p1"`) {
		t.Fatalf("list returned unprefixed ID: %s", rec.Body.String())
	}
}

func TestListItemsOmitFullPluginJSON(t *testing.T) {
	f := &fakeService{list: []model.Plugin{{ID: "p1", Name: "One", Type: model.PluginTypeSkill, Manifest: json.RawMessage(`{"m":1}`), Package: json.RawMessage(`{"attachments":[]}`)}}}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins?scene_code=loop&plugin_type=skill", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"plugin_json"`) {
		t.Fatalf("list leaked plugin_json: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"manifest_json"`) {
		t.Fatalf("list dropped manifest_json: %s", rec.Body.String())
	}
}

func TestUpsertReturnsRelationSyncResult(t *testing.T) {
	space := "space-a"
	f := &fakeService{detail: &pluginsvc.Detail{
		Plugin:         &model.Plugin{ID: "plugin-1", Name: "Demo", Type: model.PluginTypeSkill, OwnerUID: "user-1", SpaceID: &space, Visibility: model.PluginVisibilityPrivate, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`)},
		RelationResult: &pluginsvc.RelationResult{Created: []string{"rel-new"}, Updated: []string{"rel-1"}, Deleted: []string{"rel-0"}},
	}}
	body := []byte(`{"plugin":{"plugin_name":"Demo","plugin_type":"skill","visibility":"private","tags":[],"manifest_json":{},"plugin_json":{}},"relations":[{"relation_id":"rel-1","source_plugin_id":"plugin-1","target_plugin_id":"target-1","relation_type":"expert_skill"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/upsert", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	testEngine(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(f.write.Relations) != 1 || f.write.Relations[0].ID != "rel-1" || f.write.Relations[0].SourcePluginID != "plugin-1" {
		t.Fatalf("relations forwarded = %#v", f.write.Relations)
	}
	if !strings.Contains(rec.Body.String(), `"relation_result":{"created":["rel-new"],"updated":["rel-1"],"deleted":["rel-0"]}`) {
		t.Fatalf("missing relation_result: %s", rec.Body.String())
	}
}

func TestDeleteRouteSoftDeletesByBodyPluginID(t *testing.T) {
	f := &fakeService{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/delete", strings.NewReader(`{"plugin_id":"p1"}`))
	req.Header.Set("Content-Type", "application/json")
	testEngine(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"deleted":true`) || f.caller.UID != "user-1" {
		t.Fatalf("body=%s caller=%#v", rec.Body.String(), f.caller)
	}
}

func TestListTagsRouteForwardsFiltersAndClampsLimit(t *testing.T) {
	f := &fakeService{tags: []model.TagFilter{{Name: "dev", Count: 3}}}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugin_tags?scene_code=default&plugin_type=connector&q=de&mode=mine&limit=500", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"name":"dev"`) || !strings.Contains(rec.Body.String(), `"count":3`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
	want := pluginsvc.TagListParams{PlacementCode: "default", Type: model.PluginTypeConnector, Keyword: "de", Mine: true, Limit: 100}
	if f.tagParams != want {
		t.Fatalf("params = %#v", f.tagParams)
	}
	rec = httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugin_tags?mode=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus mode status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestVersionsRouteUsesOffsetEnvelope(t *testing.T) {
	f := &fakeService{versions: []model.PluginVersion{{ID: "v1", PluginID: "p1", PluginType: model.PluginTypeSkill, Version: "1.0.0", Relations: json.RawMessage(`[]`)}}, versionTotal: 1}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/versions?plugin_id=p1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"version":"1.0.0"`) || !strings.Contains(rec.Body.String(), `"pagination"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}
