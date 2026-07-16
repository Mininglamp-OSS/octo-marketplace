package middleware

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// AdminAuthenticator guards the /admin/api/v1/* namespace used by octo-admin.
//
// Auth contract (documented on the octo-admin side; not yet in mcp-v1.md
// because that doc only covers the public /market/api/v1/* surface):
//
//	Header:  X-Admin-Token: <shared-secret>
//	Env:     MARKETPLACE_ADMIN_TOKEN=<same-value>
//
// A request is admitted iff the header equals the configured token AND the
// token is non-empty. When AUTH_ENABLED=false (dev), the token check is
// skipped and the request is admitted unconditionally — matching the dev-mode
// behaviour of the regular Authenticator so local iteration doesn't need
// secret management. Every admin request is stamped with a synthetic admin
// identity (`admin` uid, `Admin` name) so downstream service code has a
// caller for creator_name and audit trails.
//
// Wire error envelope mirrors the marketplace public API (doc §2):
//
//	{"err": {"code":"err.marketplace.admin.unauthorized",
//	         "message":"Admin token required"}}
type AdminAuthenticator struct {
	enabled  bool
	token    string
	identity model.Identity
	spaceID  string
}

// NewAdminAuthenticator constructs the middleware.
//
//   - `authEnabled` mirrors the public authenticator's flag; when false, the
//     token check is bypassed for local dev.
//   - `token` is the shared secret from config.MARKETPLACE_ADMIN_TOKEN.
//     Empty AND authEnabled=true ⇒ every admin request is rejected 401
//     (deployed without a token means admin is disabled by design).
//   - `devIdentity` supplies uid/name used for stamping creator_name on
//     system MCPs.
func NewAdminAuthenticator(authEnabled bool, token string, devIdentity model.Identity) *AdminAuthenticator {
	identity := devIdentity
	if identity.UID == "" {
		identity.UID = "admin"
	}
	if identity.Name == "" {
		identity.Name = "Admin"
	}
	// Admin operates outside the Space model: system MCPs carry space_id=NULL.
	// We hand an empty spaceID down to service code and let it interpret that
	// as "no space anchor" for the CreateSystem path.
	return &AdminAuthenticator{
		enabled:  authEnabled,
		token:    strings.TrimSpace(token),
		identity: identity,
		spaceID:  "",
	}
}

// Wrap returns an http.Handler middleware wrapping `next`. Same
// http.Handler chain shape as Authenticator.WrapMarket so it composes with
// the existing marketplace router wiring.
func (a *AdminAuthenticator) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.enabled {
			// Prod path: require a matching token. Empty configured token
			// closes the door (admin disabled). Comparison uses
			// crypto/subtle to blunt timing attacks on the secret.
			supplied := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
			if a.token == "" || supplied == "" ||
				subtle.ConstantTimeCompare([]byte(supplied), []byte(a.token)) != 1 {
				writeAdminError(w, http.StatusUnauthorized,
					"err.marketplace.auth.admin_unauthorized",
					"Admin token required")
				return
			}
		}
		// Stamp the synthetic admin identity + empty space so downstream
		// handlers reuse the same context accessors as the market path.
		next.ServeHTTP(w, r.WithContext(withAuthContext(r.Context(), a.identity, a.spaceID)))
	})
}

// writeAdminError renders the marketplace wire envelope (doc §2) with an
// admin-specific error code that lives in the auth family (doc §9).
func writeAdminError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"err": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
