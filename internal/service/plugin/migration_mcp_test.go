package plugin

import "testing"

func TestMigrationMCPRejectsDuplicateCredentialFields(t *testing.T) {
	if err := ValidateMigrationMCP(`{"mcpServers":{"search":{"command":"search","env":{"TOKEN":""}}}}`); err != nil {
		t.Fatalf("valid control rejected: %v", err)
	}
	for _, raw := range []string{
		`{"mcpServers":{"search":{"command":"search","env":{"TOKEN":"credential-must-not-survive","TOKEN":""}}}}`,
		`{"mcpServers":{"search":{"command":"first"},"search":{"command":"second"}}}`,
	} {
		if err := ValidateMigrationMCP(raw); err == nil {
			t.Fatal("ambiguous migration configuration accepted")
		}
	}
}
