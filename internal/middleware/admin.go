package middleware

import (
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/auth"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/gin-gonic/gin"
)

// RoleSuperAdmin is the identity.Role value octo-server encodes for global
// administrators. It is coupled by convention — not by import — to the string
// literal used in octo-server (see pkg/auth/tokeninfo.go and callers of
// wkhttp.Context.CheckLoginRoleIsSuperAdmin). If octo-server ever renames or
// re-cases the role, marketplace silently 403s every SuperAdmin until this
// constant is updated to match. Grep both repos before changing.
const RoleSuperAdmin = "superAdmin"

// RoleMarketAdmin is the octo-server fixed role for staff who curate the
// platform MCP / Skill catalogs and hold no other administrative power. Coupled
// by convention — not by import — to octo-server's
// pkg/auth.ManagerRoleMarketAdmin, exactly like RoleSuperAdmin above. Grep both
// repos before changing either string.
//
// It is admitted only on the catalog groups; see Handler's alsoAllow parameter.
const RoleMarketAdmin = "marketAdmin"

// AdminAuthenticator guards the /api/v1/admin/* namespace consumed by
// octo-admin. It resolves the caller's Octo session token the same way as the
// public Authenticator (via resolveUserIdentity) and then applies a per-group
// role gate.
//
// SuperAdmin is admitted on every group, mirroring octo-server's /v1/manager/*
// CheckLoginRoleIsSuperAdmin gate. Individual groups may additionally admit a
// narrower octo-server fixed role by passing it to Handler — today only the
// platform catalog groups do, admitting RoleMarketAdmin. Groups registered
// without one (the Expert Market groups) stay SuperAdmin-only.
//
// AUTH_ENABLED=false skips the resolve+role check entirely and stamps the
// configured dev identity, matching the dev bypass on the public
// Authenticator so local iteration doesn't need a real octo-server.
//
// Admin routes carry no X-Space-Id — system MCPs live outside the Space model
// (space_id=NULL) — so setAuthContext is called with an empty spaceID.
type AdminAuthenticator struct {
	enabled     bool
	resolver    auth.Resolver
	devIdentity model.Identity
}

// NewAdminAuthenticator constructs the admin middleware.
//
//   - authEnabled mirrors the public authenticator's flag; when false, no
//     token is resolved and no role is checked.
//   - resolver is the same identity resolver used by the public Authenticator.
//     REQUIRED when authEnabled=true; passing nil in that mode panics at
//     construction so a misconfigured deployment fails at boot rather than
//     silently returning 503 to every admin request.
//   - devIdentity is stamped onto the request context in dev bypass. Empty
//     UID/Name default to "admin"/"Admin" so downstream creator_name fields
//     stay populated during local runs.
func NewAdminAuthenticator(authEnabled bool, resolver auth.Resolver, devIdentity model.Identity) *AdminAuthenticator {
	if authEnabled && resolver == nil {
		panic("middleware: NewAdminAuthenticator requires a non-nil resolver when authEnabled=true")
	}
	if devIdentity.UID == "" {
		devIdentity.UID = "admin"
	}
	if devIdentity.Name == "" {
		devIdentity.Name = "Admin"
	}
	return &AdminAuthenticator{
		enabled:     authEnabled,
		resolver:    resolver,
		devIdentity: devIdentity,
	}
}

// Handler guards admin marketplace routes in the Gin router.
//
// SuperAdmin is admitted everywhere. alsoAllow widens one route group to
// additional fixed octo-server roles: the platform catalog groups (MCP, Skill,
// skill categories, skill uploads) pass RoleMarketAdmin, while the Expert Market
// groups deliberately pass nothing and stay SuperAdmin-only. Widening is
// therefore opt-in per group — a new admin group added without an explicit
// alsoAllow inherits the strictest gate.
func (a *AdminAuthenticator) Handler(alsoAllow ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !a.enabled {
			setAuthContext(c, a.devIdentity, "")
			c.Next()
			return
		}

		token := requestToken(c)
		if token == "" {
			abortError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "Admin authentication is required.")
			return
		}
		identity, ok := resolveUserIdentity(c, a.resolver, token)
		if !ok {
			return
		}
		if !roleAdmitted(identity.Role, alsoAllow) {
			// One generic message for every rejected role: which role would have
			// been sufficient is not something an unauthorized caller should learn.
			abortError(c, http.StatusForbidden, "FORBIDDEN", "Insufficient role for this admin resource.")
			return
		}
		setAuthContext(c, identity, "")
		c.Next()
	}
}

// roleAdmitted reports whether role may use a group guarded with alsoAllow.
// An empty role (a plain user, or an octo-server that returned no role) matches
// nothing, since neither RoleSuperAdmin nor any allowed entry is empty.
func roleAdmitted(role string, alsoAllow []string) bool {
	if role == RoleSuperAdmin {
		return true
	}
	for _, allowed := range alsoAllow {
		if allowed != "" && role == allowed {
			return true
		}
	}
	return false
}
