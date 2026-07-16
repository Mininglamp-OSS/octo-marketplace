package router

import (
	"testing"
)

func TestGenerateID(t *testing.T) {
	id := generateID()
	if len(id) != 36 {
		t.Errorf("generateID() length = %d, want 36", len(id))
	}
	// Check UUID format: 8-4-4-4-12
	if id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Errorf("generateID() = %q, invalid UUID format", id)
	}
	// Check version nibble (position 14 should be '4')
	if id[14] != '4' {
		t.Errorf("generateID() version nibble = %c, want '4'", id[14])
	}

	// Uniqueness
	id2 := generateID()
	if id == id2 {
		t.Error("generateID() produced duplicate IDs")
	}
}
