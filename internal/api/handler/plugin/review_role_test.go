package plugin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

// engineWithIdentity builds a gin engine with AUTH_ENABLED=false using the given
// Identity as the dev identity. devIdentityFor only grafts the DEV_SPACE_ID role
// onto a Space the map does NOT already carry (auth.go:172-189), so an identity
// holding explicit entries for space-a and space-b reaches caller() with both
// roles intact and exercises the per-Space selection directly.
func engineWithIdentity(identity model.Identity, svc Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(logging.RequestID())
	auth := middleware.NewAuthenticator(false, nil, identity, "space-a")
	v1 := r.Group("/api/v1", auth.Handler())
	New(svc).Register(v1)
	return r
}

// approveAs drives the approve endpoint under the given Space and returns the
// Caller the service actually received. Reviewer authority itself is enforced in
// the service (isReviewer, covered by the service tests); what the handler owns —
// and what this file pins — is which Space's role ends up in that Caller.
func approveAs(t *testing.T, identity model.Identity, spaceID string) pluginsvc.Caller {
	t.Helper()
	f := &fakeService{}
	f.review.approved = &model.Plugin{ID: "plugin-1", Name: "Demo", Type: model.PluginTypeSkill}
	eng := engineWithIdentity(identity, f)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/review_requests/review-1/approve", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Space-Id", spaceID)
	rec := httptest.NewRecorder()
	eng.ServeHTTP(rec, req)
	if f.review.approveID != "review-1" {
		t.Fatalf("service was not reached (status %d, body %s); the Caller under test was never built", rec.Code, rec.Body.String())
	}
	return f.caller
}

// TestPerSpaceRoleIsolation pins that a caller who holds admin in one Space is a
// plain member while operating under another Space's X-Space-Id. caller()
// (handler.go:607) reads the role BY SpaceID; replacing that with a max-over-
// Spaces read — or with "any admin entry means admin" — would hand an admin of
// Space B reviewer authority over Space A's queue, and isReviewer would grant it
// because the Caller it is handed already looks like an admin.
func TestPerSpaceRoleIsolation(t *testing.T) {
	adminInSpaceB := model.Identity{
		UID:  "admin-2",
		Name: "B-admin",
		SpaceRoles: map[string]int{
			"space-a": model.SpaceRoleMember,
			"space-b": model.SpaceRoleAdmin,
		},
	}

	inA := approveAs(t, adminInSpaceB, "space-a")
	if inA.SpaceID != "space-a" {
		t.Fatalf("caller.SpaceID = %q, want space-a", inA.SpaceID)
	}
	if inA.SpaceRole != pluginsvc.SpaceRoleMember {
		t.Fatalf("caller.SpaceRole in space-a = %d, want %d (member); cross-Space reviewer authority leaked",
			inA.SpaceRole, pluginsvc.SpaceRoleMember)
	}
	if inA.IsSystemAdmin {
		t.Fatal("a Space admin was promoted to system admin")
	}

	// Same identity operating IN space-b, where the admin role is real.
	inB := approveAs(t, adminInSpaceB, "space-b")
	if inB.SpaceRole != pluginsvc.SpaceRoleAdmin {
		t.Fatalf("caller.SpaceRole in space-b = %d, want %d (admin); the per-Space read missed a real entry",
			inB.SpaceRole, pluginsvc.SpaceRoleAdmin)
	}
}

// TestCallerClampsAnOutOfRangeSpaceRole pins the clamp on the PRODUCTION wire
// path. Roles arrive from octo-server as integers; a value outside 0..2 — a
// negative, a larger number, or a drift onto octo-web's inverted encoding where
// 3 means member — must collapse to member rather than satisfy the
// `>= SpaceRoleAdmin` reviewer test. config.go bounds only the DEV_SPACE_ROLE
// input, so the clamp inside Identity.SpaceRole is the only thing standing
// between a wire-format change and a self-service reviewer.
func TestCallerClampsAnOutOfRangeSpaceRole(t *testing.T) {
	for _, role := range []int{-1, 3, 7, 99} {
		identity := model.Identity{
			UID:        "user-9",
			Name:       "Wire drift",
			SpaceRoles: map[string]int{"space-a": role},
		}
		got := approveAs(t, identity, "space-a")
		if got.SpaceRole != pluginsvc.SpaceRoleMember {
			t.Fatalf("wire role %d became caller.SpaceRole %d, want %d (member)",
				role, got.SpaceRole, pluginsvc.SpaceRoleMember)
		}
	}
	// The in-range values still pass through untouched, so the clamp is not just
	// zeroing everything.
	for wire, want := range map[int]int{
		model.SpaceRoleMember: pluginsvc.SpaceRoleMember,
		model.SpaceRoleAdmin:  pluginsvc.SpaceRoleAdmin,
		model.SpaceRoleOwner:  pluginsvc.SpaceRoleOwner,
	} {
		identity := model.Identity{UID: "user-9", Name: "In range", SpaceRoles: map[string]int{"space-a": wire}}
		if got := approveAs(t, identity, "space-a"); got.SpaceRole != want {
			t.Fatalf("wire role %d became caller.SpaceRole %d, want %d", wire, got.SpaceRole, want)
		}
	}
}

// A caller with no entry for the request's Space is a plain member, not an error
// and not an inherited role from some other Space.
func TestCallerWithNoRoleInTheRequestSpaceIsAMember(t *testing.T) {
	identity := model.Identity{
		UID:        "outsider",
		Name:       "Outsider",
		SpaceRoles: map[string]int{"space-b": model.SpaceRoleOwner},
	}
	// space-c is neither the identity's Space nor DEV_SPACE_ID, so devIdentityFor
	// grafts the DEV_SPACE_ID role — which this identity does not have — and the
	// map stays as written.
	if got := approveAs(t, identity, "space-c"); got.SpaceRole != pluginsvc.SpaceRoleMember {
		t.Fatalf("caller.SpaceRole for an unlisted Space = %d, want %d (member)", got.SpaceRole, pluginsvc.SpaceRoleMember)
	}
}
