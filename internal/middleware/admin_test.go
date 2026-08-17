package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/gin-gonic/gin"
)

// adminPingHandler echoes whatever identity the middleware stamped so tests
// can assert the resolved SuperAdmin was installed.
func adminPingHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := Identity(c)
		c.JSON(http.StatusOK, gin.H{
			"uid":   id.UID,
			"name":  id.Name,
			"space": SpaceID(c),
		})
	}
}

func newAdminEngine(a *AdminAuthenticator) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/admin/ping", a.Handler(), adminPingHandler())
	return r
}

func TestAdminAuth_DevBypassesResolver(t *testing.T) {
	a := NewAdminAuthenticator(false, nil, model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping", nil)
	rr := httptest.NewRecorder()
	newAdminEngine(a).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("dev mode expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]string
	_ = json.NewDecoder(rr.Body).Decode(&body)
	if body["uid"] != "admin" || body["name"] != "Admin" {
		t.Fatalf("default dev identity not applied: %#v", body)
	}
}

func TestAdminAuth_DevUsesConfiguredIdentity(t *testing.T) {
	a := NewAdminAuthenticator(false, nil, model.Identity{UID: "dev-user", Name: "Developer"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping", nil)
	rr := httptest.NewRecorder()
	newAdminEngine(a).ServeHTTP(rr, req)
	var body map[string]string
	_ = json.NewDecoder(rr.Body).Decode(&body)
	if body["uid"] != "dev-user" || body["name"] != "Developer" || body["space"] != "" {
		t.Fatalf("unexpected identity: %#v", body)
	}
}

func TestAdminAuth_NewAdminAuthenticatorPanicsOnNilResolverInProd(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for nil resolver + authEnabled=true")
		}
	}()
	_ = NewAdminAuthenticator(true, nil, model.Identity{})
}

func TestAdminAuth_ProdRejectsMissingToken(t *testing.T) {
	a := NewAdminAuthenticator(true, stubResolver{}, model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping", nil)
	rr := httptest.NewRecorder()
	newAdminEngine(a).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !containsErrorCode(rr.Body.Bytes(), "AUTH_REQUIRED") {
		t.Fatalf("expected AUTH_REQUIRED, got %s", rr.Body.String())
	}
}

func TestAdminAuth_ProdRejectsResolverError(t *testing.T) {
	a := NewAdminAuthenticator(true, stubResolver{err: errors.New("boom")}, model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping", nil)
	req.Header.Set("Token", "any")
	rr := httptest.NewRecorder()
	newAdminEngine(a).ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !containsErrorCode(rr.Body.Bytes(), "UPSTREAM_UNAVAILABLE") {
		t.Fatalf("expected UPSTREAM_UNAVAILABLE, got %s", rr.Body.String())
	}
}

func TestAdminAuth_ProdRejectsInvalidToken(t *testing.T) {
	// Resolver returns empty UID → mimics "token expired / not found".
	a := NewAdminAuthenticator(true, stubResolver{identity: model.Identity{}}, model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping", nil)
	req.Header.Set("Token", "expired")
	rr := httptest.NewRecorder()
	newAdminEngine(a).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminAuth_ProdRejectsMissingContext(t *testing.T) {
	// Resolver returns identity without ContextIncluded → upstream too old to
	// vouch for space membership; treat as unavailable.
	a := NewAdminAuthenticator(true, stubResolver{identity: model.Identity{
		UID:  "u1",
		Role: RoleSuperAdmin,
	}}, model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping", nil)
	req.Header.Set("Token", "session")
	rr := httptest.NewRecorder()
	newAdminEngine(a).ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminAuth_ProdRejectsNonSuperAdmin(t *testing.T) {
	a := NewAdminAuthenticator(true, stubResolver{identity: model.Identity{
		UID:             "u1",
		Name:            "Alice",
		Role:            "",
		ContextIncluded: true,
	}}, model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping", nil)
	req.Header.Set("Token", "regular-user")
	rr := httptest.NewRecorder()
	newAdminEngine(a).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !containsErrorCode(rr.Body.Bytes(), "FORBIDDEN") {
		t.Fatalf("expected FORBIDDEN, got %s", rr.Body.String())
	}
}

func TestAdminAuth_ProdAcceptsSuperAdmin(t *testing.T) {
	a := NewAdminAuthenticator(true, stubResolver{identity: model.Identity{
		UID:             "root",
		Name:            "Root",
		Role:            RoleSuperAdmin,
		ContextIncluded: true,
	}}, model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping", nil)
	req.Header.Set("Token", "super-admin-session")
	rr := httptest.NewRecorder()
	newAdminEngine(a).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["uid"] != "root" || body["name"] != "Root" || body["space"] != "" {
		t.Fatalf("unexpected identity stamp: %#v", body)
	}
}

// containsErrorCode is a minimal probe for the standard error envelope
// {"error":{"code":"..."}} so tests don't reimplement JSON walking.
func containsErrorCode(body []byte, code string) bool {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	return envelope.Error.Code == code
}

// newAdminEngineAllowing mounts two groups the way the real router does: a
// catalog group widened to marketAdmin, and an Expert Market group left at the
// default SuperAdmin-only gate.
func newAdminEngineAllowing(a *AdminAuthenticator) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/admin/skills", a.Handler(RoleMarketAdmin), adminPingHandler())
	r.GET("/api/v1/admin/experts", a.Handler(), adminPingHandler())
	return r
}

func adminGet(t *testing.T, engine *gin.Engine, path string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Token", "session")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr.Code
}

// TestAdminAuth_MarketAdminScopedToCatalog is the whole point of the alsoAllow
// parameter: marketAdmin curates the platform catalogs and must NOT reach the
// Expert Market, which stays SuperAdmin-only.
func TestAdminAuth_MarketAdminScopedToCatalog(t *testing.T) {
	tests := []struct {
		name        string
		role        string
		wantCatalog int
		wantExpert  int
	}{
		{name: "super admin reaches everything", role: RoleSuperAdmin, wantCatalog: http.StatusOK, wantExpert: http.StatusOK},
		{name: "market admin is catalog only", role: RoleMarketAdmin, wantCatalog: http.StatusOK, wantExpert: http.StatusForbidden},
		{name: "plain user reaches nothing", role: "", wantCatalog: http.StatusForbidden, wantExpert: http.StatusForbidden},
		{name: "unknown role reaches nothing", role: "dashboardReader", wantCatalog: http.StatusForbidden, wantExpert: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAdminAuthenticator(true, stubResolver{identity: model.Identity{
				UID: "u1", Name: "Alice", Role: tt.role, ContextIncluded: true,
			}}, model.Identity{})
			engine := newAdminEngineAllowing(a)
			if got := adminGet(t, engine, "/api/v1/admin/skills"); got != tt.wantCatalog {
				t.Fatalf("catalog group: got %d, want %d", got, tt.wantCatalog)
			}
			if got := adminGet(t, engine, "/api/v1/admin/experts"); got != tt.wantExpert {
				t.Fatalf("expert group: got %d, want %d", got, tt.wantExpert)
			}
		})
	}
}

// TestRoleAdmitted covers the empty-role edge directly: an octo-server that
// returned no role must never be admitted, including on a widened group.
func TestRoleAdmitted(t *testing.T) {
	cases := []struct {
		role      string
		alsoAllow []string
		want      bool
	}{
		{role: RoleSuperAdmin, want: true},
		{role: RoleSuperAdmin, alsoAllow: []string{RoleMarketAdmin}, want: true},
		{role: RoleMarketAdmin, alsoAllow: []string{RoleMarketAdmin}, want: true},
		{role: RoleMarketAdmin, want: false},
		{role: "", want: false},
		{role: "", alsoAllow: []string{RoleMarketAdmin}, want: false},
		{role: "", alsoAllow: []string{""}, want: false},
	}
	for _, tc := range cases {
		if got := roleAdmitted(tc.role, tc.alsoAllow); got != tc.want {
			t.Fatalf("roleAdmitted(%q, %v) = %v, want %v", tc.role, tc.alsoAllow, got, tc.want)
		}
	}
}
