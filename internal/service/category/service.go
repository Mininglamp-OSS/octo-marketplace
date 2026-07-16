package category

import (
	"context"

	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
)

// Service handles business logic for categories.
type Service struct {
	repo *categoryrepo.Repo
}

// New creates a category service.
func New(repo *categoryrepo.Repo) *Service {
	return &Service{repo: repo}
}

// CategoryItem is the API-facing representation of a category.
type CategoryItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IconKey    string `json:"icon_key"`
	SkillCount int    `json:"skill_count"`
}

// List returns all categories with skill counts for the given space/user.
func (s *Service) List(ctx context.Context, spaceID, userID string) ([]CategoryItem, error) {
	rows, err := s.repo.ListWithCount(ctx, spaceID, userID)
	if err != nil {
		return nil, err
	}
	items := make([]CategoryItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, CategoryItem{
			ID:         row.ID,
			Name:       row.Name,
			IconKey:    row.IconKey,
			SkillCount: row.SkillCount,
		})
	}
	return items, nil
}

// Exists checks if a category exists.
func (s *Service) Exists(ctx context.Context, id string) (bool, error) {
	return s.repo.Exists(ctx, id)
}
