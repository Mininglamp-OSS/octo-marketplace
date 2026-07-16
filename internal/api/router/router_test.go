package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/service"
	"github.com/gin-gonic/gin"
)

type stubPinger struct{ err error }

func (p stubPinger) PingContext(context.Context) error { return p.err }

func init() { gin.SetMode(gin.TestMode) }

func testAuthenticator() *marketmiddleware.Authenticator {
	return marketmiddleware.NewAuthenticator(false, nil, model.Identity{UID: "dev-user", Name: "Developer"}, "dev-space")
}

func testAdminAuthenticator() *marketmiddleware.AdminAuthenticator {
	return marketmiddleware.NewAdminAuthenticator(false, "", model.Identity{UID: "dev-user", Name: "Developer"})
}

func testStorageConfig() StorageConfig {
	return StorageConfig{Driver: "local", LocalDir: "/tmp/marketplace-test-uploads", BaseURL: "http://127.0.0.1:8092", MaxMB: 20}
}

// testHandler is an MCP handler with a nil service; the health/session tests
// below never reach an MCP route, so the service is never invoked.
func testHandler() *handler.MCP {
	return handler.NewMCP(nil)
}

func testAdminHandler() *handler.AdminMCP {
	return handler.NewAdminMCP(nil)
}

// reachedService is a stub MCPService whose List reports whether it was
// invoked, so a router test can assert that a given path actually reaches an
// MCP handler (route matched) rather than 404ing (route missing).
type reachedService struct{ listed bool }

func (s *reachedService) Create(context.Context, service.Caller, model.CreateRequest) (model.Detail, *apierr.Error) {
	return model.Detail{}, nil
}
func (s *reachedService) Get(context.Context, service.Caller, string) (model.Detail, *apierr.Error) {
	return model.Detail{}, nil
}
func (s *reachedService) Patch(context.Context, service.Caller, string, model.PatchRequest) (model.Detail, *apierr.Error) {
	return model.Detail{}, nil
}
func (s *reachedService) Delete(context.Context, service.Caller, string) *apierr.Error { return nil }
func (s *reachedService) List(context.Context, service.Caller, service.ListParams) (model.ListResponse, *apierr.Error) {
	s.listed = true
	return model.ListResponse{}, nil
}
func (s *reachedService) ListMine(context.Context, service.Caller, service.ListParams) (model.ListResponse, *apierr.Error) {
	return model.ListResponse{}, nil
}
func (s *reachedService) Probe(context.Context, service.ProbeRequest) (service.ProbeResponse, *apierr.Error) {
	return service.ProbeResponse{OK: true, Tools: []model.Tool{}}, nil
}
func (s *reachedService) UploadIcon(context.Context, service.Caller, string, []byte, string) (service.IconResult, *apierr.Error) {
	return service.IconResult{}, nil
}

func TestHealthz(t *testing.T) {
	recorder := httptest.NewRecorder()
	Public(stubPinger{}, testAuthenticator(), testAdminAuthenticator(), testStorageConfig(), testHandler(), testAdminHandler()).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d", recorder.Code, http.StatusOK)
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "ready", want: http.StatusOK},
		{name: "database unavailable", err: errors.New("down"), want: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			Public(stubPinger{err: tt.err}, testAuthenticator(), testAdminAuthenticator(), testStorageConfig(), testHandler(), testAdminHandler()).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if recorder.Code != tt.want {
				t.Fatalf("status=%d want=%d", recorder.Code, tt.want)
			}
		})
	}
}

func TestSessionUsesDevelopmentIdentity(t *testing.T) {
	recorder := httptest.NewRecorder()
	Public(stubPinger{}, testAuthenticator(), testAdminAuthenticator(), testStorageConfig(), testHandler(), testAdminHandler()).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/session", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d", recorder.Code, http.StatusOK)
	}
	if body := recorder.Body.String(); body != "{\"name\":\"Developer\",\"space_id\":\"dev-space\",\"uid\":\"dev-user\"}" {
		t.Fatalf("body=%q", body)
	}
}

// TestMCPMountedUnderV1 pins the deploy-path contract: the gateway strips the
// /market prefix, so a client call to /market/api/v1/mcps arrives here as
// /api/v1/mcps and must reach the List handler (200), consistent with the
// sibling /api/v1/session endpoint. See docs/api/mcp-v1.md §4 and the
// front/back alignment on issue LSC-72.
func TestMCPMountedUnderV1(t *testing.T) {
	svc := &reachedService{}
	engine := Public(stubPinger{}, testAuthenticator(), testAdminAuthenticator(), testStorageConfig(), handler.NewMCP(svc), testAdminHandler())

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/mcps", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/mcps status=%d want=%d body=%q", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !svc.listed {
		t.Fatal("GET /api/v1/mcps did not reach the List handler")
	}
}

// TestBareMCPPathNotMounted guards against a regression to the old bare /mcps
// mount. With the gateway only stripping /market, a bare /mcps would never be
// hit in production; asserting 404 here keeps the surface aligned with the
// contract deploy path (blocker ① on LSC-72).
func TestBareMCPPathNotMounted(t *testing.T) {
	svc := &reachedService{}
	engine := Public(stubPinger{}, testAuthenticator(), testAdminAuthenticator(), testStorageConfig(), handler.NewMCP(svc), testAdminHandler())

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/mcps", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET /mcps status=%d want=%d", recorder.Code, http.StatusNotFound)
	}
	if svc.listed {
		t.Fatal("bare /mcps should not reach the List handler")
	}
}

// TestProbeRouteEndToEnd walks the full stack for POST /api/v1/mcps/probe:
// router → auth middleware → handler → real service.Probe → fake MCP server.
// This pins the wire contract in mcp-v1.md §4.7: HTTP 200 + a JSON body with
// {ok:true, tools:[…], serverInfo}.
func TestProbeRouteEndToEnd(t *testing.T) {
	// Fake MCP server that answers initialize + tools/list.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "fake", "version": "1"},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": []map[string]any{
					{"name": "echo", "description": "echo back"},
				}},
			})
		}
	}))
	defer fake.Close()

	// Real service (nil store — Probe doesn't touch persistence).
	realSvc := service.New(nil)
	engine := Public(stubPinger{}, testAuthenticator(), testAdminAuthenticator(), testStorageConfig(), handler.NewMCP(realSvc), testAdminHandler())

	body, _ := json.Marshal(map[string]any{
		"transport": "streamable-http",
		"url":       fake.URL,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcps/probe", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("probe status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var resp struct {
		OK    bool `json:"ok"`
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
		ServerInfo *struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || len(resp.Tools) != 1 || resp.Tools[0].Name != "echo" {
		t.Fatalf("bad probe response: %+v", resp)
	}
	if resp.ServerInfo == nil || resp.ServerInfo.Name != "fake" {
		t.Fatalf("missing serverInfo: %+v", resp.ServerInfo)
	}
}

// TestProbeRouteStdioRejected asserts the wire-level 400 for stdio via the
// standard §2 error envelope.
func TestProbeRouteStdioRejected(t *testing.T) {
	realSvc := service.New(nil)
	engine := Public(stubPinger{}, testAuthenticator(), testAdminAuthenticator(), testStorageConfig(), handler.NewMCP(realSvc), testAdminHandler())

	body, _ := json.Marshal(map[string]any{
		"transport": "stdio",
		"command":   "npx",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcps/probe", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=400 body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "err.marketplace.mcp.probe_unsupported") {
		t.Fatalf("expected probe_unsupported in body, got %s", recorder.Body.String())
	}
}

// ─── Admin surface (docs/api/mcp-v1.md §9) ─────────────────────────────────

// reachedAdminService lets a router test assert the admin handler was
// actually reached (route matched, middleware passed) instead of 404ing.
type reachedAdminService struct {
	listed  bool
	created bool
}

func (s *reachedAdminService) CreateSystem(context.Context, service.Caller, model.CreateRequest) (model.Detail, *apierr.Error) {
	s.created = true
	return model.Detail{}, nil
}
func (s *reachedAdminService) ListSystem(context.Context, service.ListParams) (model.ListResponse, *apierr.Error) {
	s.listed = true
	return model.ListResponse{}, nil
}
func (s *reachedAdminService) GetSystem(context.Context, string) (model.Detail, *apierr.Error) {
	return model.Detail{}, nil
}
func (s *reachedAdminService) UpdateSystem(context.Context, string, model.PatchRequest) (model.Detail, *apierr.Error) {
	return model.Detail{}, nil
}
func (s *reachedAdminService) DeleteSystem(context.Context, string) *apierr.Error {
	return nil
}
func (s *reachedAdminService) Probe(context.Context, service.ProbeRequest) (service.ProbeResponse, *apierr.Error) {
	return service.ProbeResponse{OK: true, Tools: []model.Tool{}}, nil
}

// TestAdminMountedUnderV1 pins the admin deploy path: the octo-admin console
// hits /market/api/v1/admin/mcps; the gateway strips /market so it arrives
// here as /api/v1/admin/mcps and must reach the List handler. See
// docs/api/mcp-v1.md §9.
// TestAdminProbeRoute confirms the admin probe endpoint mirrors the public
// probe path: POST /api/v1/admin/mcps/probe reaches the service.Probe call
// and returns the wrapped envelope. Regression guard for the wizard's
// "试连 / 获取工具列表" button, which is admin-only.
func TestAdminProbeRoute(t *testing.T) {
	svc := &reachedAdminService{}
	engine := Public(stubPinger{}, testAuthenticator(), testAdminAuthenticator(), testStorageConfig(),
		testHandler(), handler.NewAdminMCP(svc))

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/mcps/probe",
		strings.NewReader(`{"transport":"streamable-http","url":"https://example.test/mcp"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/admin/mcps/probe status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"ok":true`) {
		t.Fatalf("expected ok:true envelope, got %s", recorder.Body.String())
	}
}

func TestAdminMountedUnderV1(t *testing.T) {
	svc := &reachedAdminService{}
	engine := Public(stubPinger{}, testAuthenticator(), testAdminAuthenticator(), testStorageConfig(),
		testHandler(), handler.NewAdminMCP(svc))

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/mcps", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/admin/mcps status=%d want=%d body=%q", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !svc.listed {
		t.Fatal("GET /api/v1/admin/mcps did not reach the ListSystem handler")
	}
}

// TestOldAdminPathNotMounted guards against the pre-migration URL
// /admin/api/v1/mcps (top-level admin namespace) coming back to life. After
// the switch to /api/v1/admin/mcps it must 404 so old clients get a hard
// failure instead of a silent divergence.
func TestOldAdminPathNotMounted(t *testing.T) {
	engine := Public(stubPinger{}, testAuthenticator(), testAdminAuthenticator(), testStorageConfig(),
		testHandler(), testAdminHandler())

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/api/v1/mcps", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET /admin/api/v1/mcps status=%d want=%d", recorder.Code, http.StatusNotFound)
	}
}

// TestAdminRejectsMissingTokenInProd wires a prod-mode admin authenticator
// (AUTH_ENABLED=true + configured secret) and confirms a request without
// X-Admin-Token is rejected 401 with the auth-family error code.
func TestAdminRejectsMissingTokenInProd(t *testing.T) {
	prodAdminAuth := marketmiddleware.NewAdminAuthenticator(true, "sekret",
		model.Identity{UID: "platform-admin", Name: "Platform"})
	svc := &reachedAdminService{}
	engine := Public(stubPinger{}, testAuthenticator(), prodAdminAuth, testStorageConfig(),
		testHandler(), handler.NewAdminMCP(svc))

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/mcps", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("prod-mode missing token status=%d want=401 body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "err.marketplace.auth.admin_unauthorized") {
		t.Fatalf("expected auth.admin_unauthorized in body, got %s", recorder.Body.String())
	}
	if svc.listed {
		t.Fatal("prod-mode with missing token must NOT reach the handler")
	}
}

// TestAdminAcceptsMatchingTokenInProd walks the happy path: prod-mode
// authenticator with a matching X-Admin-Token → 200 and the handler is
// reached.
func TestAdminAcceptsMatchingTokenInProd(t *testing.T) {
	prodAdminAuth := marketmiddleware.NewAdminAuthenticator(true, "sekret",
		model.Identity{UID: "platform-admin", Name: "Platform"})
	svc := &reachedAdminService{}
	engine := Public(stubPinger{}, testAuthenticator(), prodAdminAuth, testStorageConfig(),
		testHandler(), handler.NewAdminMCP(svc))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/mcps", nil)
	req.Header.Set("X-Admin-Token", "sekret")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("prod-mode matching token status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !svc.listed {
		t.Fatal("matching token must reach the handler")
	}
}
