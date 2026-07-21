package middleware

import (
	"context"
	"net/http"
	"strings"

	apiresponse "github.com/Mininglamp-OSS/octo-marketplace/internal/api/response"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/auth"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/gin-gonic/gin"
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
			setAuthContext(c, a.devIdentity, spaceID)
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
		abortError(c, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "Authentication service is unavailable.")
		return model.Identity{}, false
	}
	identity, err := resolver.Resolve(c.Request.Context(), token)
	if err != nil {
		abortError(c, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "Authentication service is unavailable.")
		return model.Identity{}, false
	}
	if identity.UID == "" {
		abortError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "Invalid or expired token.")
		return model.Identity{}, false
	}
	if !identity.ContextIncluded {
		abortError(c, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "Authorization context is unavailable.")
		return model.Identity{}, false
	}
	return identity, true
}

func (a *Authenticator) authenticateBot(c *gin.Context, token string) {
	if a.botResolver == nil {
		abortError(c, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "Authentication service is unavailable.")
		return
	}
	bot, err := a.botResolver.ResolveBot(c.Request.Context(), token)
	if err != nil {
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
