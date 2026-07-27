package category

import (
	"context"
	"strings"

	categoryrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/category"
	skillrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/skill"
)

// Service handles business logic for categories.
type Service struct {
	repo      *categoryrepo.Repo
	skillRepo *skillrepo.Repo
}

// New creates a category service.
func New(repo *categoryrepo.Repo, skillRepo *skillrepo.Repo) *Service {
	return &Service{repo: repo, skillRepo: skillRepo}
}

// CategoryItem is the API-facing representation of a category.
type CategoryItem struct {
	ID         string `json:"skill_category_id"`
	Name       string `json:"name"`
	IconKey    string `json:"icon_key"`
	SkillCount int    `json:"skill_count"`
}

// ListParams are the parameters for listing categories and filtered skill counts.
type ListParams struct {
	SpaceID string
	UserID  string
	Query   string
	Tags    []string
}

// List returns all categories with skill counts for the given space/user.
func (s *Service) List(ctx context.Context, p ListParams) ([]CategoryItem, error) {
	tags := normalizeTags(p.Tags)
	tagIDGroups, err := s.resolveListTagIDGroups(ctx, p.SpaceID, tags)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListWithCount(ctx, categoryrepo.ListFilter{
		SpaceID:            p.SpaceID,
		UserID:             p.UserID,
		Query:              strings.TrimSpace(p.Query),
		TagIDGroups:        tagIDGroups,
		TagFilterUnmatched: len(tags) > 0 && len(tagIDGroups) == 0,
	})
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

func (s *Service) resolveListTagIDGroups(ctx context.Context, spaceID string, tags []string) ([][]int64, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	groups, err := s.skillRepo.ResolveFilterTagIDs(ctx, spaceID, tags)
	if err != nil {
		return nil, err
	}
	merged := mergeTagIDGroups(groups)
	if len(merged) == 0 {
		return nil, nil
	}
	return [][]int64{merged}, nil
}

func mergeTagIDGroups(groups [][]int64) []int64 {
	seen := make(map[int64]struct{})
	var merged []int64
	for _, group := range groups {
		for _, id := range group {
			if id <= 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			merged = append(merged, id)
		}
	}
	return merged
}

func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

// Exists checks if a category exists.
func (s *Service) Exists(ctx context.Context, id string) (bool, error) {
	return s.repo.Exists(ctx, id)
}
