package plugin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type fakeSceneAdminService struct {
	listParams pluginsvc.PlacementListParams
	create     pluginsvc.PlacementCreateParams
	update     pluginsvc.PlacementUpdateParams
	batch      pluginsvc.PlacementBatchParams
	items      []model.AdminPluginPlacement
	err        error
}

func (f *fakeSceneAdminService) AdminListScenes(context.Context) ([]model.PluginScene, error) {
	return []model.PluginScene{{ID: "scene-1", Code: "featured", Name: "Featured"}}, f.err
}
func (f *fakeSceneAdminService) AdminCreateScene(context.Context, pluginsvc.SceneCreateParams) (*model.PluginScene, error) {
	return &model.PluginScene{ID: "scene-1", Code: "featured", Name: "Featured"}, f.err
}
func (f *fakeSceneAdminService) AdminUpdateScene(context.Context, string, pluginsvc.SceneUpdateParams) (*model.PluginScene, error) {
	return &model.PluginScene{ID: "scene-1", Code: "featured", Name: "Featured"}, f.err
}
func (f *fakeSceneAdminService) AdminDeleteScene(context.Context, string) error { return f.err }
func (f *fakeSceneAdminService) AdminListPlacements(_ context.Context, p pluginsvc.PlacementListParams) ([]model.AdminPluginPlacement, int64, error) {
	f.listParams = p
	return f.items, int64(len(f.items)), f.err
}
func (f *fakeSceneAdminService) AdminCreatePlacement(_ context.Context, p pluginsvc.PlacementCreateParams) (*model.AdminPluginPlacement, error) {
	f.create = p
	return &model.AdminPluginPlacement{ID: "placement-1", SceneCode: p.SceneCode, PluginID: p.PluginID, IsVisible: p.IsVisible, SortOrder: p.SortOrder}, f.err
}
func (f *fakeSceneAdminService) AdminUpdatePlacement(_ context.Context, _ string, p pluginsvc.PlacementUpdateParams) (*model.AdminPluginPlacement, error) {
	f.update = p
	return &model.AdminPluginPlacement{ID: "placement-1"}, f.err
}
func (f *fakeSceneAdminService) AdminDeletePlacement(context.Context, string) error { return f.err }
func (f *fakeSceneAdminService) AdminBatchSetPlacements(_ context.Context, p pluginsvc.PlacementBatchParams) error {
	f.batch = p
	f.create = pluginsvc.PlacementCreateParams{SceneCode: p.SceneCode, PluginID: p.PluginIDs[0], IsVisible: p.IsVisible, SortOrder: p.SortOrder}
	return f.err
}

func sceneAdminTestEngine(svc AdminSceneService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := marketmiddleware.NewAdminAuthenticator(false, nil, model.Identity{UID: "admin-1", Name: "Root"})
	NewSceneAdmin(svc).RegisterAdmin(r, auth)
	return r
}

func TestSceneAdminListPlacementsForwardsFiltersAndPagination(t *testing.T) {
	fake := &fakeSceneAdminService{items: []model.AdminPluginPlacement{{ID: "placement-1", SceneCode: "featured", PluginType: model.PluginTypeSkill}}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugin_placements?scene_code=featured&plugin_type=skill&q=ops&page=2&page_size=10", nil)
	sceneAdminTestEngine(fake).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.listParams.SceneCode != "featured" || fake.listParams.Type != model.PluginTypeSkill || fake.listParams.Keyword != "ops" || fake.listParams.Limit != 10 || fake.listParams.Offset != 10 {
		t.Fatalf("params=%#v", fake.listParams)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"pagination":{"total":1,"page":2,"page_size":10}`)) {
		t.Fatalf("body=%s", recorder.Body.String())
	}
}

func TestSceneAdminCreatePlacementDefaultsVisible(t *testing.T) {
	fake := &fakeSceneAdminService{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugin_placements", bytes.NewBufferString(`{"scene_code":"featured","plugin_id":"skill-1","sort_order":8}`))
	request.Header.Set("Content-Type", "application/json")
	sceneAdminTestEngine(fake).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !fake.create.IsVisible || fake.create.SceneCode != "featured" || fake.create.PluginID != "skill-1" || fake.create.SortOrder != 8 {
		t.Fatalf("params=%#v", fake.create)
	}
}

func TestSceneAdminRejectsUnknownFields(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugin_scenes", bytes.NewBufferString(`{"scene_code":"featured","name":"Featured","config":{}}`))
	request.Header.Set("Content-Type", "application/json")
	sceneAdminTestEngine(&fakeSceneAdminService{}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"VALIDATION_ERROR"`)) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSceneAdminConflictIs409(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/plugin_scenes/default-id", nil)
	sceneAdminTestEngine(&fakeSceneAdminService{err: pluginsvc.ErrConflict}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"CONFLICT"`)) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSceneAdminBatchPlacementForwardsAllSelectedPlugins(t *testing.T) {
	fake := &fakeSceneAdminService{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugin_placements/_batch", bytes.NewBufferString(`{"scene_code":"featured","plugin_ids":["skill-1","skill-2"],"is_placed":true,"is_visible":true,"sort_order":20}`))
	request.Header.Set("Content-Type", "application/json")
	sceneAdminTestEngine(fake).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.batch.SceneCode != "featured" || len(fake.batch.PluginIDs) != 2 || fake.batch.PluginIDs[1] != "skill-2" || !fake.batch.IsPlaced || !fake.batch.IsVisible || fake.batch.SortOrder != 20 {
		t.Fatalf("forwarded=%#v", fake.batch)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"plugin_id":"skill-2"`)) || !bytes.Contains(recorder.Body.Bytes(), []byte(`"is_placed":true`)) {
		t.Fatalf("body=%s", recorder.Body.String())
	}
}

func TestSceneAdminBatchPlacementReportsFailedInputIndex(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugin_placements/_batch", bytes.NewBufferString(`{"scene_code":"featured","plugin_ids":["skill-1","missing"],"is_placed":true}`))
	request.Header.Set("Content-Type", "application/json")
	sceneAdminTestEngine(&fakeSceneAdminService{err: &pluginsvc.BatchItemError{Index: 1, Err: pluginsvc.ErrNotFound}}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound || !bytes.Contains(recorder.Body.Bytes(), []byte(`"failed_index":1`)) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
