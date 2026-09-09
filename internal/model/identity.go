package model

// Space member roles, using octo-server's `space_member.role` column encoding.
// Coupled by convention — not by import — to octo-server, exactly like
// middleware.RoleSuperAdmin. Grep both repos before changing a value.
//
// WARNING: octo-web's `Space.role` uses a different, INVERTED encoding
// (1=owner, 2=admin, 3=member). The two are never interchangeable: a web
// "member" (3) read with this encoding compares as >= SpaceRoleAdmin and would
// be handed review authority, and a web "owner" (1) would be demoted to admin.
// Never compare, copy, or default one encoding across to the other.
//
// That hazard is now BOUNDED, not merely documented: SpaceRole clamps anything
// outside 0..2 down to SpaceRoleMember (see ClampSpaceRole), so an octo-server
// that drifted onto the web encoding hands review authority to NOBODY instead of
// to every member of every Space. The clamp is at ingress on purpose — a single
// accessor plus the two wire boundaries (internal/auth.HTTPResolver,
// internal/notify.Client.MemberRole). Do NOT add another one at a `>=
// SpaceRoleAdmin` comparison site, and do not read SpaceRoles directly to
// recover the "real" value.
const (
	SpaceRoleMember = 0
	SpaceRoleAdmin  = 1
	SpaceRoleOwner  = 2
)

type Identity struct {
	UID              string              `json:"uid"`
	Name             string              `json:"name"`
	Role             string              `json:"role,omitempty"`
	Spaces           []string            `json:"spaces,omitempty"`
	OwnedBotsBySpace map[string][]string `json:"owned_bots_by_space,omitempty"`

	// SpaceRoles maps space_id to the caller's role in that Space, as returned
	// by octo-server's POST /v1/auth/verify?include=context alongside `spaces`
	// and `owned_bots_by_space`. Values use the SpaceRole* encoding above
	// (0=member, 1=admin, 2=owner); a plugin reviewer is >= SpaceRoleAdmin.
	//
	// A MISSING entry is indistinguishable from an explicit SpaceRoleMember:
	// both read as 0 and both mean "not a reviewer". Do not use the two-value
	// map read to infer anything about membership — `Spaces` is the membership
	// list, and an older octo-server that does not send `space_roles` at all
	// leaves every Space looking like plain membership, which fails closed.
	SpaceRoles map[string]int `json:"space_roles,omitempty"`

	ContextIncluded bool `json:"context_included,omitempty"`
}

// SpaceRole returns the caller's role in spaceID, or SpaceRoleMember when the
// Space carries no entry. See the SpaceRoles field: absent and 0 are the same
// answer on purpose, and anything outside 0..2 (e.g. a drifted octo-server
// using the inverted octo-web encoding where 3 means "member") is CLAMPED to
// SpaceRoleMember so authorization fails closed instead of silently handing
// every member review authority. Callers MUST NOT read SpaceRoles directly to
// bypass the clamp.
func (i Identity) SpaceRole(spaceID string) int {
	if spaceID == "" {
		return SpaceRoleMember
	}
	r, _ := ClampSpaceRole(i.SpaceRoles[spaceID])
	return r
}

// ClampSpaceRole bounds an incoming octo-server role value to the
// SpaceRoleMember..SpaceRoleOwner range. Values outside 0..2 — negatives,
// larger numbers, or a drift onto the inverted octo-web encoding (3=member) —
// collapse to SpaceRoleMember so that `>= SpaceRoleAdmin` never accidentally
// promotes a plain member to reviewer.
//
// The second return value reports whether the input was out of range, so wire-
// ingress callers (auth resolver, notify client) can log a single "octo-server
// drift" warning per distinct bad value without model having to depend on the
// logging package.
//
// This is called from every ingress (SpaceRole accessor, auth resolver decode,
// notify member-role response). Do NOT call it at comparison sites.
func ClampSpaceRole(r int) (clamped int, outOfRange bool) {
	if r < SpaceRoleMember || r > SpaceRoleOwner {
		return SpaceRoleMember, true
	}
	return r, false
}

// CanReviewSpace reports whether this identity may act on Space-level plugin
// review decisions in spaceID — that is, whether it is an admin or owner of
// that specific Space. It is deliberately Space-scoped: holding owner in one
// Space says nothing about any other, so callers must pass the Space the
// request is actually operating on and never a cached or default one.
func (i Identity) CanReviewSpace(spaceID string) bool {
	return i.SpaceRole(spaceID) >= SpaceRoleAdmin
}

type BotIdentity struct {
	BotUID    string `json:"bot_uid"`
	BotName   string `json:"bot_name"`
	OwnerUID  string `json:"owner_uid"`
	OwnerName string `json:"owner_name"`
	SpaceID   string `json:"space_id"`
}
