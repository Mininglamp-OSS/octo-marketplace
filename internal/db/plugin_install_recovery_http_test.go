package db

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type recoveryHTTPInstaller struct {
	calls int
	err   error
	empty bool
}

func (f *recoveryHTTPInstaller) InstallCapability(context.Context, string, string, string, string, fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	f.calls++
	if f.empty || f.err != nil {
		return nil, f.err
	}
	return &fleet.CapabilityInstallResult{ExpertID: "created"}, nil
}

// Real HTTP handler → real Service.Install → real MySQL store → Fleet seam.
// Do not inject an already-classified InstallationAttemptError into the handler.
func TestInstallRecoveryHTTPAcrossContentModes(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "pinned"}[pinned], func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			remote := &recoveryHTTPInstaller{}
			svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(pinned).WithPinnedInstallation(pinned).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
			root, err := svc.Create(ctx, managedOwner(), managedWrite("1.0.0", "released instructions", model.PluginVisibilityPrivate))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.Publish(ctx, managedOwner(), explicitTestPublication(t, database, root.Plugin.ID)); err != nil {
				t.Fatal(err)
			}
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Owner"}, "space-a")
			pluginhandler.New(svc).Register(engine.Group("/api/v1", auth.Handler()))
			for _, tc := range []struct {
				name           string
				err            error
				empty, unknown bool
				status         int
			}{
				{"timeout", context.DeadlineExceeded, false, true, 503},
				{"missing-result", nil, true, true, 503},
				{"upstream-500", &fleet.CapabilityAPIError{Status: 500}, false, true, 503},
				{"upstream-timeout", &fleet.CapabilityAPIError{Status: 408}, false, true, 503},
				{"upstream-busy", &fleet.CapabilityAPIError{Status: 429}, false, false, 429},
				{"forbidden", &fleet.CapabilityAPIError{Status: 403}, false, false, 403},
				{"invalid", &fleet.CapabilityAPIError{Status: 400}, false, false, 400},
			} {
				for _, keyed := range []bool{false, true} {
					t.Run(tc.name+map[bool]string{false: "/keyless", true: "/keyed"}[keyed], func(t *testing.T) {
						remote.err, remote.empty = tc.err, tc.empty
						before := remote.calls
						body, _ := json.Marshal(map[string]string{"plugin_id": root.Plugin.ID, "workspace_id": "workspace-1", "runtime_id": "old-runtime-id"})
						req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/install", strings.NewReader(string(body)))
						req.Header.Set("Content-Type", "application/json")
						if keyed {
							req.Header.Set("Idempotency-Key", "test-"+tc.name)
						}
						rec := httptest.NewRecorder()
						engine.ServeHTTP(rec, req)
						if rec.Code != tc.status || remote.calls != before+1 {
							t.Fatalf("status=%d calls=%d body=%s", rec.Code, remote.calls-before, rec.Body.String())
						}
						var response struct {
							Error struct {
								Details map[string]any `json:"details"`
								Hint    string         `json:"hint"`
							} `json:"error"`
						}
						if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
							t.Fatal(err)
						}
						if tc.unknown {
							if response.Error.Details["reason"] != "installation_result_unknown" || response.Error.Details["can_retry_same_key"] != keyed {
								t.Fatalf("wrong recovery contract: %s", rec.Body.String())
							}
							if !keyed && !strings.Contains(response.Error.Hint, "先核对工作区已有资源") {
								t.Fatalf("keyless retry warning missing: %s", rec.Body.String())
							}
						} else if response.Error.Details["reason"] == "installation_result_unknown" {
							t.Fatal("definite rejection was classified as unknown")
						}
					})
				}
			}
		})
	}
}
