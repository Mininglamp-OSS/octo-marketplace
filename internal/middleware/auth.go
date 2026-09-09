package middleware

import (
	"context"
	"maps"
	"net/http"
	"strings"

	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/auth"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type contextKey string

const (
	identityCtxKey contextKey = "marketplace.identity"
	spaceCtxKey    contextKey = "marketplace.space_id"
)

const (
	identityKey    = "marketplace.identity"
	spaceKey       = "marketplace.space_id"
	botIdentityKey = "marketplace.bot_identity"
)

type Authenticator struct {
	enabled     bool
	resolver    auth.Resolver
	botResolver auth.BotResolver
	devIdentity model.Identity
	devSpaceID  string
}

// NewAuthenticator constructs the public request authenticator.
//
// When enabled=false, devIdentity is stamped on every request. Its Space role
// travels in devIdentity.SpaceRoles keyed on devSpaceID (see
// cmd/marketplace-api, which builds it from DEV_AUTH_SPACE_ROLE); the handler
// rebinds that role onto whichever Space the request names. The signature is
// deliberately unchanged — the dev role rides in the identity rather than in a
// new parameter, so the many existing call sites keep compiling and a test that
// wants a reviewer dev identity just sets the map.
func NewAuthenticator(enabled bool, resolver auth.Resolver, devIdentity model.Identity, devSpaceID string, botResolvers ...auth.BotResolver) *Authenticator {
	authenticator := &Authenticator{
		enabled:     enabled,
		resolver:    resolver,
		devIdentity: devIdentity,
		devSpaceID:  devSpaceID,
	}
	if len(botResolvers) > 0 {
		authenticator.botResolver = botResolvers[0]
	}
	return authenticator
}

// AuthEnabled returns whether authentication is enabled.
func (a *Authenticator) AuthEnabled() bool {
	return a.enabled
}

func (a *Authenticator) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !a.enabled {
			spaceID := strings.TrimSpace(c.GetHeader("X-Space-Id"))
			if spaceID == "" {
				spaceID = a.devSpaceID
			}
			setAuthContext(c, a.devIdentityFor(spaceID), spaceID)
			c.Next()
			return
		}

		token := requestToken(c)
		if token == "" {
			abortError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "Authentication is required.")
			return
		}
		if strings.HasPrefix(token, "bf_") {
			a.authenticateBot(c, token)
			return
		}
		identity, ok := resolveUserIdentity(c, a.resolver, token)
		if !ok {
			return
		}

		spaceID := strings.TrimSpace(c.GetHeader("X-Space-Id"))
		if spaceID == "" {
			abortError(c, http.StatusBadRequest, "VALIDATION_ERROR", "X-Space-Id header is required.")
			return
		}
		if !contains(identity.Spaces, spaceID) {
			abortError(c, http.StatusForbidden, "FORBIDDEN", "Access to this Space is forbidden.")
			return
		}

		setAuthContext(c, identity, spaceID)
		c.Next()
	}
}

// resolveUserIdentity runs the resolver-based identity validation both public
// and admin middleware share once the caller-supplied token has been
// extracted and any bot-token dispatch has been done. On any failure it emits
// the appropriate 4xx/5xx envelope via abortError and returns ok=false;
// callers must not continue in that case.
//
// The nil-resolver branch is a defense-in-depth 503: the constructors reject
// this configuration at startup, so a non-nil resolver is a wiring invariant
// by the time a request reaches here.
func resolveUserIdentity(c *gin.Context, resolver auth.Resolver, token string) (model.Identity, bool) {
	if resolver == nil {
		logging.Error("auth_resolver_failed", append(logging.RequestFields(c),
			zap.String("operation", "auth.resolve"),
			zap.String("reason", "nil_resolver"),
		)...)
		abortError(c, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "Authentication service is unavailable.")
		return model.Identity{}, false
	}
	identity, err := resolver.Resolve(c.Request.Context(), token)
	if err != nil {
		logging.Error("auth_resolver_failed", append(logging.RequestFields(c),
			zap.String("operation", "auth.resolve"),
			logging.ErrorField(err),
		)...)
		abortError(c, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "Authentication service is unavailable.")
		return model.Identity{}, false
	}
	if identity.UID == "" {
		abortError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "Invalid or expired token.")
		return model.Identity{}, false
	}
	if !identity.ContextIncluded {
		logging.Error("auth_resolver_failed", append(logging.RequestFields(c),
			zap.String("operation", "auth.resolve"),
			zap.String("reason", "missing_context"),
			zap.String("uid", identity.UID),
		)...)
		abortError(c, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "Authorization context is unavailable.")
		return model.Identity{}, false
	}
	return identity, true
}

// devIdentityFor returns the fixed dev identity bound to the Space the request
// is actually in, for AUTH_ENABLED=false only.
//
// The dev bypass already honours the caller's X-Space-Id header and only falls
// back to DEV_SPACE_ID when it is absent, so a browser signed in to a real
// octo-server sends a real space_id that DEV_SPACE_ID does not name. Without
// this rebinding the configured dev Space role (keyed on DEV_SPACE_ID) would
// simply miss on lookup and the developer would silently degrade to role 0 —
// able to submit a plugin for review and never to approve one, with nothing on
// screen or in the log explaining why.
//
// TENSION, stated rather than buried: CLAUDE.md says disabled mode uses fixed
// DEV_AUTH_* values and "never caller-supplied identity headers". Letting the
// header pick which Space the fixed role applies to widens the dev identity in
// exactly that direction. It does not let the header choose the role itself,
// and an auth-disabled instance is already fully open to anyone who can reach
// it, so the marginal risk is small — but this is one more reason
// AUTH_ENABLED=false must never run outside a developer's machine.
//
// Returns a copy. Identity is a value type but SpaceRoles is a map, so a
// shallow copy still aliases the shared map; writing through it would be a data
// race between concurrent requests and would leak one request's Space into
// another's identity. The map is therefore rebuilt, never mutated in place.
func (a *Authenticator) devIdentityFor(spaceID string) model.Identity {
	identity := a.devIdentity
	if spaceID == "" || len(identity.SpaceRoles) == 0 {
		return identity
	}
	if _, ok := identity.SpaceRoles[spaceID]; ok {
		return identity
	}
	role, ok := identity.SpaceRoles[a.devSpaceID]
	if !ok {
		return identity
	}
	roles := make(map[string]int, len(identity.SpaceRoles)+1)
	maps.Copy(roles, identity.SpaceRoles)
	roles[spaceID] = role
	identity.SpaceRoles = roles
	return identity
}

func (a *Authenticator) authenticateBot(c *gin.Context, token string) {
	if a.botResolver == nil {
		logging.Error("auth_resolver_failed", append(logging.RequestFields(c),
			zap.String("operation", "auth.resolve_bot"),
			zap.String("reason", "nil_resolver"),
		)...)
		abortError(c, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "Authentication service is unavailable.")
		return
	}
	bot, err := a.botResolver.ResolveBot(c.Request.Context(), token)
	if err != nil {
		logging.Error("auth_resolver_failed", append(logging.RequestFields(c),
			zap.String("operation", "auth.resolve_bot"),
			logging.ErrorField(err),
		)...)
		abortError(c, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "Authentication service is unavailable.")
		return
	}
	if bot.BotUID == "" || bot.OwnerUID == "" || bot.SpaceID == "" {
		abortError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "Invalid or expired Bot token.")
		return
	}
	identity := model.Identity{
		UID:             bot.OwnerUID,
		Name:            bot.OwnerName,
		Spaces:          []string{bot.SpaceID},
		ContextIncluded: true,
	}
	c.Set(botIdentityKey, bot)
	setAuthContext(c, identity, bot.SpaceID)
	c.Next()
}

func Identity(c *gin.Context) (model.Identity, bool) {
	value, ok := c.Get(identityKey)
	if !ok {
		return model.Identity{}, false
	}
	identity, ok := value.(model.Identity)
	return identity, ok
}

func SpaceID(c *gin.Context) string {
	value, _ := c.Get(spaceKey)
	spaceID, _ := value.(string)
	return spaceID
}

func IdentityFromContext(ctx context.Context) (model.Identity, bool) {
	identity, ok := ctx.Value(identityCtxKey).(model.Identity)
	return identity, ok
}

func SpaceIDFromContext(ctx context.Context) string {
	spaceID, _ := ctx.Value(spaceCtxKey).(string)
	return spaceID
}

func BotIdentity(c *gin.Context) (model.BotIdentity, bool) {
	value, ok := c.Get(botIdentityKey)
	if !ok {
		return model.BotIdentity{}, false
	}
	identity, ok := value.(model.BotIdentity)
	return identity, ok
}

func OwnsBot(c *gin.Context, botID string) bool {
	identity, ok := Identity(c)
	if !ok {
		return false
	}
	return contains(identity.OwnedBotsBySpace[SpaceID(c)], botID)
}

func setAuthContext(c *gin.Context, identity model.Identity, spaceID string) {
	c.Set(identityKey, identity)
	c.Set(spaceKey, spaceID)
	c.Request = c.Request.WithContext(withAuthContext(c.Request.Context(), identity, spaceID))
}

func withAuthContext(ctx context.Context, identity model.Identity, spaceID string) context.Context {
	ctx = context.WithValue(ctx, identityCtxKey, identity)
	return context.WithValue(ctx, spaceCtxKey, spaceID)
}

func requestToken(c *gin.Context) string {
	if token := strings.TrimSpace(c.GetHeader("Token")); token != "" {
		return token
	}
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if len(authorization) > 7 && strings.EqualFold(authorization[:7], "Bearer ") {
		return strings.TrimSpace(authorization[7:])
	}
	return ""
}

// Token returns the raw credential on the request (`Token` header, else an
// `Authorization: Bearer` value). The auth middleware resolves and then
// discards this token; handlers that must forward it to another service
// (e.g. the expert-install → fleet aggregation) re-read it via this helper.
func Token(c *gin.Context) string {
	return requestToken(c)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func abortError(c *gin.Context, status int, code, message string) {
	apiresponse.Fail(c, status, code, message, nil, "")
}
