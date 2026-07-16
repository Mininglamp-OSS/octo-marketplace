package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/gin-gonic/gin"
)

type stubResolver struct {
	identity model.Identity
	err      error
}

type stubBotResolver struct {
	identity model.BotIdentity
	err      error
}

func (r stubBotResolver) ResolveBot(context.Context, string) (model.BotIdentity, error) {
	return r.identity, r.err
}

func (r stubResolver) Resolve(context.Context, string) (model.Identity, error) {
	return r.identity, r.err
}

func testRouter(authenticator *Authenticator) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(authenticator.Handler())
	r.GET("/", func(c *gin.Context) {
		identity, _ := Identity(c)
		c.String(http.StatusOK, identity.UID+"@"+SpaceID(c))
	})
	return r
}

func TestAuthDisabledUsesDevelopmentContext(t *testing.T) {
	authenticator := NewAuthenticator(false, nil, model.Identity{UID: "dev"}, "dev-space")
	recorder := httptest.NewRecorder()
	testRouter(authenticator).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "dev@dev-space" {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestAuthEnabled(t *testing.T) {
	tests := []struct {
		name     string
		resolver stubResolver
		token    string
		spaceID  string
		want     int
	}{
		{name: "missing token", want: http.StatusUnauthorized},
		{name: "resolver unavailable", token: "t", spaceID: "s1", resolver: stubResolver{err: errors.New("down")}, want: http.StatusServiceUnavailable},
		{name: "invalid token", token: "t", spaceID: "s1", want: http.StatusUnauthorized},
		{name: "old server response", token: "t", spaceID: "s1", resolver: stubResolver{identity: model.Identity{UID: "u1"}}, want: http.StatusServiceUnavailable},
		{name: "space required", token: "t", resolver: stubResolver{identity: model.Identity{UID: "u1", ContextIncluded: true}}, want: http.StatusBadRequest},
		{name: "space forbidden", token: "t", spaceID: "s2", resolver: stubResolver{identity: model.Identity{UID: "u1", ContextIncluded: true, Spaces: []string{"s1"}}}, want: http.StatusForbidden},
		{name: "allowed", token: "t", spaceID: "s1", resolver: stubResolver{identity: model.Identity{UID: "u1", ContextIncluded: true, Spaces: []string{"s1"}}}, want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticator := NewAuthenticator(true, tt.resolver, model.Identity{}, "")
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.token != "" {
				req.Header.Set("Token", tt.token)
			}
			if tt.spaceID != "" {
				req.Header.Set("X-Space-Id", tt.spaceID)
			}
			recorder := httptest.NewRecorder()
			testRouter(authenticator).ServeHTTP(recorder, req)
			if recorder.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tt.want, recorder.Body.String())
			}
		})
	}
}

func TestAuthRejectsQueryCredentials(t *testing.T) {
	authenticator := NewAuthenticator(true, stubResolver{identity: model.Identity{
		UID: "u1", ContextIncluded: true, Spaces: []string{"s1"},
	}}, model.Identity{}, "")
	req := httptest.NewRequest(http.MethodGet, "/?token=secret&space_id=s1", nil)
	recorder := httptest.NewRecorder()
	testRouter(authenticator).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=%d body=%s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
}

func TestOwnsBot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	identity := model.Identity{UID: "u1", OwnedBotsBySpace: map[string][]string{"s1": {"bot-1"}}}
	setAuthContext(c, identity, "s1")
	if !OwnsBot(c, "bot-1") || OwnsBot(c, "bot-2") {
		t.Fatal("unexpected bot ownership result")
	}
}

func TestAuthEnabledWithUserBot(t *testing.T) {
	tests := []struct {
		name     string
		resolver stubBotResolver
		want     int
	}{
		{name: "resolver unavailable", resolver: stubBotResolver{err: errors.New("down")}, want: http.StatusServiceUnavailable},
		{name: "invalid bot", want: http.StatusUnauthorized},
		{name: "missing owner", resolver: stubBotResolver{identity: model.BotIdentity{BotUID: "b1", SpaceID: "s1"}}, want: http.StatusUnauthorized},
		{name: "allowed", resolver: stubBotResolver{identity: model.BotIdentity{BotUID: "b1", BotName: "Bot", OwnerUID: "u1", OwnerName: "User", SpaceID: "s1"}}, want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticator := NewAuthenticator(true, nil, model.Identity{}, "", tt.resolver)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer bf_token")
			req.Header.Set("X-Space-Id", "untrusted-space")
			recorder := httptest.NewRecorder()
			testRouter(authenticator).ServeHTTP(recorder, req)
			if recorder.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tt.want, recorder.Body.String())
			}
			if tt.want == http.StatusOK && recorder.Body.String() != "u1@s1" {
				t.Fatalf("body=%q, want owner identity and authoritative bot space", recorder.Body.String())
			}
		})
	}
}

func TestBotIdentityContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authenticator := NewAuthenticator(true, nil, model.Identity{}, "", stubBotResolver{identity: model.BotIdentity{
		BotUID: "b1", OwnerUID: "u1", SpaceID: "s1",
	}})
	r := gin.New()
	r.Use(authenticator.Handler())
	r.GET("/", func(c *gin.Context) {
		bot, ok := BotIdentity(c)
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.String(http.StatusOK, bot.BotUID)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Token", "bf_token")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "b1" {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}
