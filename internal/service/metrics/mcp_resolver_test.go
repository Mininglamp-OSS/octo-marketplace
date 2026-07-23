package metrics

import (
	"context"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	marketplacesvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service"
)

type fakeMCPService struct {
	detail model.Detail
	err    *apierr.Error
	caller marketplacesvc.Caller
}

func (f *fakeMCPService) Get(_ context.Context, caller marketplacesvc.Caller, _ string) (model.Detail, *apierr.Error) {
	f.caller = caller
	return f.detail, f.err
}

func TestMCPResolverCanView(t *testing.T) {
	svc := &fakeMCPService{detail: model.Detail{ID: "mcp-1"}}
	ok, err := NewMCPResolver(svc).CanView(context.Background(), "mcp-1", Caller{UID: "user-1", SpaceID: "space-1"})
	if err != nil || !ok {
		t.Fatalf("CanView() = %v, %v; want true, nil", ok, err)
	}
	if svc.caller.UID != "user-1" || svc.caller.SpaceID != "space-1" {
		t.Fatalf("caller = %#v", svc.caller)
	}
}

func TestMCPResolverHidesNotFound(t *testing.T) {
	svc := &fakeMCPService{err: apierr.NotFound()}
	ok, err := NewMCPResolver(svc).CanView(context.Background(), "missing", Caller{})
	if err != nil || ok {
		t.Fatalf("CanView() = %v, %v; want false, nil", ok, err)
	}
}

func TestMCPResolverPropagatesInternalError(t *testing.T) {
	svc := &fakeMCPService{err: apierr.Internal()}
	ok, err := NewMCPResolver(svc).CanView(context.Background(), "mcp-1", Caller{})
	if err == nil || ok {
		t.Fatalf("CanView() = %v, %v; want false, error", ok, err)
	}
}
