package expert

import (
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestVersionManagementDisablesLegacyWritesBeforeAnyStoreAccess(t *testing.T) {
	svc := New(nil, nil, nil).WithLegacyWritesDisabled(true)
	ctx := context.Background()
	actions := []func() error{
		func() error { _, err := svc.CreateExpert(ctx, callerA, model.ExpertCreateRequest{}); return err },
		func() error {
			_, err := svc.PatchExpert(ctx, callerA, "expert", model.ExpertPatchRequest{})
			return err
		},
		func() error { _, err := svc.CreateSquad(ctx, callerA, model.SquadCreateRequest{}); return err },
		func() error { _, err := svc.PatchSquad(ctx, callerA, "team", model.SquadPatchRequest{}); return err },
	}
	for i, action := range actions {
		if err := action(); !errors.Is(err, ErrVersionedWriteRequired) {
			t.Fatalf("action %d: %v", i, err)
		}
	}
}

func TestLegacyWritesRemainAvailableWithFlagOff(t *testing.T) {
	svc, _ := newService()
	if _, err := svc.CreateExpert(context.Background(), callerA, baseExpert()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSquad(context.Background(), callerA, baseSquad()); err != nil {
		t.Fatal(err)
	}
}
