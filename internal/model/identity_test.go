package model

import "testing"

func TestClampSpaceRole(t *testing.T) {
	cases := []struct {
		name  string
		in    int
		want  int
		isBad bool
	}{
		{"member passes through", SpaceRoleMember, SpaceRoleMember, false},
		{"admin passes through", SpaceRoleAdmin, SpaceRoleAdmin, false},
		{"owner passes through", SpaceRoleOwner, SpaceRoleOwner, false},
		{"web-encoded member (3) clamped to member", 3, SpaceRoleMember, true},
		{"large value clamped", 99, SpaceRoleMember, true},
		{"negative clamped", -1, SpaceRoleMember, true},
		{"web-encoded admin (2 stays admin — symmetric hazard is web owner=1 demoted)", 2, SpaceRoleOwner, false},
		{"web-encoded owner (1 stays admin — 1 is admin in our encoding, so the demotion side is silent but safe)", 1, SpaceRoleAdmin, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, bad := ClampSpaceRole(tc.in)
			if got != tc.want {
				t.Errorf("ClampSpaceRole(%d)=%d, want %d", tc.in, got, tc.want)
			}
			if bad != tc.isBad {
				t.Errorf("ClampSpaceRole(%d) bad=%v, want %v", tc.in, bad, tc.isBad)
			}
		})
	}
}

func TestIdentitySpaceRole(t *testing.T) {
	id := Identity{SpaceRoles: map[string]int{
		"sp-member":  SpaceRoleMember,
		"sp-admin":   SpaceRoleAdmin,
		"sp-owner":   SpaceRoleOwner,
		"sp-web-mem": 3,  // drifted octo-web encoding
		"sp-big":     99, // nonsense
		"sp-neg":     -1, // nonsense negative
	}}

	cases := []struct {
		name    string
		spaceID string
		want    int
		can     bool
	}{
		{"member stays member", "sp-member", SpaceRoleMember, false},
		{"admin passes", "sp-admin", SpaceRoleAdmin, true},
		{"owner passes", "sp-owner", SpaceRoleOwner, true},
		{"web 3 -> member (no review)", "sp-web-mem", SpaceRoleMember, false},
		{"99 -> member (no review)", "sp-big", SpaceRoleMember, false},
		{"-1 -> member (no review)", "sp-neg", SpaceRoleMember, false},
		{"absent entry -> member (fail closed)", "sp-missing", SpaceRoleMember, false},
		{"nil map -> member", "", SpaceRoleMember, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := id.SpaceRole(tc.spaceID)
			if got != tc.want {
				t.Errorf("SpaceRole(%q)=%d, want %d", tc.spaceID, got, tc.want)
			}
			if can := id.CanReviewSpace(tc.spaceID); can != tc.can {
				t.Errorf("CanReviewSpace(%q)=%v, want %v", tc.spaceID, can, tc.can)
			}
		})
	}
}

// TestIdentitySpaceRole_AbsentMapFailsClosed makes sure the documented "older
// octo-server that omits space_roles entirely" case still collapses to member,
// which is distinct from the clamped path but produces the same safe answer.
func TestIdentitySpaceRole_AbsentMapFailsClosed(t *testing.T) {
	id := Identity{UID: "u", Spaces: []string{"sp"}} // SpaceRoles is nil
	if got := id.SpaceRole("sp"); got != SpaceRoleMember {
		t.Fatalf("nil SpaceRoles: SpaceRole=%d, want %d", got, SpaceRoleMember)
	}
	if id.CanReviewSpace("sp") {
		t.Fatal("nil SpaceRoles must not grant review authority")
	}
}

// TestClampNeverPromotes guards the invariant the fix rests on: no out-of-range
// input must ever clamp to admin or owner, or the authorization boundary
// quietly stops existing in the other direction.
func TestClampNeverPromotes(t *testing.T) {
	for v := -100; v < 100; v++ {
		got, _ := ClampSpaceRole(v)
		if got > SpaceRoleOwner {
			t.Fatalf("ClampSpaceRole(%d)=%d > SpaceRoleOwner(%d)", v, got, SpaceRoleOwner)
		}
		if v < SpaceRoleMember || v > SpaceRoleOwner {
			if got != SpaceRoleMember {
				t.Fatalf("out-of-range %d clamped to %d, want SpaceRoleMember(%d)", v, got, SpaceRoleMember)
			}
		}
	}
}
