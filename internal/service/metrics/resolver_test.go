package metrics

import (
	"context"
	"testing"
)

func TestRegisterAndGetResolver(t *testing.T) {
	ResetResolvers()
	defer ResetResolvers()

	// Initially no resolver
	_, ok := GetResolver("skill")
	if ok {
		t.Fatal("expected no resolver for 'skill' before registration")
	}

	// Register
	mock := &testResolver{canView: true}
	RegisterResolver("skill", mock)

	r, ok := GetResolver("skill")
	if !ok {
		t.Fatal("expected resolver for 'skill' after registration")
	}
	if r != mock {
		t.Fatal("expected same resolver instance")
	}

	// Unknown type
	_, ok = GetResolver("unknown")
	if ok {
		t.Fatal("expected no resolver for 'unknown'")
	}
}

func TestResolverOverwrite(t *testing.T) {
	ResetResolvers()
	defer ResetResolvers()

	r1 := &testResolver{canView: true}
	r2 := &testResolver{canView: false}

	RegisterResolver("skill", r1)
	RegisterResolver("skill", r2)

	r, ok := GetResolver("skill")
	if !ok {
		t.Fatal("expected resolver")
	}
	if r != r2 {
		t.Fatal("expected second resolver to overwrite first")
	}
}

type testResolver struct {
	canView bool
	err     error
}

func (m *testResolver) CanView(_ context.Context, _ string, _ Caller) (bool, error) {
	return m.canView, m.err
}
