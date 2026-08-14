package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
)

type fakeExpertService struct {
	experts map[string]*model.ExpertAgentDetail
	squads  map[string]*model.ExpertSquadDetail
	err     error
}

func (f *fakeExpertService) GetExpert(_ context.Context, _ expertsvc.Caller, id string) (*model.ExpertAgentDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	item, ok := f.experts[id]
	if !ok {
		return nil, expertsvc.ErrNotFound
	}
	return item, nil
}

func (f *fakeExpertService) GetSquad(_ context.Context, _ expertsvc.Caller, id string) (*model.ExpertSquadDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	item, ok := f.squads[id]
	if !ok {
		return nil, expertsvc.ErrNotFound
	}
	return item, nil
}

func TestExpertResolver_CanView_Exists(t *testing.T) {
	svc := &fakeExpertService{
		experts: map[string]*model.ExpertAgentDetail{
			"expert-1": {ExpertID: "expert-1"},
		},
	}
	resolver := NewExpertResolver(svc)
	caller := Caller{UID: "user-1", SpaceID: "space-1"}

	ok, err := resolver.CanView(context.Background(), "expert-1", caller)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected CanView to return true for existing expert")
	}
}

func TestExpertResolver_CanView_NotFound(t *testing.T) {
	svc := &fakeExpertService{experts: map[string]*model.ExpertAgentDetail{}}
	resolver := NewExpertResolver(svc)
	caller := Caller{UID: "user-1", SpaceID: "space-1"}

	ok, err := resolver.CanView(context.Background(), "nonexistent", caller)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected CanView to return false for nonexistent expert")
	}
}

func TestExpertResolver_CanView_InternalError(t *testing.T) {
	dbErr := errors.New("database connection failed")
	svc := &fakeExpertService{err: dbErr}
	resolver := NewExpertResolver(svc)
	caller := Caller{UID: "user-1", SpaceID: "space-1"}

	ok, err := resolver.CanView(context.Background(), "expert-1", caller)
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected original error, got: %v", err)
	}
	if ok {
		t.Fatal("expected CanView to return false on internal error")
	}
}

func TestSquadResolver_CanView_Exists(t *testing.T) {
	svc := &fakeExpertService{
		squads: map[string]*model.ExpertSquadDetail{
			"squad-1": {SquadID: "squad-1"},
		},
	}
	resolver := NewSquadResolver(svc)
	caller := Caller{UID: "user-1", SpaceID: "space-1"}

	ok, err := resolver.CanView(context.Background(), "squad-1", caller)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected CanView to return true for existing squad")
	}
}

func TestSquadResolver_CanView_NotFound(t *testing.T) {
	svc := &fakeExpertService{squads: map[string]*model.ExpertSquadDetail{}}
	resolver := NewSquadResolver(svc)
	caller := Caller{UID: "user-1", SpaceID: "space-1"}

	ok, err := resolver.CanView(context.Background(), "nonexistent", caller)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected CanView to return false for nonexistent squad")
	}
}
