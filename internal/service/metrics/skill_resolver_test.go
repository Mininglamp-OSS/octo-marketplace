package metrics

import (
	"context"
	"errors"
	"testing"

	skillsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/skill"
)

type fakeSkillService struct {
	items map[string]*skillsvc.SkillItem
	err   error
}

func (f *fakeSkillService) Get(_ context.Context, id, spaceID, userID string) (*skillsvc.SkillItem, error) {
	if f.err != nil {
		return nil, f.err
	}
	item, ok := f.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return item, nil
}

func TestSkillResolver_CanView_Exists(t *testing.T) {
	svc := &fakeSkillService{
		items: map[string]*skillsvc.SkillItem{
			"skill-1": {ID: "skill-1"},
		},
	}
	resolver := NewSkillResolver(svc)
	caller := Caller{UID: "user-1", SpaceID: "space-1"}

	ok, err := resolver.CanView(context.Background(), "skill-1", caller)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected CanView to return true for existing skill")
	}
}

func TestSkillResolver_CanView_NotFound(t *testing.T) {
	svc := &fakeSkillService{
		items: map[string]*skillsvc.SkillItem{},
	}
	resolver := NewSkillResolver(svc)
	caller := Caller{UID: "user-1", SpaceID: "space-1"}

	ok, err := resolver.CanView(context.Background(), "nonexistent", caller)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected CanView to return false for nonexistent skill")
	}
}

func TestSkillResolver_CanView_ServiceError(t *testing.T) {
	svc := &fakeSkillService{
		err: errors.New("internal error"),
	}
	resolver := NewSkillResolver(svc)
	caller := Caller{UID: "user-1", SpaceID: "space-1"}

	ok, err := resolver.CanView(context.Background(), "skill-1", caller)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected CanView to return false on service error")
	}
}
