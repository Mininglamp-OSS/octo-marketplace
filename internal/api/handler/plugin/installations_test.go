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

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type noRootInstaller struct{ calls int }

func (f *noRootInstaller) InstallCapability(context.Context, string, string, string, string, fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	f.calls++
	return nil, errors.New("unexpected Fleet call")
}

func TestPinnedInvisibleRootReturns404ThroughRealService(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectQuery("SELECT .* FROM plugin_install_attempts").WillReturnRows(sqlmock.NewRows([]string{"plugin_id"}))
	mock.ExpectQuery("SELECT .* FROM plugins p WHERE p.plugin_id IN").WillReturnRows(sqlmock.NewRows([]string{"plugin_id"}))
	remote := &noRootInstaller{}
	svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
	rec := httptest.NewRecorder()
	testEngine(svc).ServeHTTP(rec, installationRequest(installationBody))
	if rec.Code != http.StatusNotFound || remote.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, remote.calls, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

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

func TestInstallationAndLegacyAdaptersKeepTheirWireContracts(t *testing.T) {
	f := &fakeInstallationService{fakeService: &fakeService{installOutcome: &pluginsvc.InstallOutcome{AgentID: "legacy-agent"}}, out: &pluginsvc.InstallationOutcome{SquadID: "new-squad", Replayed: true}}
	r := testEngine(f)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, installationRequest(installationBody))
	if rec.Code != 200 || f.calls != 1 || f.installID != "" || rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("code=%d calls=%d body=%s", rec.Code, f.calls, rec.Body.String())
	}
	if f.id != "expert-1" || f.params.Token != "end-user-token" || f.params.WorkspaceID != "workspace-1" || f.params.IdempotencyKey != "operation-key" || f.params.ResourceName != "My reviewer" || f.params.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider" || !f.params.WorkspaceConnectors {
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

func TestInstallationDefaultsWorkspaceConnectorsWhenOmitted(t *testing.T) {
	version := "1.8.0"
	marketID := "market-calendar"
	f := &fakeInstallationService{
		fakeService: &fakeService{},
		out: &pluginsvc.InstallationOutcome{
			AgentID: "expert-1",
			Connectors: []fleet.CapabilityConnectorResult{{
				ConnectorID: "connector-calendar", Name: "Calendar", Version: &version,
				MarketMCPID: &marketID, Created: true,
			}},
		},
	}
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, installationRequest(installationBody))
	if rec.Code != http.StatusOK || f.calls != 1 || !f.params.WorkspaceConnectors {
		t.Fatalf("omitted selector did not default to connectors: code=%d calls=%d params=%+v", rec.Code, f.calls, f.params)
	}
	if connectors, ok := decodeData(t, rec.Body.Bytes())["connectors"].([]any); !ok || len(connectors) != 1 {
		t.Fatal("default connector installation response omitted connector results")
	}
}

func TestExplicitFalseInstallationKeepsLegacyResponseShape(t *testing.T) {
	version := "1.8.0"
	marketID := "market-calendar"
	f := &fakeInstallationService{
		fakeService: &fakeService{},
		out: &pluginsvc.InstallationOutcome{
			AgentID: "expert-1",
			Connectors: []fleet.CapabilityConnectorResult{{
				ConnectorID: "connector-calendar", Name: "Calendar", Version: &version,
				MarketMCPID: &marketID, Created: true,
			}},
		},
	}
	body := strings.TrimSuffix(installationBody, "}") + `,"workspace_connectors":false}`
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, installationRequest(body))
	if rec.Code != http.StatusOK || f.calls != 1 || f.params.WorkspaceConnectors {
		t.Fatalf("false selector did not preserve legacy behavior: code=%d calls=%d params=%+v", rec.Code, f.calls, f.params)
	}
	data := decodeData(t, rec.Body.Bytes())
	if data["agent_id"] != "expert-1" {
		t.Fatal("legacy installation response lost its expert id")
	}
	if _, exists := data["connectors"]; exists {
		t.Fatal("explicit false response gained connector fields")
	}
}

func TestWorkspaceConnectorInstallationAllowsMissingRuntime(t *testing.T) {
	marketID := "market-browser"
	f := &fakeInstallationService{
		fakeService: &fakeService{},
		out: &pluginsvc.InstallationOutcome{Connectors: []fleet.CapabilityConnectorResult{
			{ConnectorID: "connector-browser", Name: "Browser", MarketMCPID: &marketID, Created: true},
		}},
	}
	req := installationRequest(`{"resource_name":"Browser copy","workspace_connectors":true}`)
	req.URL.Path = "/api/v1/plugins/market-browser/installations"
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || f.calls != 1 || !f.params.WorkspaceConnectors || f.params.RuntimeID != "" {
		t.Fatalf("missing runtime connector install was not forwarded: code=%d calls=%d params=%+v", rec.Code, f.calls, f.params)
	}
	if connectors, ok := decodeData(t, rec.Body.Bytes())["connectors"].([]any); !ok || len(connectors) != 1 {
		t.Fatal("connector installation response did not include connector results")
	}
}

func TestWorkspaceConnectorInstallationRejectsInvalidProvidedRuntime(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "invalid_uuid", raw: `"not-a-uuid"`},
		{name: "empty", raw: `""`},
		{name: "whitespace", raw: `"   "`},
		{name: "null", raw: `null`},
		{name: "number", raw: `42`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeInstallationService{fakeService: &fakeService{}}
			req := installationRequest(`{"resource_name":"Browser","runtime_id":` + tc.raw + `,"workspace_connectors":true}`)
			rec := httptest.NewRecorder()
			testEngine(f).ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || f.calls != 0 {
				t.Fatalf("invalid optional runtime was not rejected before installation: code=%d calls=%d body=%s", rec.Code, f.calls, rec.Body.String())
			}
			var result struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Error.Code != "VALIDATION_ERROR" {
				t.Fatalf("error code = %q, want VALIDATION_ERROR", result.Error.Code)
			}
		})
	}
}

func TestExplicitFalseInstallationStillRequiresRuntime(t *testing.T) {
	f := &fakeInstallationService{fakeService: &fakeService{}}
	req := installationRequest(`{"resource_name":"My reviewer","workspace_connectors":false}`)
	rec := httptest.NewRecorder()
	testEngine(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || f.calls != 0 {
		t.Fatalf("explicit-false installation accepted a missing runtime: code=%d calls=%d", rec.Code, f.calls)
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
		{name: "invalid_runtime_false_mode", body: `{"runtime_id":"rt","workspace_connectors":false}`, status: 400},
		{name: "invalid_runtime_with_workspace_connectors", body: `{"runtime_id":"rt","workspace_connectors":true}`, status: 400},
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

func TestInstallationCountsCaseInsensitiveWorkspaceHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
		status int
		calls  int
	}{
		{
			name: "single_noncanonical_header",
			mutate: func(r *http.Request) {
				r.Header.Del("X-Workspace-ID")
				r.Header["x-workspace-id"] = []string{"workspace-case"}
			},
			status: http.StatusOK, calls: 1,
		},
		{
			name: "case_variant_duplicate",
			mutate: func(r *http.Request) {
				r.Header["x-workspace-id"] = []string{"workspace-other"}
			},
			status: http.StatusBadRequest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeInstallationService{fakeService: &fakeService{}, out: &pluginsvc.InstallationOutcome{AgentID: "case-expert"}}
			req := installationRequest(installationBody)
			tc.mutate(req)
			rec := httptest.NewRecorder()
			testEngine(f).ServeHTTP(rec, req)
			if rec.Code != tc.status || f.calls != tc.calls {
				t.Fatalf("code=%d calls=%d body=%s", rec.Code, f.calls, rec.Body.String())
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
		{"not_configured", expertsvc.ErrFleetNotConfigured, 503, "UPSTREAM_UNAVAILABLE"},
		{"not_found", pluginsvc.ErrNotFound, 404, "NOT_FOUND"},
		{"hidden", pluginsvc.ErrDependencyHidden, 403, "FORBIDDEN"},
		{"size", pluginsvc.ErrTooLarge, 413, "PAYLOAD_TOO_LARGE"},
		{"local-conflict", pluginsvc.ErrConflict, 409, "CONFLICT"},
		{"missing-release", pluginsvc.ErrVersionBaselineUnavailable, 409, "CONFLICT"},
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
			if (tc.name == "timeout" || tc.name == "nil_result") && out.Error.Details["phase"] != "fleet" {
				t.Fatal("uncertain result lost its Fleet phase")
			}
			if (tc.name == "preparation" || tc.name == "integrity") && out.Error.Details["phase"] != "preparation" {
				t.Fatal("preparation failure mislabeled as an uncertain Fleet result")
			}
			if tc.name == "not_configured" && out.Error.Details["reason"] != "not_configured" {
				t.Fatal("missing configuration lost its reason")
			}
		})
	}
}

func TestInstallationKeepsUnmeasuredFleetRejectionUnattributed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"error":{"code":"PAYLOAD_TOO_LARGE","message":"private-diagnostic","details":{"field":"definition.skills","reason":"must contain at most 500 supporting files"}}}`))
	}))
	defer upstream.Close()
	_, err := fleet.New(upstream.URL).InstallCapability(context.Background(), "token", "space", "workspace", "key", fleet.CapabilityInstallRequest{})
	if err == nil {
		t.Fatal("upstream rejection was accepted")
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	writeInstallationError(c, err)
	var out struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusRequestEntityTooLarge || out.Error.Code != "PAYLOAD_TOO_LARGE" || out.Error.Details["upstream"] != "fleet" {
		t.Fatalf("upstream rejection classification changed: %s", rec.Body.String())
	}
	for _, field := range []string{"actual_bytes", "limit_bytes", "actual_bytes_lower_bound"} {
		if _, exists := out.Error.Details[field]; exists {
			t.Fatalf("unreported %s in response: %s", field, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "private-diagnostic") {
		t.Fatal("upstream diagnostic was exposed")
	}
}

// octo-web surfaces an unknown-code message directly and truncates it to 200
// characters, so the capacity limit, the actual wire size and the location must
// survive inside the message itself. details may carry the full path.
type capacityDiagnosticStub struct {
	field         string
	limit, actual int64
	skill, path   string
	lowerBound    bool
}

func (e *capacityDiagnosticStub) Error() string { return "capacity exceeded" }
func (e *capacityDiagnosticStub) Unwrap() error { return pluginsvc.ErrTooLarge }
func (e *capacityDiagnosticStub) CapacityDetails() map[string]any {
	details := map[string]any{
		"field": e.field, "limit_bytes": e.limit, "actual_bytes": e.actual,
	}
	if e.skill != "" {
		details["skill"] = e.skill
	}
	if e.path != "" {
		details["path"] = e.path
	}
	if e.lowerBound {
		details["actual_bytes_lower_bound"] = true
	}
	return details
}

func TestInstallationPayloadTooLargeReportsCapacityAndLocation(t *testing.T) {
	longPath := "references/" + strings.Repeat("a", 300) + "/guide.md"
	for _, tc := range []struct {
		name       string
		err        error
		field      string
		limit      int64
		actual     int64
		skill      string
		path       string
		lowerBound bool
		wantMsg    []string
	}{
		{
			name:    "definition_json",
			err:     &capacityDiagnosticStub{field: "definition", limit: 20_000_000, actual: 20_000_001},
			field:   "definition",
			limit:   20_000_000,
			actual:  20_000_001,
			wantMsg: []string{"20000000", "20000001", "definition"},
		},
		{
			name:    "skill_file",
			err:     &capacityDiagnosticStub{field: "definition.skills.files", limit: 20_000_000, actual: 20_000_003, skill: "skill-1", path: longPath},
			field:   "definition.skills.files",
			limit:   20_000_000,
			actual:  20_000_003,
			skill:   "skill-1",
			path:    longPath,
			wantMsg: []string{"20000000", "20000003", "skill-1", longPath[:40]},
		},
		{
			name:       "unknown_object",
			err:        &capacityDiagnosticStub{field: "definition.skills.files", limit: 20_000_000, actual: 20_000_001, skill: "skill-1", path: "references/large.md", lowerBound: true},
			field:      "definition.skills.files",
			limit:      20_000_000,
			actual:     20_000_001,
			skill:      "skill-1",
			path:       "references/large.md",
			lowerBound: true,
			wantMsg:    []string{"20000000", "20000001", "skill-1", "references/large.md"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeInstallationService{fakeService: &fakeService{}, installationErr: tc.err}
			rec := httptest.NewRecorder()
			testEngine(f).ServeHTTP(rec, installationRequest(installationBody))
			var out struct {
				Error struct {
					Code    string         `json:"code"`
					Message string         `json:"message"`
					Details map[string]any `json:"details"`
					Hint    string         `json:"hint"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusRequestEntityTooLarge || out.Error.Code != "PAYLOAD_TOO_LARGE" {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if out.Error.Details["field"] != tc.field ||
				out.Error.Details["limit_bytes"] != float64(tc.limit) ||
				out.Error.Details["actual_bytes"] != float64(tc.actual) {
				t.Fatalf("details=%v", out.Error.Details)
			}
			if tc.skill != "" && out.Error.Details["skill"] != tc.skill {
				t.Fatalf("skill detail=%v", out.Error.Details["skill"])
			}
			if tc.path != "" && out.Error.Details["path"] != tc.path {
				t.Fatalf("path detail=%v", out.Error.Details["path"])
			}
			if n := len([]rune(out.Error.Message)); n > 200 {
				t.Fatalf("message is %d runes; clients truncate at 200: %q", n, out.Error.Message)
			}
			if tc.lowerBound && out.Error.Details["actual_bytes_lower_bound"] != true {
				t.Fatalf("lower-bound detail=%v", out.Error.Details["actual_bytes_lower_bound"])
			}
			for _, want := range tc.wantMsg {
				if !strings.Contains(out.Error.Message, want) {
					t.Fatalf("message %q missing %q", out.Error.Message, want)
				}
			}
			if strings.Contains(out.Error.Message, strings.Repeat("a", 100)) || strings.Contains(out.Error.Hint, strings.Repeat("a", 100)) {
				t.Fatal("message leaked file content")
			}
		})
	}
}

func TestInstallationConflictUsesSafeMessage(t *testing.T) {
	for _, tc := range []struct {
		err     fleet.CapabilityAPIError
		message string
	}{
		{fleet.CapabilityAPIError{Status: 409, Code: "DUPLICATE", IdempotencyKeyReused: true}, "幂等键已用于其他安装参数，请核对原安装记录。"},
		{fleet.CapabilityAPIError{Status: 409, Code: "CONFLICT", ConflictResource: "skill"}, "安装发生技能资源冲突，请核对工作区已有资源。"},
		{fleet.CapabilityAPIError{Status: 409, Code: "DUPLICATE", ConflictResource: "expert"}, "安装发生专家资源冲突，请核对工作区已有资源。"},
		{fleet.CapabilityAPIError{Status: 409, Code: "CONFLICT", ConflictResource: "expert_team"}, "安装发生专家团资源冲突，请核对工作区已有资源。"},
		{fleet.CapabilityAPIError{Status: 409, Code: "DUPLICATE", ConflictResource: "untrusted-secret"}, "安装发生冲突，请检查已有资源或安装参数。"},
	} {
		f := &fakeInstallationService{fakeService: &fakeService{}, installationErr: &tc.err}
		rec := httptest.NewRecorder()
		testEngine(f).ServeHTTP(rec, installationRequest(installationBody))
		var out struct {
			Error struct {
				Code, Message string
				Details       map[string]any
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 409 || out.Error.Message != tc.message || strings.Contains(rec.Body.String(), "untrusted-secret") {
			t.Fatalf("unsafe conflict response: %s", rec.Body.String())
		}
		if tc.err.IdempotencyKeyReused && out.Error.Details["conflict_reason"] != "idempotency_key_reused" {
			t.Fatal("safe conflict reason lost")
		}
	}
}

func TestInstallationFailureLogsOnlySafeClassification(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	restore := logging.Replace(zap.New(core))
	defer restore()
	for _, err := range []error{errors.New("private-submitted-content"), &pluginsvc.InstallationAttemptError{Err: errors.New("private-submitted-content")}, &fleet.CapabilityAPIError{Status: 500}} {
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
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"reason":"not_configured"`) || !strings.Contains(rec.Body.String(), "安装服务尚未配置") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestInstallationWrappedFleetRejections(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{400, "VALIDATION_ERROR"}, {401, "AUTH_REQUIRED"}, {403, "FORBIDDEN"},
		{404, "NOT_FOUND"}, {409, "DUPLICATE"}, {429, "RATE_LIMITED"},
	} {
		for _, unknown := range []bool{false, true} {
			upstream := &fleet.CapabilityAPIError{Status: tc.status, Code: "DUPLICATE", RetryAfter: "3"}
			wrapped := &pluginsvc.InstallationAttemptError{Err: upstream, Unknown: unknown}
			rec := httptest.NewRecorder()
			testEngine(&fakeInstallationService{fakeService: &fakeService{}, installationErr: wrapped}).ServeHTTP(rec, installationRequest(installationBody))
			var out struct {
				Error struct {
					Code    string
					Details map[string]any
				}
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			wantStatus, wantCode := tc.status, tc.code
			if unknown {
				wantStatus, wantCode = 503, "UPSTREAM_UNAVAILABLE"
			}
			if rec.Code != wantStatus || out.Error.Code != wantCode {
				t.Fatalf("upstream=%d unknown=%t status=%d body=%s", tc.status, unknown, rec.Code, rec.Body.String())
			}
			if unknown && out.Error.Details["reason"] != "installation_result_unknown" {
				t.Fatal("earlier uncertainty lost")
			}
			if !unknown && tc.status == 429 && rec.Header().Get("Retry-After") != "3" {
				t.Fatal("wrapped rate-limit header lost")
			}
			if unknown && rec.Header().Get("Retry-After") != "" {
				t.Fatal("unknown outcome mislabeled as safe rate-limit retry")
			}
		}
	}
}
