package skill

import (
	"testing"
	"time"
)

func TestParseCursor(t *testing.T) {
	now := time.Now().UTC()
	cursor := buildCursor(now, "abc-123")
	parsedTime, parsedID, err := parseCursor(cursor)
	if err != nil {
		t.Fatalf("parseCursor: %v", err)
	}
	if !parsedTime.Equal(now) {
		t.Errorf("time mismatch: got %v, want %v", parsedTime, now)
	}
	if parsedID != "abc-123" {
		t.Errorf("id mismatch: got %q, want %q", parsedID, "abc-123")
	}
}

func TestParseCursorInvalid(t *testing.T) {
	_, _, err := parseCursor("invalid")
	if err == nil {
		t.Error("expected error for invalid cursor")
	}

	_, _, err = parseCursor("not-a-time,some-id")
	if err == nil {
		t.Error("expected error for non-RFC3339 time")
	}
}

func TestBuildCursor(t *testing.T) {
	ts := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	cursor := buildCursor(ts, "test-id")
	if cursor == "" {
		t.Error("expected non-empty cursor")
	}
	// Should be parseable
	parsedTime, parsedID, err := parseCursor(cursor)
	if err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	if !parsedTime.Equal(ts) {
		t.Errorf("time mismatch after round-trip")
	}
	if parsedID != "test-id" {
		t.Errorf("id mismatch after round-trip")
	}
}

func TestEscapeLike(t *testing.T) {
	got := escapeLike(`100%_match\literal`)
	want := `100\%\_match\\literal`
	if got != want {
		t.Fatalf("escapeLike() = %q, want %q", got, want)
	}
}
