package router

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	marketmiddleware "github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/notify"
	"github.com/gin-gonic/gin"
)

const routeTestCardSecret = "0123456789abcdef0123456789abcdef" // gitleaks:allow

// reviewRouterEngine builds the engine through the PRODUCTION entry point with a
// real *sql.DB (so the plugin surface is wired at all) and a PROD-mode tenant
// authenticator. Building a bespoke router here would make every assertion below
// a tautology: the point is what Public() actually mounts and in which chain.
func reviewRouterEngine(t *testing.T, cardSecret string) *gin.Engine {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.MatchExpectationsInOrder(false)
	prodAuth := marketmiddleware.NewAuthenticator(true, stubResolver{identity: model.Identity{UID: "user-1", Name: "Alice", Spaces: []string{"space-a"}, ContextIncluded: true}}, model.Identity{}, "")
	return Public(db, prodAuth, testAdminAuthenticator(), testStorageConfig(),
		testHandler(), testAdminHandler(), testParseConfig(), nil, ReviewConfig{
			OctoAPIURL:       "http://octo-server.invalid",
			InternalToken:    strings.Repeat("t", 32),
			CardActionSecret: cardSecret,
		})
}

// All six tenant review endpoints, plus the two listing-lifecycle endpoints, must
// be registered by the production wiring.
// Reading them off the engine's route table means a forgotten Register() call
// fails here rather than 404ing in the browser.
func TestReviewRoutesAreMountedByProductionWiring(t *testing.T) {
	engine := reviewRouterEngine(t, routeTestCardSecret)
	registered := map[string]bool{}
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"POST /api/v1/plugins/review_requests",
		"GET /api/v1/plugins/review_requests",
		"GET /api/v1/plugins/review_requests/:review_id",
		"POST /api/v1/plugins/review_requests/:review_id/approve",
		"POST /api/v1/plugins/review_requests/:review_id/reject",
		"POST /api/v1/plugins/review_requests/:review_id/cancel",
		"POST /api/v1/plugins/publish",
		"POST /api/v1/plugins/delist",
		"POST " + pluginhandler.CardActionPath,
	} {
		if !registered[want] {
			t.Errorf("route %q is not mounted", want)
		}
	}
	// The callback must NOT also be reachable under the authenticated prefix: the
	// signature covers the path, so a second mount point could never verify.
	if registered["POST /api/v1"+pluginhandler.CardActionPath] {
		t.Error("the card-action callback is also mounted under /api/v1")
	}
}

// The tenant review endpoints are behind the tenant Authenticator.
func TestReviewRoutesRequireATenantToken(t *testing.T) {
	engine := reviewRouterEngine(t, routeTestCardSecret)
	for _, target := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/plugins/review_requests?mode=mine"},
		{http.MethodPost, "/api/v1/plugins/review_requests"},
		{http.MethodGet, "/api/v1/plugins/review_requests/review-1"},
		{http.MethodPost, "/api/v1/plugins/review_requests/review-1/approve"},
		{http.MethodPost, "/api/v1/plugins/review_requests/review-1/reject"},
		{http.MethodPost, "/api/v1/plugins/review_requests/review-1/cancel"},
		{http.MethodPost, "/api/v1/plugins/publish"},
		{http.MethodPost, "/api/v1/plugins/delist"},
	} {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(target.method, target.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", target.method, target.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "AUTH_REQUIRED") {
			t.Errorf("%s %s did not go through the tenant authenticator: %s", target.method, target.path, rec.Body.String())
		}
	}
}

// The card-action callback must be OUTSIDE the tenant Authenticator: octo-server
// presents an HMAC signature, not a user token. Proof that it is outside has to
// come from a request with NO token reaching the handler's own logic — here the
// signature verifies against the configured secret and the handler then refuses
// the event_id on its own terms, a 400 the authenticator never produces.
func TestCardActionCallbackBypassesTheTenantAuthenticator(t *testing.T) {
	engine := reviewRouterEngine(t, routeTestCardSecret)
	// A non-numeric event id: correctly signed, so verification passes, and then
	// rejected by the handler with its own 400.
	const eventID = "not-a-number"
	body := `{"event_id":"not-a-number","action_id":"a","decision":"approve","operator_uid":"admin-1","inputs":{},"data":{"review_id":"review-1"},"message_id":"1","channel_id":"notification","channel_type":1,"acted_at":1}`
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, pluginhandler.CardActionPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(notify.HeaderTimestamp, stamp)
	req.Header.Set(notify.HeaderEventID, eventID)
	req.Header.Set(notify.HeaderSignature, notify.Sign(routeTestCardSecret, http.MethodPost, pluginhandler.CardActionPath, stamp, eventID, []byte(body)))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 from the card handler (401 means it is behind the authenticator): %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "AUTH_REQUIRED") {
		t.Fatalf("the callback went through the tenant authenticator: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "malformed event_id") {
		t.Fatalf("did not reach the card handler: %s", rec.Body.String())
	}
}

// An unsigned callback is refused by the HMAC check, not by the tenant
// authenticator — the distinction matters because only one of them is the
// endpoint's actual authentication.
func TestCardActionCallbackRefusesUnsignedRequests(t *testing.T) {
	engine := reviewRouterEngine(t, routeTestCardSecret)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, pluginhandler.CardActionPath, strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "AUTH_REQUIRED") {
		t.Fatalf("refused by the tenant authenticator rather than the signature check: %s", rec.Body.String())
	}
}

// An unconfigured secret leaves the endpoint closed rather than open. This is the
// deployment state where the route exists but nothing may act through it.
func TestCardActionCallbackClosedWithoutASecret(t *testing.T) {
	engine := reviewRouterEngine(t, "")
	const eventID = "9007199254740993"
	body := `{"event_id":"9007199254740993","action_id":"a","decision":"approve","operator_uid":"admin-1","inputs":{},"data":{"review_id":"review-1"},"message_id":"1","channel_id":"notification","channel_type":1,"acted_at":1}`
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, pluginhandler.CardActionPath, strings.NewReader(body))
	req.Header.Set(notify.HeaderTimestamp, stamp)
	req.Header.Set(notify.HeaderEventID, eventID)
	// Signed with SOME secret; the endpoint has none configured, so it cannot match.
	req.Header.Set(notify.HeaderSignature, notify.Sign(routeTestCardSecret, http.MethodPost, pluginhandler.CardActionPath, stamp, eventID, []byte(body)))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with no secret configured", rec.Code)
	}
}
