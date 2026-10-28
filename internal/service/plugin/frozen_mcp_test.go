package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFrozenInstallRejectsConnectorCredentialsBeforePersistence(t *testing.T) {
	configs := map[string]string{
		"authorization":    `{"mcpServers":{"search":{"url":"https://example.test/mcp","headers":{"Authorization":"Bearer synthetic-private-value"}}}}`,
		"custom_header":    `{"mcpServers":{"search":{"url":"https://example.test/mcp","headers":{"X-Custom":"synthetic-private-value"}}}}`,
		"env":              `{"mcpServers":{"search":{"command":"search","env":{"TOKEN":"synthetic-private-value"}}}}`,
		"url_userinfo":     `{"mcpServers":{"search":{"url":"https://user:synthetic-private-value@example.test/mcp"}}}`,
		"url_query":        `{"mcpServers":{"search":{"url":"https://example.test/mcp?apiKey=synthetic-private-value"}}}`,
		"argument":         `{"mcpServers":{"search":{"command":"search","args":["--client-secret","synthetic-private-value"]}}}`,
		"url_fragment":     `{"mcpServers":{"search":{"url":"https://example.test/mcp#access_token=synthetic-private-value"}}}`,
		"duplicate_header": `{"mcpServers":{"search":{"url":"https://example.test/mcp","headers":{"Authorization":"synthetic-private-value","Authorization":""}}}}`,
		"argument_equals":  `{"mcpServers":{"search":{"command":"search","args":["--token=synthetic-private-value"]}}}`,
	}
	for _, adapter := range []string{"new", "legacy"} {
		for name, config := range configs {
			t.Run(adapter+"/"+name, func(t *testing.T) {
				s, f, remote := fixturePinned()
				f.plugins["connector"].Package = packageWith(rawAtt("mcp.json", config))
				err := invokeIntegrityInstall(s, adapter, installationParams())
				var invalid *InstallationValidationError
				if !errors.As(err, &invalid) || remote.calls != 0 || len(f.attempts) != 0 {
					t.Fatalf("unsafe config persisted/dispatched: err=%v calls=%d attempts=%d", err, remote.calls, len(f.attempts))
				}
				if strings.Contains(err.Error()+invalid.Field+invalid.Reason, "synthetic-private-value") {
					t.Fatal("credential leaked through validation")
				}
			})
		}
	}
}

func TestFrozenRetryRejectsPreviouslyPersistedCredentialWithoutMutation(t *testing.T) {
	for _, adapter := range []string{"new", "legacy"} {
		for _, completed := range []bool{false, true} {
			t.Run(adapter+map[bool]string{false: "/unknown", true: "/completed"}[completed], func(t *testing.T) {
				s, f, remote := fixturePinned()
				p := installationParams()
				if !completed {
					remote.err = context.DeadlineExceeded
				}
				err := invokeIntegrityInstall(s, adapter, p)
				if (completed && err != nil) || (!completed && err == nil) {
					t.Fatal(err)
				}
				a := f.attempts[p.WorkspaceID+digestInstall(p.IdempotencyKey)]
				var plan pinnedInstallationPlan
				if err = json.Unmarshal(a.FrozenPlan, &plan); err != nil {
					t.Fatal(err)
				}
				raw := json.RawMessage(`{"mcpServers":{"search":{"url":"https://example.test/mcp","headers":{"Authorization":"Bearer historical-private-value"}}}}`)
				plan.Definition.Experts[0].MCPConfig = raw
				// Model a legitimate pre-fix row: both HMACs are valid, not a tampering failure.
				request := remote.in
				request.Definition.Experts[0].MCPConfig = raw
				a.RequestFingerprint, err = s.installationHMAC(a.HMACKeyID, "request", request)
				if err != nil {
					t.Fatal(err)
				}
				plan.EligibilityHMAC, err = s.installationEligibilityHMAC(testCaller, *a, &plan)
				if err != nil {
					t.Fatal(err)
				}
				a.FrozenPlan, err = json.Marshal(plan)
				if err != nil {
					t.Fatal(err)
				}
				before, _ := json.Marshal(a)
				remote.err = nil
				err = invokeIntegrityInstall(s, adapter, p)
				after, _ := json.Marshal(a)
				var invalid *InstallationValidationError
				if !errors.As(err, &invalid) || invalid.Reason != "credential_not_persistable" || remote.calls != 1 || string(before) != string(after) {
					t.Fatalf("unsafe legacy plan replayed/mutated: err=%v calls=%d unchanged=%t", err, remote.calls, string(before) == string(after))
				}
			})
		}
	}
}

func TestFrozenInstallPreservesCredentialFreeMCP(t *testing.T) {
	for _, adapter := range []string{"new", "legacy"} {
		t.Run(adapter, func(t *testing.T) {
			s, f, remote := fixturePinned()
			p := installationParams()
			f.plugins["connector"].Package = packageWith(rawAtt("mcp.json", `{"mcpServers":{"search":{"command":"search","args":["--region=eu","--token=${API_TOKEN}"],"env":{"TOKEN":"${API_TOKEN}","EMPTY":""}}}}`))
			remote.err = context.DeadlineExceeded
			_ = invokeIntegrityInstall(s, adapter, p)
			if remote.calls != 1 || len(f.attempts) != 1 {
				t.Fatal("safe references rejected")
			}
			before, _ := json.Marshal(remote.in)
			key := remote.key
			remote.err = nil
			// Configuration changes must not rebuild even a previously credential-free plan.
			f.plugins["connector"].Package = packageWith(rawAtt("mcp.json", `{"mcpServers":{"search":{"command":"new-command"}}}`))
			if err := invokeIntegrityInstall(s, adapter, p); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(remote.in)
			if string(before) != string(after) || remote.key != key {
				t.Fatal("safe replay changed")
			}
			for _, a := range f.attempts {
				if strings.Contains(string(a.FrozenPlan), p.Token) {
					t.Fatal("runtime credential persisted")
				}
			}
		})
	}
}
