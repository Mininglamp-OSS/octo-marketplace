package model

import "testing"

func TestMCPViewCountNeverNegativeOnWire(t *testing.T) {
	mcp := MCP{ViewCount: -1}
	if got := mcp.ToListItem().ViewCount; got != 0 {
		t.Fatalf("ToListItem().ViewCount = %d, want 0", got)
	}
	if got := mcp.ToDetail().ViewCount; got != 0 {
		t.Fatalf("ToDetail().ViewCount = %d, want 0", got)
	}
}
