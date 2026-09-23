package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type fakeInstallationService struct {
	*fakeService
	calls           int
	id              string
	params          pluginsvc.InstallationParams
	out             *pluginsvc.InstallationOutcome
	installationErr error
}

func (f *fakeInstallationService) CreateInstallation(_ context.Context, c pluginsvc.Caller, id string, p pluginsvc.InstallationParams) (*pluginsvc.InstallationOutcome, error) {
	f.calls++
	f.caller, f.id, f.params = c, id, p
	return f.out, f.installationErr
}

const installationBody = `{"resource_name":"My reviewer","runtime_id":"7f63db70-6b66-4c2d-a52f-fb6de1d78ad9","custom_env":{"OCTOBUDDY_PROVIDER_ID":"provider"}}`

func installationRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/expert-1/installations", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer end-user-token")
	r.Header.Set("X-Workspace-ID", "workspace-1")
	r.Header.Set("Idempotency-Key", "operation-key")
	return r
}

func TestInstallationAndLegacyRoutesRemainIndependent(t *testing.T) {
	f := &fakeInstallationService{fakeService: &fakeService{installOutcome: &pluginsvc.InstallOutcome{AgentID: "legacy-agent"}}, out: &pluginsvc.InstallationOutcome{SquadID: "new-squad", Replayed: true}}
	r := testEngine(f)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, installationRequest(installationBody))
	if rec.Code != 200 || f.calls != 1 || f.installID != "" || rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("code=%d calls=%d body=%s", rec.Code, f.calls, rec.Body.String())
	}
	if f.id != "expert-1" || f.params.Token != "end-user-token" || f.params.WorkspaceID != "workspace-1" || f.params.IdempotencyKey != "operation-key" || f.params.ResourceName != "My reviewer" || f.params.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider" {
		t.Fatal("new installation arguments were not forwarded")
	}
	if f.caller.UID != "user-1" || f.caller.SpaceID != "space-a" {
		t.Fatal("identity was not derived from the authenticator")
	}
	if data := decodeData(t, rec.Body.Bytes()); data["squad_id"] != "new-squad" || data["agent_id"] != nil {
		t.Fatalf("unexpected envelope: %v", data)
	}
	rec = httptest.NewRecorder()
	legacy := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/install", strings.NewReader(`{"plugin_id":"legacy-1","workspace_id":"ws","runtime_id":"rt"}`))
	legacy.Header.Set("Token", "legacy-token")
	r.ServeHTTP(rec, legacy)
	if rec.Code != 200 || f.calls != 1 || f.installID != "legacy-1" || f.installParams.Token != "legacy-token" {
		t.Fatalf("legacy route changed: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestInstallationRejectsInvalidRequestsBeforeService(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		mutate func(*http.Request)
		status int
	}{
		{name: "missing_workspace", mutate: func(r *http.Request) { r.Header.Del("X-Workspace-ID") }, status: 400},
		{name: "duplicate_workspace", mutate: func(r *http.Request) { r.Header.Add("X-Workspace-ID", "other") }, status: 400},
		{name: "slug_not_supported", mutate: func(r *http.Request) { r.Header.Set("X-Workspace-Slug", "slug") }, status: 400},
		{name: "missing_key", mutate: func(r *http.Request) { r.Header.Del("Idempotency-Key") }, status: 400},
		{name: "duplicate_key", mutate: func(r *http.Request) { r.Header.Add("Idempotency-Key", "other") }, status: 400},
		{name: "invalid_key", mutate: func(r *http.Request) { r.Header.Set("Idempotency-Key", strings.Repeat("x", 201)) }, status: 400},
		{name: "invalid_runtime", body: `{"runtime_id":"rt"}`, status: 400},
		{name: "forged_identity", body: strings.TrimSuffix(installationBody, "}") + `,"uid":"other"}`, status: 400},
		{name: "body_workspace", body: strings.TrimSuffix(installationBody, "}") + `,"workspace_id":"other"}`, status: 400},
		{name: "forged_space", body: strings.TrimSuffix(installationBody, "}") + `,"space_id":"other"}`, status: 400},
		{name: "unknown_field", body: strings.TrimSuffix(installationBody, "}") + `,"definition":{}}`, status: 400},
		{name: "invalid_env", body: strings.Replace(installationBody, "OCTOBUDDY_PROVIDER_ID", "INVALID-NAME", 1), status: 400},
		{name: "nul_env", body: strings.Replace(installationBody, `"provider"`, `"secret\u0000value"`, 1), status: 400},
		{name: "invalid_name", body: strings.Replace(installationBody, "My reviewer", strings.Repeat("名", 201), 1), status: 400},
		{name: "trailing_json", body: installationBody + `{}`, status: 400},
		{name: "oversized", body: `{"resource_name":"` + strings.Repeat("x", maxBodyBytes) + `"}`, status: 413},
		{name: "wrong_media", mutate: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, status: 415},
		{name: "compressed", mutate: func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, status: 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeInstallationService{fakeService: &fakeService{}}
			body := tc.body
			if body == "" {
				body = installationBody
			}
			req := installationRequest(body)
			if tc.mutate != nil {
				tc.mutate(req)
			}
			rec := httptest.NewRecorder()
			testEngine(f).ServeHTTP(rec, req)
			if rec.Code != tc.status || f.calls != 0 {
				t.Fatalf("code=%d calls=%d body=%s", rec.Code, f.calls, rec.Body.String())
			}
		})
	}
}

func TestInstallationRequiresAuthentication(t *testing.T) {
	f := &fakeInstallationService{fakeService: &fakeService{}}
	r := gin.New()
	New(f).Register(r.Group("/api/v1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, installationRequest(installationBody))
	if rec.Code != 401 || f.calls != 0 {
		t.Fatalf("code=%d calls=%d", rec.Code, f.calls)
	}
}

func TestInstallationErrorMappingAndSafeRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"disabled", fleet.ErrCapabilityInstallDisabled, 503, "UPSTREAM_UNAVAILABLE"},
		{"not_found", pluginsvc.ErrNotFound, 404, "NOT_FOUND"},
		{"hidden", pluginsvc.ErrDependencyHidden, 403, "FORBIDDEN"},
		{"size", pluginsvc.ErrTooLarge, 413, "PAYLOAD_TOO_LARGE"},
		{"validation", &pluginsvc.InstallationValidationError{Field: "runtime_id", Reason: "invalid_uuid"}, 400, "VALIDATION_ERROR"},
		{"timeout", errors.New("secret-env-value token internal-url"), 503, "UPSTREAM_UNAVAILABLE"},
		{"upstream_validation", &fleet.CapabilityAPIError{Status: 400}, 400, "VALIDATION_ERROR"},
		{"upstream_auth", &fleet.CapabilityAPIError{Status: 401}, 401, "AUTH_REQUIRED"},
		{"upstream_forbidden", &fleet.CapabilityAPIError{Status: 403}, 403, "FORBIDDEN"},
		{"upstream_missing", &fleet.CapabilityAPIError{Status: 404}, 404, "NOT_FOUND"},
		{"conflict", &fleet.CapabilityAPIError{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED"}, 409, "CONFLICT"},
		{"duplicate", &fleet.CapabilityAPIError{Status: 409, Code: "DUPLICATE"}, 409, "DUPLICATE"},
		{"rate_limit", &fleet.CapabilityAPIError{Status: 429, RetryAfter: "3"}, 429, "RATE_LIMITED"},
		{"bad_retry_after", &fleet.CapabilityAPIError{Status: 429, RetryAfter: "secret-env-value"}, 429, "RATE_LIMITED"},
		{"upstream_failure", &fleet.CapabilityAPIError{Status: 500, Code: "secret-env-value"}, 503, "UPSTREAM_UNAVAILABLE"},
		{"nil_result", nil, 503, "UPSTREAM_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeInstallationService{fakeService: &fakeService{}, installationErr: tc.err}
			rec := httptest.NewRecorder()
			testEngine(f).ServeHTTP(rec, installationRequest(installationBody))
			var out struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
					Hint    string         `json:"hint"`
				} `json:"error"`
			}
			if json.Unmarshal(rec.Body.Bytes(), &out) != nil || rec.Code != tc.status || out.Error.Code != tc.code {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret-env-value") || strings.Contains(rec.Header().Get("Retry-After"), "secret") {
				t.Fatal("upstream details leaked")
			}
			if tc.name == "conflict" && out.Error.Details["conflict_reason"] != "idempotency_key_reused" {
				t.Fatal("conflict reason lost")
			}
			if tc.name == "rate_limit" && rec.Header().Get("Retry-After") != "3" {
				t.Fatal("retry delay lost")
			}
			if (tc.name == "timeout" || tc.name == "nil_result") && !strings.Contains(out.Error.Hint, "do not switch") {
				t.Fatal("unsafe retry guidance")
			}
		})
	}
}

func TestInstallationUnavailableWithoutNewService(t *testing.T) {
	rec := httptest.NewRecorder()
	testEngine(&fakeService{}).ServeHTTP(rec, installationRequest(installationBody))
	if rec.Code != 503 {
		t.Fatalf("code=%d", rec.Code)
	}
}
