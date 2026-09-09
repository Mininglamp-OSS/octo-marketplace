package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// verifyResponse is the shape the resolver posts against octo-server. Tests
// here build a server that returns it verbatim so we can drive SpaceRoles into
// HTTPResolver.Resolve without depending on the real octo-server.
func newTestResolver(t *testing.T, h http.HandlerFunc) *HTTPResolver {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewHTTPResolver(srv.URL)
}

func TestResolve_ClampsSpaceRolesAtIngress(t *testing.T) {
	cases := []struct {
		name      string
		wire      map[string]int // value the server sends in space_roles
		want      int            // clamped value we expect Identity.SpaceRole to return
		canReview bool
	}{
		{"member 0 passes through", map[string]int{"sp": 0}, model.SpaceRoleMember, false},
		{"admin 1 passes through", map[string]int{"sp": 1}, model.SpaceRoleAdmin, true},
		{"owner 2 passes through", map[string]int{"sp": 2}, model.SpaceRoleOwner, true},
		{"web-encoded member 3 -> member (not admin)", map[string]int{"sp": 3}, model.SpaceRoleMember, false},
		{"large value 99 -> member", map[string]int{"sp": 99}, model.SpaceRoleMember, false},
		{"negative -1 -> member", map[string]int{"sp": -1}, model.SpaceRoleMember, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestResolver(t, func(w http.ResponseWriter, req *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"uid":              "u-1",
					"spaces":           []string{"sp"},
					"context_included": true,
					"space_roles":      tc.wire,
				})
			})
			id, err := r.Resolve(context.Background(), "tok")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			// Clamp also mutates the decoded map so any future direct read stays safe.
			if stored := id.SpaceRoles["sp"]; stored != tc.want {
				t.Errorf("SpaceRoles entry stored as %d, want %d (map not clamped in place)", stored, tc.want)
			}
			if got := id.SpaceRole("sp"); got != tc.want {
				t.Errorf("SpaceRole=%d, want %d", got, tc.want)
			}
			if got := id.CanReviewSpace("sp"); got != tc.canReview {
				t.Errorf("CanReviewSpace=%v, want %v", got, tc.canReview)
			}
		})
	}
}

// TestResolve_AbsentSpaceRolesFailClosed is the documented "older octo-server
// that does not send space_roles" case — must not grant review authority. This
// is structurally different from the clamped path (nil map vs clamped value)
// but produces the same safe answer.
func TestResolve_AbsentSpaceRolesFailClosed(t *testing.T) {
	r := newTestResolver(t, func(w http.ResponseWriter, req *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uid":              "u-1",
			"spaces":           []string{"sp"},
			"context_included": true,
		})
	})
	id, err := r.Resolve(context.Background(), "tok")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if id.SpaceRoles != nil {
		t.Fatalf("SpaceRoles should be nil/absent for an older server, got %v", id.SpaceRoles)
	}
	if id.SpaceRole("sp") != model.SpaceRoleMember {
		t.Fatal("absent space_roles must collapse to member, not admin/owner")
	}
	if id.CanReviewSpace("sp") {
		t.Fatal("absent space_roles must not grant review authority")
	}
}

// TestResolve_DriftWarningLoggedOncePerBadValue confirms the rate-limit choice:
// a bad value repeated N times logs exactly once per distinct bad value. We
// can't introspect zap output cleanly without swapping the global logger, but
// we can verify the sync.Once bookkeeping is wired up so each distinct bad
// value is logged at most once.
func TestResolve_DriftWarningLoggedOncePerBadValue(t *testing.T) {
	calls := 0
	r := newTestResolver(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uid":              "u-1",
			"spaces":           []string{"sp"},
			"context_included": true,
			"space_roles":      map[string]int{"sp": 3}, // drifted encoding
		})
	})
	for i := 0; i < 50; i++ {
		if _, err := r.Resolve(context.Background(), "tok"); err != nil {
			t.Fatalf("Resolve[%d]: %v", i, err)
		}
	}
	if calls != 50 {
		t.Fatalf("server calls=%d, want 50 (no caching at HTTPResolver level)", calls)
	}
	// The once for value 3 must exist (created on first bad value).
	r.driftMu.Lock()
	o3 := r.driftLogOnce[3]
	r.driftMu.Unlock()
	if o3 == nil {
		t.Fatal("driftLogOnce[3] was never created despite receiving role=3 fifty times")
	}
}

// TestClampSpaceRole_MatchesModel guards that the resolver treats
// model.ClampSpaceRole as authoritative across a wide input range, so a future
// refactor that desynchronizes the two will be caught.
func TestClampSpaceRole_MatchesModel(t *testing.T) {
	for v := -100; v < 100; v++ {
		got, bad := model.ClampSpaceRole(v)
		// The invariant we actually rely on in auth checks:
		//  - in-range values pass through
		//  - out-of-range values collapse to SpaceRoleMember (never >= Admin)
		if v >= model.SpaceRoleMember && v <= model.SpaceRoleOwner {
			if got != v || bad {
				t.Fatalf("in-range %d clamped to (%d,%v)", v, got, bad)
			}
		} else {
			if got != model.SpaceRoleMember || !bad {
				t.Fatalf("out-of-range %d should be SpaceRoleMember/bad, got (%d,%v)", v, got, bad)
			}
			if got >= model.SpaceRoleAdmin {
				t.Fatalf("out-of-range %d clamped to %d >= Admin — authorization leak", v, got)
			}
		}
	}
}

// TestResolverClampIsConcurrentSafe guards against a data race on driftLogOnce.
func TestResolverClampIsConcurrentSafe(t *testing.T) {
	r := newTestResolver(t, func(w http.ResponseWriter, req *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uid":              "u-1",
			"spaces":           []string{"sp"},
			"context_included": true,
			"space_roles":      map[string]int{"sp": 3},
		})
	})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := r.Resolve(ctx, "tok"); err != nil {
				t.Errorf("Resolve: %v", err)
			}
		}()
	}
	wg.Wait()
}
