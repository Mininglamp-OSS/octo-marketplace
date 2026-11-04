package plugin

import (
	"context"
	"errors"
	"testing"
)

func TestDependencyUpdateRequiresSourceAndDraftConditions(t *testing.T) {
	r := New(nil)
	for _, p := range []DependencyUpdatePublishParams{{}, {ExpectedPublishedVersionID: "release"}, {ExpectedDraftRevision: "revision"}} {
		if _, err := r.SubmitDependencyUpdates(context.Background(), Scope{}, p); !errors.Is(err, ErrConflict) {
			t.Fatalf("unguarded submit: %v", err)
		}
	}
}
