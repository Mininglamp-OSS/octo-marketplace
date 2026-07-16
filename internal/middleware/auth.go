package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

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
			abortError(c, http.StatusUnauthorized, "err.marketplace.authentication_required", "Authentication is required.")
			return
		}
		if strings.HasPrefix(token, "bf_") {
			a.authenticateBot(c, token)
			return
		}
		if a.resolver == nil {
			abortError(c, http.StatusServiceUnavailable, "err.marketplace.auth_unavailable", "Authentication service is unavailable.")
			return
		}
		identity, err := a.resolver.Resolve(c.Request.Context(), token)
		if err != nil {
			abortError(c, http.StatusServiceUnavailable, "err.marketplace.auth_unavailable", "Authentication service is unavailable.")
			return
		}
		if identity.UID == "" {
			abortError(c, http.StatusUnauthorized, "err.marketplace.invalid_token", "Invalid or expired token.")
			return
		}
		if !identity.ContextIncluded {
			abortError(c, http.StatusServiceUnavailable, "err.marketplace.auth_context_unavailable", "Authorization context is unavailable.")
			return
		}

		spaceID := strings.TrimSpace(c.GetHeader("X-Space-Id"))
		if spaceID == "" {
			abortError(c, http.StatusBadRequest, "err.marketplace.space_required", "X-Space-Id header is required.")
			return
		}
		if !contains(identity.Spaces, spaceID) {
			abortError(c, http.StatusForbidden, "err.marketplace.space_forbidden", "Access to this Space is forbidden.")
			return
		}

		setAuthContext(c, identity, spaceID)
		c.Next()
	}
}

// WrapMarket is the authenticator for the MCP catalog endpoints. It performs
// the same token + Space resolution as Handler but renders the marketplace wire
// contract (docs/api/mcp-v1.md §1/§2): the {"err":{code,message}} envelope with
// err.marketplace.auth.* codes.
func (a *Authenticator) WrapMarket(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.enabled {
			spaceID := strings.TrimSpace(r.Header.Get("X-Space-Id"))
			if spaceID == "" {
				spaceID = a.devSpaceID
			}
			next.ServeHTTP(w, r.WithContext(withAuthContext(r.Context(), a.devIdentity, spaceID)))
			return
		}

		token := requestTokenFromHTTP(r)
		if token == "" {
			writeMarketError(w, http.StatusUnauthorized, "err.marketplace.auth.unauthorized", "Missing or invalid Octo token")
			return
		}
		if a.resolver == nil {
			writeMarketError(w, http.StatusInternalServerError, "err.marketplace.internal", "Internal server error")
			return
		}
		identity, err := a.resolver.Resolve(r.Context(), token)
		if err != nil {
			writeMarketError(w, http.StatusInternalServerError, "err.marketplace.internal", "Internal server error")
			return
		}
		if identity.UID == "" || !identity.ContextIncluded {
			writeMarketError(w, http.StatusUnauthorized, "err.marketplace.auth.unauthorized", "Missing or invalid Octo token")
			return
		}

		spaceID := strings.TrimSpace(r.Header.Get("X-Space-Id"))
		if spaceID == "" || !contains(identity.Spaces, spaceID) {
			writeMarketError(w, http.StatusForbidden, "err.marketplace.auth.forbidden_space", "Missing X-Space-Id or Space membership denied")
			return
		}

		next.ServeHTTP(w, r.WithContext(withAuthContext(r.Context(), identity, spaceID)))
	})
}

func (a *Authenticator) authenticateBot(c *gin.Context, token string) {
	if a.botResolver == nil {
		abortError(c, http.StatusServiceUnavailable, "err.marketplace.auth_unavailable", "Authentication service is unavailable.")
		return
	}
	bot, err := a.botResolver.ResolveBot(c.Request.Context(), token)
	if err != nil {
		abortError(c, http.StatusServiceUnavailable, "err.marketplace.auth_unavailable", "Authentication service is unavailable.")
		return
	}
	if bot.BotUID == "" || bot.OwnerUID == "" || bot.SpaceID == "" {
		abortError(c, http.StatusUnauthorized, "err.marketplace.invalid_bot_token", "Invalid or expired Bot token.")
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

func requestTokenFromHTTP(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("Token")); token != "" {
		return token
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
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
	c.AbortWithStatusJSON(status, gin.H{
		"error": gin.H{
			"code":        code,
			"message":     message,
			"http_status": status,
		},
	})
}

// writeMarketError renders the marketplace wire envelope (doc §2):
// {"err":{"code":..,"message":..}}.
func writeMarketError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"err": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
