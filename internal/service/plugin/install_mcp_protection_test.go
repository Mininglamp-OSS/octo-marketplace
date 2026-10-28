package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
)

const protectedMCPFixture = `{"mcpServers":{"remote":{"url":"https://example.invalid/mcp?key=synthetic-url-credential","headers":{"Authorization":"Bearer synthetic-header-credential"}},"local":{"command":"worker","args":["--key=synthetic-argument-credential"],"env":{"TOKEN":"synthetic-env-credential"}}}}`

func TestFrozenMCPProtectionPreservesRetryBothAdapters(t *testing.T) {
	for _, adapter := range []string{"new", "legacy"} {
		for _, completed := range []bool{false, true} {
			name := adapter + "/unknown"
			if completed {
				name = adapter + "/completed"
			}
			t.Run(name, func(t *testing.T) {
				s, f, remote := fixturePinned()
				p := installationParams()
				p.ResourceName = ""
				f.plugins["connector"].Package = packageWith(rawAtt("mcp.json", protectedMCPFixture))
				if !completed {
					remote.err = context.DeadlineExceeded
				}
				err := invokeIntegrityInstall(s, adapter, p)
				if (completed && err != nil) || (!completed && err == nil) || remote.calls != 1 {
					t.Fatalf("initial err=%v calls=%d", err, remote.calls)
				}
				a := f.attempts[p.WorkspaceID+digestInstall(p.IdempotencyKey)]
				for _, secret := range []string{"synthetic-url-credential", "synthetic-header-credential", "synthetic-argument-credential", "synthetic-env-credential"} {
					if strings.Contains(string(a.FrozenPlan), secret) {
						t.Fatal("Connector credential persisted in plaintext")
					}
				}
				var plan pinnedInstallationPlan
				if err := json.Unmarshal(a.FrozenPlan, &plan); err != nil {
					t.Fatal(err)
				}
				for _, e := range plan.Definition.Experts {
					if len(e.MCPConfig) != 0 {
						t.Fatal("plaintext definition retained MCP")
					}
				}
				original := append([]byte(nil), a.FrozenPlan...)
				firstKey := remote.key
				firstBody, _ := json.Marshal(remote.in)
				f.plugins["connector"].Package = packageWith(rawAtt("mcp.json", `{"mcpServers":{"changed":{"command":"latest-content"}}}`))
				// The original key must remain usable across deployment key rotation.
				s.WithInstallationHMACKeys("new", map[string]string{"test": "synthetic-installation-test-key-32-bytes", "new": strings.Repeat("n", 32)})
				remote.err = nil
				if err = invokeIntegrityInstall(s, adapter, p); err != nil {
					t.Fatal(err)
				}
				secondBody, _ := json.Marshal(remote.in)
				if remote.calls != 2 || remote.key != firstKey || string(firstBody) != string(secondBody) || string(a.FrozenPlan) != string(original) {
					t.Fatal("protected retry changed original key/content/plan")
				}
			})
		}
	}
}

func TestFrozenMCPProtectionRejectsCorruptionBothAdapters(t *testing.T) {
	for _, adapter := range []string{"new", "legacy"} {
		for _, mode := range []string{"missing", "tampered", "plaintext", "other_operation"} {
			t.Run(adapter+"/"+mode, func(t *testing.T) {
				s, f, remote := fixturePinned()
				p := installationParams()
				p.ResourceName = ""
				remote.err = context.DeadlineExceeded
				f.plugins["connector"].Package = packageWith(rawAtt("mcp.json", protectedMCPFixture))
				if err := invokeIntegrityInstall(s, adapter, p); err == nil {
					t.Fatal("initial uncertainty missing")
				}
				a := f.attempts[p.WorkspaceID+digestInstall(p.IdempotencyKey)]
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(a.FrozenPlan, &fields); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "missing":
					delete(fields, "ProtectedMCPConfigs")
				case "tampered":
					fields["ProtectedMCPConfigs"] = json.RawMessage(`"dGFtcGVyZWQ="`)
				case "plaintext":
					var def fleet.CapabilityDefinition
					if err := json.Unmarshal(fields["Definition"], &def); err != nil {
						t.Fatal(err)
					}
					def.Experts[0].MCPConfig = json.RawMessage(protectedMCPFixture)
					fields["Definition"], _ = json.Marshal(def)
				case "other_operation":
					other := p
					other.IdempotencyKey = "different-operation"
					if err := invokeIntegrityInstall(s, adapter, other); err == nil {
						t.Fatal("second uncertainty missing")
					}
					var otherFields map[string]json.RawMessage
					_ = json.Unmarshal(f.attempts[p.WorkspaceID+digestInstall(other.IdempotencyKey)].FrozenPlan, &otherFields)
					fields["ProtectedMCPConfigs"] = otherFields["ProtectedMCPConfigs"]
				}
				a.FrozenPlan, _ = json.Marshal(fields)
				before, _ := json.Marshal(a)
				calls := remote.calls
				remote.err = nil
				err := invokeIntegrityInstall(s, adapter, p)
				after, _ := json.Marshal(a)
				if !errors.Is(err, ErrIntegrity) || remote.calls != calls || string(before) != string(after) {
					t.Fatalf("invalid protected plan not preserved: err=%v calls=%d", err, remote.calls)
				}
			})
		}
	}
}
