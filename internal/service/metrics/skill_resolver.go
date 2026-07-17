package metrics

import (
	"context"

	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
)

// SkillService is the subset of the skill service needed for visibility checks.
type SkillService interface {
	Get(ctx context.Context, id, spaceID, userID string) (*skillsvc.SkillItem, error)
}

// SkillResolver checks whether a skill exists and is visible to the caller.
type SkillResolver struct {
	skillSvc SkillService
}

// NewSkillResolver creates a SkillResolver.
func NewSkillResolver(skillSvc SkillService) *SkillResolver {
	return &SkillResolver{skillSvc: skillSvc}
}

// CanView returns true if the skill exists and is visible to the caller.
func (r *SkillResolver) CanView(ctx context.Context, resourceID string, caller Caller) (bool, error) {
	item, err := r.skillSvc.Get(ctx, resourceID, caller.SpaceID, caller.UID)
	if err != nil {
		return false, nil // skill not found or not visible — treat as "cannot view"
	}
	return item != nil, nil
}
