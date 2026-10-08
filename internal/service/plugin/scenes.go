package plugin

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

var sceneCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type sceneStore interface {
	ListScenes(context.Context) ([]model.PluginScene, error)
	GetScene(context.Context, string) (*model.PluginScene, error)
	CreateScene(context.Context, model.PluginScene) error
	UpdateScene(context.Context, string, pluginrepo.SceneUpdate) error
	DeleteScene(context.Context, string) error
	ListPlacements(context.Context, pluginrepo.PlacementListFilter) ([]model.AdminPluginPlacement, int64, error)
	GetPlacement(context.Context, string) (*model.AdminPluginPlacement, error)
	CreatePlacement(context.Context, string, string, string, bool, int) error
	UpdatePlacement(context.Context, string, pluginrepo.PlacementUpdate) error
	DeletePlacement(context.Context, string) error
	BatchSetPlacements(context.Context, string, []pluginrepo.PlacementBatchItem, bool, bool, int) error
}

// Scenes owns the admin configuration surface for Marketplace scene codes and
// Plugin placement relationships.
type Scenes struct {
	repo sceneStore
	id   func() string
}

func NewScenes(repo sceneStore, idGen func() string) *Scenes {
	return &Scenes{repo: repo, id: idGen}
}

type SceneCreateParams struct {
	Code        string
	Name        string
	Description string
	SortOrder   int
}

type SceneUpdateParams struct {
	Name        *string
	Description *string
	SortOrder   *int
}

type PlacementListParams struct {
	SceneCode string
	Type      model.PluginType
	Keyword   string
	Limit     int
	Offset    int
}

type PlacementCreateParams struct {
	SceneCode string
	PluginID  string
	IsVisible bool
	SortOrder int
}

type PlacementUpdateParams struct {
	IsVisible *bool
	SortOrder *int
}

type PlacementBatchParams struct {
	SceneCode string
	PluginIDs []string
	IsPlaced  bool
	IsVisible bool
	SortOrder int
}

// BatchItemError reports the zero-based index of an invalid or failed item in
// the caller's original plugin_ids order.
type BatchItemError struct {
	Index int
	Err   error
}

func (e *BatchItemError) Error() string { return e.Err.Error() }
func (e *BatchItemError) Unwrap() error { return e.Err }

func (s *Scenes) AdminListScenes(ctx context.Context) ([]model.PluginScene, error) {
	items, err := s.repo.ListScenes(ctx)
	return items, mapSceneStoreError(err)
}

func (s *Scenes) AdminCreateScene(ctx context.Context, p SceneCreateParams) (*model.PluginScene, error) {
	p.Code = strings.TrimSpace(p.Code)
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	if !validSceneCode(p.Code) || !validSceneText(p.Name, p.Description) || p.SortOrder < 0 {
		return nil, ErrInvalidRequest
	}
	scene := model.PluginScene{ID: s.id(), Code: p.Code, Name: p.Name, Description: p.Description, SortOrder: p.SortOrder}
	if err := s.repo.CreateScene(ctx, scene); err != nil {
		return nil, mapSceneStoreError(err)
	}
	item, err := s.repo.GetScene(ctx, scene.ID)
	return item, mapSceneStoreError(err)
}

func (s *Scenes) AdminUpdateScene(ctx context.Context, sceneID string, p SceneUpdateParams) (*model.PluginScene, error) {
	if strings.TrimSpace(sceneID) == "" || p.Name == nil && p.Description == nil && p.SortOrder == nil {
		return nil, ErrInvalidRequest
	}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		p.Name = &name
		if name == "" || utf8.RuneCountInString(name) > 128 {
			return nil, ErrInvalidRequest
		}
	}
	if p.Description != nil {
		description := strings.TrimSpace(*p.Description)
		p.Description = &description
		if utf8.RuneCountInString(description) > 1024 {
			return nil, ErrInvalidRequest
		}
	}
	if p.SortOrder != nil && *p.SortOrder < 0 {
		return nil, ErrInvalidRequest
	}
	if err := s.repo.UpdateScene(ctx, sceneID, pluginrepo.SceneUpdate{Name: p.Name, Description: p.Description, SortOrder: p.SortOrder}); err != nil {
		return nil, mapSceneStoreError(err)
	}
	item, err := s.repo.GetScene(ctx, sceneID)
	return item, mapSceneStoreError(err)
}

func (s *Scenes) AdminDeleteScene(ctx context.Context, sceneID string) error {
	if strings.TrimSpace(sceneID) == "" {
		return ErrInvalidRequest
	}
	return mapSceneStoreError(s.repo.DeleteScene(ctx, sceneID))
}

func (s *Scenes) AdminListPlacements(ctx context.Context, p PlacementListParams) ([]model.AdminPluginPlacement, int64, error) {
	if p.Type != "" && !validPluginType(p.Type) {
		return nil, 0, ErrInvalidRequest
	}
	if p.Limit < 0 || p.Limit > maxListLimit || p.Offset < 0 {
		return nil, 0, ErrInvalidRequest
	}
	items, total, err := s.repo.ListPlacements(ctx, pluginrepo.PlacementListFilter{
		SceneCode: strings.TrimSpace(p.SceneCode), Type: p.Type,
		Keyword: strings.TrimSpace(p.Keyword), Limit: p.Limit, Offset: p.Offset,
	})
	if err != nil {
		return nil, 0, mapSceneStoreError(err)
	}
	return items, total, nil
}

func (s *Scenes) AdminCreatePlacement(ctx context.Context, p PlacementCreateParams) (*model.AdminPluginPlacement, error) {
	p.SceneCode = strings.TrimSpace(p.SceneCode)
	p.PluginID = strings.TrimSpace(p.PluginID)
	if !validSceneCode(p.SceneCode) || p.PluginID == "" || p.SortOrder < 0 {
		return nil, ErrInvalidRequest
	}
	if p.SceneCode == "default" && !p.IsVisible {
		return nil, ErrConflict
	}
	id := s.id()
	if err := s.repo.CreatePlacement(ctx, id, p.SceneCode, p.PluginID, p.IsVisible, p.SortOrder); err != nil {
		return nil, mapSceneStoreError(err)
	}
	item, err := s.repo.GetPlacement(ctx, id)
	return item, mapSceneStoreError(err)
}

func (s *Scenes) AdminUpdatePlacement(ctx context.Context, placementID string, p PlacementUpdateParams) (*model.AdminPluginPlacement, error) {
	if strings.TrimSpace(placementID) == "" || p.IsVisible == nil && p.SortOrder == nil {
		return nil, ErrInvalidRequest
	}
	if p.SortOrder != nil && *p.SortOrder < 0 {
		return nil, ErrInvalidRequest
	}
	if err := s.repo.UpdatePlacement(ctx, placementID, pluginrepo.PlacementUpdate{IsVisible: p.IsVisible, SortOrder: p.SortOrder}); err != nil {
		return nil, mapSceneStoreError(err)
	}
	item, err := s.repo.GetPlacement(ctx, placementID)
	return item, mapSceneStoreError(err)
}

func (s *Scenes) AdminDeletePlacement(ctx context.Context, placementID string) error {
	if strings.TrimSpace(placementID) == "" {
		return ErrInvalidRequest
	}
	return mapSceneStoreError(s.repo.DeletePlacement(ctx, placementID))
}

func (s *Scenes) AdminBatchSetPlacements(ctx context.Context, p PlacementBatchParams) error {
	p.SceneCode = strings.TrimSpace(p.SceneCode)
	if !validSceneCode(p.SceneCode) || len(p.PluginIDs) == 0 || len(p.PluginIDs) > 100 || p.SortOrder < 0 {
		return ErrInvalidRequest
	}
	if p.SceneCode == "default" && (!p.IsPlaced || !p.IsVisible) {
		return ErrConflict
	}
	seen := make(map[string]struct{}, len(p.PluginIDs))
	for i, pluginID := range p.PluginIDs {
		pluginID = strings.TrimSpace(pluginID)
		if pluginID == "" {
			return &BatchItemError{Index: i, Err: ErrInvalidRequest}
		}
		if _, duplicate := seen[pluginID]; duplicate {
			return &BatchItemError{Index: i, Err: ErrInvalidRequest}
		}
		seen[pluginID] = struct{}{}
		p.PluginIDs[i] = pluginID
	}
	items := make([]pluginrepo.PlacementBatchItem, len(p.PluginIDs))
	for i, pluginID := range p.PluginIDs {
		items[i].PluginID = pluginID
		items[i].InputIndex = i
		if p.IsPlaced {
			items[i].PlacementID = s.id()
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].PluginID < items[j].PluginID })
	err := s.repo.BatchSetPlacements(ctx, p.SceneCode, items, p.IsPlaced, p.IsVisible, p.SortOrder)
	var itemErr *pluginrepo.BatchItemError
	if errors.As(err, &itemErr) {
		return &BatchItemError{Index: itemErr.Index, Err: mapSceneStoreError(itemErr.Err)}
	}
	return mapSceneStoreError(err)
}

func validSceneCode(code string) bool { return sceneCodePattern.MatchString(code) }

func validSceneText(name, description string) bool {
	return name != "" && utf8.RuneCountInString(name) <= 128 && utf8.RuneCountInString(description) <= 1024
}

func mapSceneStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pluginrepo.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, pluginrepo.ErrConflict):
		return ErrConflict
	default:
		return err
	}
}
