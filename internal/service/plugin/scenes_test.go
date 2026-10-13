package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

type fakeSceneStore struct {
	scenes           map[string]model.PluginScene
	placements       map[string]model.AdminPluginPlacement
	listFilter       pluginrepo.PlacementListFilter
	createdScene     model.PluginScene
	createdVisible   bool
	createdSceneCode string
	createdPluginID  string
	updatedSceneID   string
	sceneUpdate      pluginrepo.SceneUpdate
	updatedPlacement string
	placementUpdate  pluginrepo.PlacementUpdate
	err              error
	batchSceneCode   string
	batchItems       []pluginrepo.PlacementBatchItem
	batchIsPlaced    bool
}

func (f *fakeSceneStore) ListScenes(context.Context) ([]model.PluginScene, error) {
	return nil, f.err
}
func (f *fakeSceneStore) GetScene(_ context.Context, id string) (*model.PluginScene, error) {
	if f.err != nil {
		return nil, f.err
	}
	item, ok := f.scenes[id]
	if !ok {
		return nil, pluginrepo.ErrNotFound
	}
	return &item, nil
}
func (f *fakeSceneStore) CreateScene(_ context.Context, scene model.PluginScene) error {
	if f.err != nil {
		return f.err
	}
	f.createdScene = scene
	if f.scenes == nil {
		f.scenes = map[string]model.PluginScene{}
	}
	f.scenes[scene.ID] = scene
	return nil
}
func (f *fakeSceneStore) UpdateScene(_ context.Context, sceneID string, update pluginrepo.SceneUpdate) error {
	if f.err != nil {
		return f.err
	}
	f.updatedSceneID, f.sceneUpdate = sceneID, update
	scene := f.scenes[sceneID]
	if update.Name != nil {
		scene.Name = *update.Name
	}
	if update.Description != nil {
		scene.Description = *update.Description
	}
	if update.SortOrder != nil {
		scene.SortOrder = *update.SortOrder
	}
	f.scenes[sceneID] = scene
	return nil
}
func (f *fakeSceneStore) DeleteScene(context.Context, string) error { return f.err }
func (f *fakeSceneStore) ListPlacements(_ context.Context, filter pluginrepo.PlacementListFilter) ([]model.AdminPluginPlacement, int64, error) {
	f.listFilter = filter
	return nil, 0, f.err
}
func (f *fakeSceneStore) GetPlacement(_ context.Context, id string) (*model.AdminPluginPlacement, error) {
	if f.err != nil {
		return nil, f.err
	}
	item, ok := f.placements[id]
	if !ok {
		return nil, pluginrepo.ErrNotFound
	}
	return &item, nil
}
func (f *fakeSceneStore) CreatePlacement(_ context.Context, id, sceneCode, pluginID string, visible bool, sortOrder int) error {
	if f.err != nil {
		return f.err
	}
	f.createdSceneCode, f.createdPluginID, f.createdVisible = sceneCode, pluginID, visible
	if f.placements == nil {
		f.placements = map[string]model.AdminPluginPlacement{}
	}
	f.placements[id] = model.AdminPluginPlacement{ID: id, SceneCode: sceneCode, PluginID: pluginID, IsVisible: visible, SortOrder: sortOrder}
	return nil
}
func (f *fakeSceneStore) UpdatePlacement(_ context.Context, placementID string, update pluginrepo.PlacementUpdate) error {
	f.updatedPlacement, f.placementUpdate = placementID, update
	return f.err
}
func (f *fakeSceneStore) DeletePlacement(context.Context, string) error { return f.err }
func (f *fakeSceneStore) BatchSetPlacements(_ context.Context, sceneCode string, items []pluginrepo.PlacementBatchItem, isPlaced, _ bool, _ int) error {
	f.batchSceneCode, f.batchItems, f.batchIsPlaced = sceneCode, items, isPlaced
	return f.err
}

func TestAdminCreateSceneNormalizesAndValidatesCode(t *testing.T) {
	store := &fakeSceneStore{}
	svc := NewScenes(store, func() string { return "scene-1" })
	item, err := svc.AdminCreateScene(context.Background(), SceneCreateParams{Code: " featured ", Name: " Featured ", SortOrder: 10})
	if err != nil {
		t.Fatalf("AdminCreateScene: %v", err)
	}
	if item.Code != "featured" || item.Name != "Featured" || store.createdScene.ID != "scene-1" {
		t.Fatalf("scene=%#v created=%#v", item, store.createdScene)
	}
	if _, err := svc.AdminCreateScene(context.Background(), SceneCreateParams{Code: "Bad Code", Name: "Bad"}); err != ErrInvalidRequest {
		t.Fatalf("bad code error=%v, want ErrInvalidRequest", err)
	}
	if _, err := svc.AdminCreateScene(context.Background(), SceneCreateParams{Code: "loop.marketplace.home", Name: "Loop Home"}); err != nil {
		t.Fatalf("legacy dotted code error=%v", err)
	}
}

func TestAdminSceneMapsStoreErrors(t *testing.T) {
	svc := NewScenes(&fakeSceneStore{err: pluginrepo.ErrConflict}, func() string { return "scene-1" })
	if _, err := svc.AdminCreateScene(context.Background(), SceneCreateParams{Code: "featured", Name: "Featured"}); err != ErrConflict {
		t.Fatalf("error=%v, want ErrConflict", err)
	}
}

func TestAdminListPlacementsForwardsNormalizedFilter(t *testing.T) {
	store := &fakeSceneStore{}
	svc := NewScenes(store, func() string { return "placement-1" })
	_, _, err := svc.AdminListPlacements(context.Background(), PlacementListParams{SceneCode: " featured ", Type: model.PluginTypeSkill, Keyword: " ops ", Limit: 20, Offset: 20})
	if err != nil {
		t.Fatalf("AdminListPlacements: %v", err)
	}
	if store.listFilter.SceneCode != "featured" || store.listFilter.Keyword != "ops" || store.listFilter.Type != model.PluginTypeSkill || store.listFilter.Offset != 20 {
		t.Fatalf("filter=%#v", store.listFilter)
	}
}

func TestAdminCreatePlacementPreservesVisibility(t *testing.T) {
	store := &fakeSceneStore{}
	svc := NewScenes(store, func() string { return "placement-1" })
	item, err := svc.AdminCreatePlacement(context.Background(), PlacementCreateParams{SceneCode: "featured", PluginID: "skill-1", IsVisible: false, SortOrder: 7})
	if err != nil {
		t.Fatalf("AdminCreatePlacement: %v", err)
	}
	if store.createdSceneCode != "featured" || store.createdPluginID != "skill-1" || store.createdVisible || item.SortOrder != 7 {
		t.Fatalf("created scene=%q plugin=%q visible=%v item=%#v", store.createdSceneCode, store.createdPluginID, store.createdVisible, item)
	}
}

func TestAdminCreatePlacementRejectsHiddenDefault(t *testing.T) {
	store := &fakeSceneStore{}
	svc := NewScenes(store, func() string { return "placement-1" })
	_, err := svc.AdminCreatePlacement(context.Background(), PlacementCreateParams{
		SceneCode: "default", PluginID: "skill-1", IsVisible: false,
	})
	if err != ErrConflict {
		t.Fatalf("error=%v, want ErrConflict", err)
	}
	if store.createdPluginID != "" {
		t.Fatal("hidden default placement reached the repository")
	}
}

func TestAdminUpdatesOnlySuppliedFields(t *testing.T) {
	store := &fakeSceneStore{
		scenes: map[string]model.PluginScene{
			"scene-1": {ID: "scene-1", Name: "Featured", Description: "Keep me", SortOrder: 10},
		},
		placements: map[string]model.AdminPluginPlacement{
			"placement-1": {ID: "placement-1", IsVisible: false, SortOrder: 20},
		},
	}
	svc := NewScenes(store, func() string { return "unused" })

	sortOrder := 30
	scene, err := svc.AdminUpdateScene(context.Background(), "scene-1", SceneUpdateParams{SortOrder: &sortOrder})
	if err != nil {
		t.Fatal(err)
	}
	if store.sceneUpdate.Name != nil || store.sceneUpdate.Description != nil || store.sceneUpdate.SortOrder == nil || scene.Description != "Keep me" {
		t.Fatalf("update=%#v scene=%#v", store.sceneUpdate, scene)
	}

	visible := true
	if _, err := svc.AdminUpdatePlacement(context.Background(), "placement-1", PlacementUpdateParams{IsVisible: &visible}); err != nil {
		t.Fatal(err)
	}
	if store.placementUpdate.IsVisible == nil || store.placementUpdate.SortOrder != nil {
		t.Fatalf("placement update=%#v", store.placementUpdate)
	}
}

func TestAdminBatchSetPlacementsValidatesAndBuildsAtomicItems(t *testing.T) {
	store := &fakeSceneStore{}
	ids := []string{"placement-1", "placement-2"}
	svc := NewScenes(store, func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	})
	err := svc.AdminBatchSetPlacements(context.Background(), PlacementBatchParams{
		SceneCode: " featured ", PluginIDs: []string{"skill-1", "skill-2"}, IsPlaced: true, IsVisible: true, SortOrder: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.batchSceneCode != "featured" || !store.batchIsPlaced || len(store.batchItems) != 2 || store.batchItems[1].PlacementID != "placement-2" {
		t.Fatalf("scene=%q placed=%v items=%#v", store.batchSceneCode, store.batchIsPlaced, store.batchItems)
	}
	if err := svc.AdminBatchSetPlacements(context.Background(), PlacementBatchParams{SceneCode: "featured", PluginIDs: []string{"same", "same"}, IsPlaced: true, IsVisible: true}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("duplicate error=%v, want ErrInvalidRequest", err)
	}
	if err := svc.AdminBatchSetPlacements(context.Background(), PlacementBatchParams{SceneCode: "default", PluginIDs: []string{"skill-1"}, IsPlaced: false}); err != ErrConflict {
		t.Fatalf("default removal error=%v, want ErrConflict", err)
	}
	if err := svc.AdminBatchSetPlacements(context.Background(), PlacementBatchParams{SceneCode: "default", PluginIDs: []string{"skill-1"}, IsPlaced: true, IsVisible: false}); err != ErrConflict {
		t.Fatalf("hidden default error=%v, want ErrConflict", err)
	}
}

func TestAdminBatchRemovePlacementsDoesNotGenerateIDs(t *testing.T) {
	store := &fakeSceneStore{}
	svc := NewScenes(store, func() string {
		t.Fatal("removing placements must not generate placement IDs")
		return ""
	})

	err := svc.AdminBatchSetPlacements(context.Background(), PlacementBatchParams{
		SceneCode: "featured", PluginIDs: []string{"skill-2", "skill-1"}, IsPlaced: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.batchItems) != 2 || store.batchItems[0].PluginID != "skill-1" || store.batchItems[0].InputIndex != 1 || store.batchItems[0].PlacementID != "" {
		t.Fatalf("items=%#v", store.batchItems)
	}
}

func TestAdminBatchSetPlacementsPreservesFailedInputIndex(t *testing.T) {
	store := &fakeSceneStore{err: &pluginrepo.BatchItemError{Index: 1, Err: pluginrepo.ErrNotFound}}
	svc := NewScenes(store, func() string { return "placement-1" })

	err := svc.AdminBatchSetPlacements(context.Background(), PlacementBatchParams{
		SceneCode: "featured", PluginIDs: []string{"skill-2", "skill-1"}, IsPlaced: true, IsVisible: true,
	})
	var itemErr *BatchItemError
	if !errors.As(err, &itemErr) || itemErr.Index != 1 || !errors.Is(err, ErrNotFound) {
		t.Fatalf("error=%#v, want failed input index 1 wrapping ErrNotFound", err)
	}
}
