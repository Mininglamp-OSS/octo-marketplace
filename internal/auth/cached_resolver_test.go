package auth

import (
	"context"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type countingResolver struct{ calls int }

func (r *countingResolver) Resolve(context.Context, string) (model.Identity, error) {
	r.calls++
	return model.Identity{UID: "user-1"}, nil
}

func TestCachedResolverCachesIdentity(t *testing.T) {
	inner := &countingResolver{}
	resolver := NewCachedResolver(inner, time.Minute, 10)
	for range 2 {
		if _, err := resolver.Resolve(context.Background(), "token"); err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("calls=%d want=1", inner.calls)
	}
}
