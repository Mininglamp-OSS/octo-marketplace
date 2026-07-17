package redis

import (
	"testing"
)

func TestKeyConstants(t *testing.T) {
	// Verify key prefix constants are set as expected.
	if metricsKeyPrefix != "metrics:" {
		t.Fatalf("expected metricsKeyPrefix = 'metrics:', got %q", metricsKeyPrefix)
	}
	if dirtySetKey != "metrics:dirty" {
		t.Fatalf("expected dirtySetKey = 'metrics:dirty', got %q", dirtySetKey)
	}
}

func TestNewClient_NotNil(t *testing.T) {
	// NewClient should not panic with a nil redis client (for structural test)
	c := NewClient(nil)
	if c == nil {
		t.Fatal("expected non-nil Client")
	}
}
