package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/config"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
)

// Exercise configuration -> unmodified Fleet constructor -> service -> HTTP.
// Old deployment manifests must not silently disable the new route anymore.
func TestCapabilityInstallationNeedsNoEnableFlag(t *testing.T) {
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Run("former_flag_"+value, func(t *testing.T) {
			t.Setenv("OCTO_FLEET_CAPABILITY_INSTALL_ENABLED", value)
			if value == "" {
				if err := os.Unsetenv("OCTO_FLEET_CAPABILITY_INSTALL_ENABLED"); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/capabilities/install" {
					t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL.Path)
				}
				for header, want := range map[string]string{
					"Token": "user-token", "X-Space-ID": testCaller.SpaceID,
					"X-Workspace-ID": "workspace-1", "Idempotency-Key": "operation-key",
				} {
					if r.Header.Get(header) != want {
						t.Errorf("%s was not forwarded", header)
					}
				}
				out := fleet.CapabilityInstallResult{
					Type: "expert", ExpertID: "created-expert",
					Experts: []fleet.CapabilityExpertResult{{ExpertID: "created-expert", Name: "Custom name"}},
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"data": out}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			t.Setenv("OCTO_FLEET_URL", server.URL)
			cfg := config.Load()
			svc, store, _ := capabilityFixture()
			store.relations["expert-1"] = nil
			svc.WithCapabilityInstaller(fleet.New(cfg.OctoFleetURL))
			out, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", installationParams())
			if err != nil || out == nil || out.AgentID != "created-expert" || calls != 1 {
				t.Fatalf("out=%+v err=%v calls=%d", out, err, calls)
			}
		})
	}
}
