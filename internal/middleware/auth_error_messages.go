package middleware

import "github.com/gin-gonic/gin"

const authErrorMessagesKey = "marketplace.auth_error_messages"

// SetAuthErrorMessages changes display text for this request only. Call before
// the authenticator; it does not change status codes, identity, or authorization.
// The supplied map is read-only for the lifetime of the request.
func SetAuthErrorMessages(c *gin.Context, messages map[string]string) {
	c.Set(authErrorMessagesKey, messages)
}
