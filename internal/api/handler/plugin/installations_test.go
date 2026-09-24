package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
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
		{name: "null_env_value", body: strings.Replace(installationBody, `"provider"`, `null`, 1), status: 400},
		{name: "invalid_utf8_env", body: strings.Replace(installationBody, "provider\"", "secret\xff\"", 1), status: 400},
		{name: "unpaired_high_surrogate_env", body: strings.Replace(installationBody, `"provider"`, `"secret\ud800"`, 1), status: 400},
		{name: "unpaired_low_surrogate_env", body: strings.Replace(installationBody, `"provider"`, `"secret\udc00"`, 1), status: 400},
		{name: "unpaired_surrogate_before_escape", body: strings.Replace(installationBody, `"provider"`, `"secret\ud800\\udc00"`, 1), status: 400},
		{name: "invalid_name", body: strings.Replace(installationBody, "My reviewer", strings.Repeat("名", 129), 1), status: 400},
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
			if strings.IndexFunc(rec.Body.String(), func(r rune) bool { return unicode.Is(unicode.Han, r) }) < 0 {
				t.Fatal("validation response must contain Chinese display text")
			}
		})
	}
}

func TestInstallationPreservesEnvironmentStrings(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`""`, ""},
		{`"  value\n\t "`, "  value\n\t "},
		{`"中文\ud83d\ude80"`, "中文🚀"},
		{`"\ufffd"`, "\ufffd"},
		{`"literal\\ud800"`, `literal\ud800`},
		{`"https:\/\/example.test"`, "https://example.test"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			f := &fakeInstallationService{fakeService: &fakeService{}, out: &pluginsvc.InstallationOutcome{AgentID: "new-agent"}}
			rec := httptest.NewRecorder()
			testEngine(f).ServeHTTP(rec, installationRequest(strings.Replace(installationBody, `"provider"`, tc.raw, 1)))
			if rec.Code != 200 || f.calls != 1 || f.params.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != tc.want {
				t.Fatalf("environment string changed: status=%d calls=%d", rec.Code, f.calls)
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
		{"timeout", &pluginsvc.InstallationAttemptError{Err: errors.New("secret-env-value token internal-url")}, 503, "UPSTREAM_UNAVAILABLE"},
		{"preparation", errors.New("secret-env-value token internal-url"), 500, "INTERNAL_ERROR"},
		{"integrity", pluginsvc.ErrIntegrity, 500, "INTERNAL_ERROR"},
		{"upstream_validation", &fleet.CapabilityAPIError{Status: 400}, 400, "VALIDATION_ERROR"},
		{"upstream_auth", &fleet.CapabilityAPIError{Status: 401}, 401, "AUTH_REQUIRED"},
		{"upstream_forbidden", &fleet.CapabilityAPIError{Status: 403}, 403, "FORBIDDEN"},
		{"upstream_missing", &fleet.CapabilityAPIError{Status: 404}, 404, "NOT_FOUND"},
		{"conflict", &fleet.CapabilityAPIError{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED"}, 409, "CONFLICT"},
		{"duplicate", &fleet.CapabilityAPIError{Status: 409, Code: "DUPLICATE"}, 409, "DUPLICATE"},
		{"idempotency_duplicate", &fleet.CapabilityAPIError{Status: 409, Code: "DUPLICATE", IdempotencyKeyReused: true}, 409, "DUPLICATE"},
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
					Message string         `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(rec.Body.Bytes(), &out) != nil || rec.Code != tc.status || out.Error.Code != tc.code {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret-env-value") || strings.Contains(rec.Header().Get("Retry-After"), "secret") {
				t.Fatal("upstream details leaked")
			}
			if strings.IndexFunc(out.Error.Message, func(r rune) bool { return unicode.Is(unicode.Han, r) }) < 0 || strings.IndexFunc(out.Error.Hint, func(r rune) bool { return unicode.Is(unicode.Han, r) }) < 0 {
				t.Fatal("self-generated message and hint must be Chinese")
			}
			if (tc.name == "conflict" || tc.name == "idempotency_duplicate") && out.Error.Details["conflict_reason"] != "idempotency_key_reused" {
				t.Fatal("conflict reason lost")
			}
			if tc.name == "rate_limit" && rec.Header().Get("Retry-After") != "3" {
				t.Fatal("retry delay lost")
			}
			if (tc.name == "timeout" || tc.name == "nil_result") && (!strings.Contains(out.Error.Hint, "不要切换安装接口") || out.Error.Details["phase"] != "fleet") {
				t.Fatal("unsafe retry guidance")
			}
			if (tc.name == "preparation" || tc.name == "integrity") && out.Error.Details["phase"] != "preparation" {
				t.Fatal("preparation failure mislabeled as an uncertain Fleet result")
			}
		})
	}
}

func TestInstallationConflictUsesUpstreamMessage(t *testing.T) {
	for _, message := range []string{"技能名称已被不同内容占用", "Conflict from Fleet", "", "  "} {
		f := &fakeInstallationService{fakeService: &fakeService{}, installationErr: &fleet.CapabilityAPIError{Status: 409, Code: "DUPLICATE", IdempotencyKeyReused: true, Message: message}}
		rec := httptest.NewRecorder()
		testEngine(f).ServeHTTP(rec, installationRequest(installationBody))
		var out struct {
			Error struct {
				Code, Message string
				Details       map[string]any
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		want := message
		if strings.TrimSpace(message) == "" {
			want = "安装发生冲突，请检查已有资源或安装参数。"
		}
		if rec.Code != 409 || out.Error.Code != "DUPLICATE" || out.Error.Message != want || out.Error.Details["conflict_reason"] != "idempotency_key_reused" {
			t.Fatalf("unexpected conflict response: %s", rec.Body.String())
		}
	}
}

func TestInstallationFailureLogsOnlySafeClassification(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	restore := logging.Replace(zap.New(core))
	defer restore()
	for _, err := range []error{errors.New("private-submitted-content"), &pluginsvc.InstallationAttemptError{Err: errors.New("private-submitted-content")}, &fleet.CapabilityAPIError{Status: 500, Message: "private-submitted-content"}} {
		f := &fakeInstallationService{fakeService: &fakeService{}, installationErr: err}
		testEngine(f).ServeHTTP(httptest.NewRecorder(), installationRequest(installationBody))
	}
	entries := logs.FilterMessage("capability_installation_failed").All()
	if len(entries) != 3 {
		t.Fatalf("failure log count=%d", len(entries))
	}
	for i, entry := range entries {
		fields := entry.ContextMap()
		if fields["reason"] == "" || fields["phase"] == "" || fields["operation"] != "plugin.installation.create" {
			t.Fatal("safe failure classification missing")
		}
		if i == 0 && fields["phase"] != "preparation" {
			t.Fatal("preparation logged as Fleet failure")
		}
		data, _ := json.Marshal(fields)
		if strings.Contains(string(data), "private-submitted-content") || strings.Contains(string(data), "end-user-token") {
			t.Fatal("failure log leaked input")
		}
	}
}

func TestInstallationUnavailableWithoutNewService(t *testing.T) {
	rec := httptest.NewRecorder()
	testEngine(&fakeService{}).ServeHTTP(rec, installationRequest(installationBody))
	if rec.Code != 503 {
		t.Fatalf("code=%d", rec.Code)
	}
}
