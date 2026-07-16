package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// pingHandler echoes whatever identity the middleware stamped, so tests can
// verify the synthetic admin identity is being installed.
func pingHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := IdentityFromContext(r.Context())
		space := SpaceIDFromContext(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uid":   id.UID,
			"name":  id.Name,
			"space": space,
		})
	})
}

func TestAdminAuth_DevBypassesTokenCheck(t *testing.T) {
	// AUTH_ENABLED=false in dev. No X-Admin-Token, still admitted.
	a := NewAdminAuthenticator(false, "", model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/v1/mcps", nil)
	rr := httptest.NewRecorder()
	a.Wrap(pingHandler()).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("dev mode expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminAuth_ProdRejectsMissingToken(t *testing.T) {
	a := NewAdminAuthenticator(true, "sekret", model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/v1/mcps", nil)
	rr := httptest.NewRecorder()
	a.Wrap(pingHandler()).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	var body map[string]map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if got := body["err"]["code"]; got != "err.marketplace.auth.admin_unauthorized" {
		t.Fatalf("wrong error code: %q", got)
	}
}

func TestAdminAuth_ProdRejectsWrongToken(t *testing.T) {
	a := NewAdminAuthenticator(true, "sekret", model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/v1/mcps", nil)
	req.Header.Set("X-Admin-Token", "wrong")
	rr := httptest.NewRecorder()
	a.Wrap(pingHandler()).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAdminAuth_ProdRejectsEmptyConfiguredToken(t *testing.T) {
	// Deployment forgot to set MARKETPLACE_ADMIN_TOKEN → admin is disabled by
	// design, even if the client sends something.
	a := NewAdminAuthenticator(true, "", model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/v1/mcps", nil)
	req.Header.Set("X-Admin-Token", "whatever")
	rr := httptest.NewRecorder()
	a.Wrap(pingHandler()).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAdminAuth_ProdAcceptsMatchingToken(t *testing.T) {
	a := NewAdminAuthenticator(true, "sekret",
		model.Identity{UID: "root", Name: "Root"})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/v1/mcps", nil)
	req.Header.Set("X-Admin-Token", "sekret")
	rr := httptest.NewRecorder()
	a.Wrap(pingHandler()).ServeHTTP(rr, req)
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

func TestAdminAuth_DefaultIdentityStamped(t *testing.T) {
	// Empty devIdentity ⇒ defaults to admin/Admin.
	a := NewAdminAuthenticator(false, "", model.Identity{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/v1/mcps", nil)
	rr := httptest.NewRecorder()
	a.Wrap(pingHandler()).ServeHTTP(rr, req)
	var body map[string]string
	_ = json.NewDecoder(rr.Body).Decode(&body)
	if body["uid"] != "admin" || body["name"] != "Admin" {
		t.Fatalf("default identity not applied: %#v", body)
	}
}
