package fleet

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCapabilityInstallUsesSharedFleetBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name, baseURL, wantURL string
	}{
		{"direct", "http://fleet:8093", "http://fleet:8093/v1/capabilities/install"},
		{"direct_host_named_fleet", "https://fleet", "https://fleet/v1/capabilities/install"},
		{"gateway", "https://octo.example.test/fleet", "https://octo.example.test/fleet/api/v1/capabilities/install"},
		{"nested_gateway", "https://octo.example.test/octo/fleet", "https://octo.example.test/octo/fleet/api/v1/capabilities/install"},
		{"different_mount", "https://octo.example.test/fleet-service", "https://octo.example.test/fleet-service/v1/capabilities/install"},
		{"nested_direct_path", "https://octo.example.test/fleet/internal", "https://octo.example.test/fleet/internal/v1/capabilities/install"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := New(tc.baseURL)
			var urls []string
			client.http.Transport = capabilityRoundTripper(func(r *http.Request) (*http.Response, error) {
				urls = append(urls, r.URL.String())
				if r.Method != http.MethodPost || r.Header.Get("Token") != "user-token" || r.Header.Get("X-Space-Id") != "space" || r.Header.Get("X-Workspace-Id") != "workspace" {
					t.Error("installation method or forwarded identity changed")
				}
				body := `{"id":"legacy-resource"}`
				if len(urls) == 1 {
					if r.Header.Get("Idempotency-Key") != "operation-key" || r.GetBody != nil {
						t.Error("capability idempotency or no-automatic-retry contract changed")
					}
					body = expertEnvelopeJSON
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			if _, err := client.InstallCapability(context.Background(), "user-token", "space", "workspace", "operation-key", capabilityRequest()); err != nil {
				t.Fatal(err)
			}
			if len(urls) != 1 || urls[0] != tc.wantURL {
				t.Fatalf("capability requests = %v, want one request to %s", urls, tc.wantURL)
			}
			// The same configured client must retain every legacy create path.
			if _, err := client.CreateAgent(context.Background(), "user-token", "space", "workspace", AgentSpec{}); err != nil {
				t.Fatal(err)
			}
			if _, err := client.CreateSkill(context.Background(), "user-token", "space", "workspace", SkillSpec{}); err != nil {
				t.Fatal(err)
			}
			if _, err := client.CreateSquad(context.Background(), "user-token", "space", "workspace", SquadSpec{}); err != nil {
				t.Fatal(err)
			}
			for i, path := range []string{"/api/agents", "/api/skills", "/api/squads"} {
				if got, want := urls[i+1], tc.baseURL+path; got != want {
					t.Errorf("legacy URL = %s, want %s", got, want)
				}
			}
		})
	}
}
