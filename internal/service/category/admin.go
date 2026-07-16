package category

import (
	"context"
	"errors"
)

// ErrCategoryInUse indicates the category still has skills and cannot be deleted.
var ErrCategoryInUse = errors.New("category in use")

// ErrCategoryNotFound indicates the category was not found.
var ErrCategoryNotFound = errors.New("category not found")

// AdminItem is the API-facing representation of a category for admin operations.
type AdminItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IconKey   string `json:"icon_key"`
	SortOrder int    `json:"sort_order"`
}

// Create creates a new category.
func (s *Service) Create(ctx context.Context, id, name, iconKey string, sortOrder int) (*AdminItem, error) {
	if err := s.repo.Create(ctx, id, name, iconKey, sortOrder); err != nil {
		return nil, err
	}
	return &AdminItem{
		ID:        id,
		Name:      name,
		IconKey:   iconKey,
		SortOrder: sortOrder,
	}, nil
}

// Update modifies an existing category.
func (s *Service) Update(ctx context.Context, id, name, iconKey string, sortOrder int) (*AdminItem, error) {
	affected, err := s.repo.Update(ctx, id, name, iconKey, sortOrder)
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrCategoryNotFound
	}
	return &AdminItem{
		ID:        id,
		Name:      name,
		IconKey:   iconKey,
		SortOrder: sortOrder,
	}, nil
}

// Delete deletes a category. Returns ErrCategoryInUse if skills exist in the category.
func (s *Service) Delete(ctx context.Context, id string) (int, error) {
	count, err := s.repo.SkillCountByCategory(ctx, id)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		return count, ErrCategoryInUse
	}
	affected, err := s.repo.Delete(ctx, id)
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		return 0, ErrCategoryNotFound
	}
	return 0, nil
}
