package expert

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	expertsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/expert"
	"github.com/gin-gonic/gin"
)

// A keyless legacy install that already reached Fleet but whose outcome is
// unknown (dropped connection or an unparseable success body) must return the
// key that was actually dispatched inside error.details.idempotency_key. That
// key is the only way the caller can retry without duplicating the install.
// The stub commits a receipt before losing the response, so retrying with the
// reported key must replay the same root UUID and create nothing new.
type recoveryFleetOutcome int

const (
	recoveryCloseConnection recoveryFleetOutcome = iota + 1
	recoveryTruncatedSuccess
	recoveryStatus
)

type recoveryFleetStub struct {
	t        *testing.T
	mu       sync.Mutex
	outcome  recoveryFleetOutcome
	status   int
	served   bool
	receipts map[string]string
	calls    []string
}

func newRecoveryFleetStub(outcome recoveryFleetOutcome, status int) *recoveryFleetStub {
	return &recoveryFleetStub{outcome: outcome, status: status, receipts: map[string]string{}}
}

func newRecoveryFleetServer(t *testing.T, stub *recoveryFleetStub) *httptest.Server {
	t.Helper()
	stub.t = t
	server := httptest.NewServer(http.HandlerFunc(stub.install))
	t.Cleanup(server.Close)
	return server
}

func (s *recoveryFleetStub) install(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/capabilities/install" {
		http.NotFound(w, r)
		return
	}
	var request struct {
		Definition fleet.CapabilityDefinition `json:"definition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if len(request.Definition.Experts) == 0 {
		http.Error(w, "missing experts", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Idempotency-Key")

	s.mu.Lock()
	s.calls = append(s.calls, key)
	if s.outcome == recoveryStatus {
		status := s.status
		s.mu.Unlock()
		writeRecoveryFleetError(w, status)
		return
	}
	root, known := s.receipts[key]
	unknown := false
	if !known {
		root = fmt.Sprintf("5f0c1e88-0000-4000-8000-%012d", len(s.receipts)+1)
		s.receipts[key] = root
		if !s.served {
			s.served = true
			unknown = true
		}
	}
	s.mu.Unlock()

	if unknown {
		if s.outcome == recoveryCloseConnection {
			// httptest's HTTP/1 writer always implements http.Hijacker. Refuse to
			// silently degrade to the truncated-success path: a broken fixture
			// must fail the test, not change what is being measured.
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				s.t.Errorf("Fleet stub: response writer does not implement http.Hijacker")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				s.t.Errorf("Fleet stub: hijack connection: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":`)
		return
	}
	writeRecoveryFleetResult(w, request.Definition, root)
}

func (s *recoveryFleetStub) receivedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *recoveryFleetStub) createdCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.receipts)
}

func (s *recoveryFleetStub) rootFor(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.receipts[key]
}

func writeRecoveryFleetError(w http.ResponseWriter, status int) {
	code := "INTERNAL_ERROR"
	if status == http.StatusConflict {
		code = "DUPLICATE"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code}})
}

// writeRecoveryFleetResult answers with the receipt Fleet returns for a fresh
// commit: created resources, request-sourced market ids and nullable versions,
// and empty (not null) skill collections. Both legacy endpoints replay the same
// committed root UUID from this shape.
func writeRecoveryFleetResult(w http.ResponseWriter, definition fleet.CapabilityDefinition, root string) {
	result := fleet.CapabilityInstallResult{
		Experts: []fleet.CapabilityExpertResult{},
		Skills:  []fleet.CapabilitySkillResult{},
	}
	if definition.ExpertTeam != nil {
		result.Type = "expert_team"
		result.ExpertTeamID = root
		result.ExpertTeam = &fleet.CapabilityExpertTeamResult{
			ExpertTeamID:       root,
			Name:               definition.ExpertTeam.Name,
			Version:            definition.ExpertTeam.Version,
			MarketExpertTeamID: definition.ExpertTeam.MarketExpertTeamID,
			Created:            true,
		}
		for i, expert := range definition.Experts {
			expertID := fmt.Sprintf("6a0d2f10-0000-4000-8000-%012d", i+1)
			entry := fleet.CapabilityExpertResult{
				ExpertID:       expertID,
				Name:           expert.Name,
				Version:        expert.Version,
				MarketExpertID: expert.MarketExpertID,
				Created:        true,
				SkillIDs:       []string{},
			}
			for _, member := range definition.ExpertTeam.Members {
				if member.ExpertName == expert.Name && member.IsLeader {
					entry.IsLeader = true
					if result.LeaderExpertID == "" {
						result.LeaderExpertID = expertID
					}
				}
			}
			result.Experts = append(result.Experts, entry)
		}
	} else {
		expert := definition.Experts[0]
		result.Type = "expert"
		result.ExpertID = root
		result.Experts = []fleet.CapabilityExpertResult{{
			ExpertID:       root,
			Name:           expert.Name,
			Version:        expert.Version,
			MarketExpertID: expert.MarketExpertID,
			Created:        true,
			SkillIDs:       []string{},
		}}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": result})
}

func recoveryExpertFixture() *model.Expert {
	return &model.Expert{
		ID: "exp-1", Name: "Code Helper", Visibility: model.VisibilityPublic, SpaceID: "space-1",
		Instruction: "Write code.",
	}
}

func recoverySquadFixture() *model.Squad {
	return &model.Squad{
		ID: "squad-1", Name: "Delivery Squad", Visibility: model.VisibilityPublic, SpaceID: "space-1",
		Members: []model.SquadMember{{MemberKey: "leader", Name: "Lead", IsLeader: true, Instruction: "Lead work."}},
	}
}

func sequentialInstallKeys() func() string {
	next := 0
	return func() string {
		next++
		return fmt.Sprintf("generated-install-%d", next)
	}
}

func recoveryInstallRouter(t *testing.T, store expertsvc.Store, stub *recoveryFleetStub, idGen func() string) *gin.Engine {
	t.Helper()
	server := newRecoveryFleetServer(t, stub)
	return legacyInstallRouter(expertsvc.New(store, nil, idGen).WithFleet(fleet.New(server.URL)))
}

func postLegacyInstall(router *gin.Engine, path, idempotencyKey string) *httptest.ResponseRecorder {
	return postLegacyInstallBody(router, path, `{"workspace_id":"workspace-1","runtime_id":"runtime-1"}`, idempotencyKey)
}

func postLegacyInstallBody(router *gin.Engine, path, body, idempotencyKey string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer legacy-caller-token")
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeLegacyInstallData(t *testing.T, response *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode install response: %v", err)
	}
	return body.Data
}

func decodeLegacyInstallError(t *testing.T, response *httptest.ResponseRecorder) (string, map[string]any) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode install error envelope: %v", err)
	}
	return envelope.Error.Code, envelope.Error.Details
}

// requireRecoveryIdempotencyKey asserts the dispatched-outcome 503 carries a
// readable recovery key and returns it.
func requireRecoveryIdempotencyKey(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	code, details := decodeLegacyInstallError(t, response)
	if code != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("error.code = %q, want UPSTREAM_UNAVAILABLE", code)
	}
	key, _ := details["idempotency_key"].(string)
	if key == "" {
		t.Fatalf("error.details.idempotency_key missing after a dispatched install (details %#v)", details)
	}
	return key
}

// requireNoRecoveryIdempotencyKey asserts an error that never reached Fleet (or
// a 4xx whose shape must stay unchanged) invents no recovery credential.
func requireNoRecoveryIdempotencyKey(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	_, details := decodeLegacyInstallError(t, response)
	if key, ok := details["idempotency_key"]; ok {
		t.Fatalf("error.details.idempotency_key = %#v, want no recovery key", key)
	}
}

func TestLegacyExpertInstallKeylessUnknownOutcomeReturnsRecoverableIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := newRecoveryFleetStub(recoveryCloseConnection, 0)
	router := recoveryInstallRouter(t, &legacyInstallStore{expert: recoveryExpertFixture()}, stub, sequentialInstallKeys())
	const path = "/v1/experts/exp-1/install"

	first := postLegacyInstall(router, path, "")
	recoveryKey := requireRecoveryIdempotencyKey(t, first)
	if calls := stub.receivedKeys(); len(calls) != 1 || calls[0] != recoveryKey {
		t.Fatalf("Fleet calls = %v, want exactly the reported key %q", calls, recoveryKey)
	}
	root := stub.rootFor(recoveryKey)
	if root == "" || stub.createdCount() != 1 {
		t.Fatalf("Fleet receipts: root %q, %d created; want one committed install", root, stub.createdCount())
	}

	retried := postLegacyInstall(router, path, recoveryKey)
	if retried.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200: %s", retried.Code, retried.Body.String())
	}
	if data := decodeLegacyInstallData(t, retried); len(data) != 1 || data["agent_id"] != root {
		t.Fatalf("retry data = %#v, want only agent_id %q", data, root)
	}
	if stub.createdCount() != 1 {
		t.Fatalf("retry created %d installs, want the original single install", stub.createdCount())
	}

	fresh := postLegacyInstall(router, path, "")
	if fresh.Code != http.StatusOK {
		t.Fatalf("fresh keyless status = %d, want 200: %s", fresh.Code, fresh.Body.String())
	}
	if data := decodeLegacyInstallData(t, fresh); data["agent_id"] == root || data["agent_id"] == "" {
		t.Fatalf("fresh keyless agent_id = %q, want a new install distinct from %q", data["agent_id"], root)
	}
	calls := stub.receivedKeys()
	if len(calls) != 3 {
		t.Fatalf("Fleet calls = %v, want one dispatch, one retry, one fresh install", calls)
	}
	if calls[2] != "generated-install-2" {
		t.Fatalf("fresh keyless Fleet key = %q, want a newly generated key", calls[2])
	}
	if stub.createdCount() != 2 {
		t.Fatalf("Fleet installs = %d, want the fresh install committed separately", stub.createdCount())
	}
}

func TestLegacySquadInstallKeylessUnknownOutcomeReturnsRecoverableIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := newRecoveryFleetStub(recoveryTruncatedSuccess, 0)
	router := recoveryInstallRouter(t, &legacyInstallStore{squad: recoverySquadFixture()}, stub, sequentialInstallKeys())
	const path = "/v1/squads/squad-1/install"

	first := postLegacyInstall(router, path, "")
	recoveryKey := requireRecoveryIdempotencyKey(t, first)
	if calls := stub.receivedKeys(); len(calls) != 1 || calls[0] != recoveryKey {
		t.Fatalf("Fleet calls = %v, want exactly the reported key %q", calls, recoveryKey)
	}
	root := stub.rootFor(recoveryKey)
	if root == "" || stub.createdCount() != 1 {
		t.Fatalf("Fleet receipts: root %q, %d created; want one committed install", root, stub.createdCount())
	}

	retried := postLegacyInstall(router, path, recoveryKey)
	if retried.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200: %s", retried.Code, retried.Body.String())
	}
	data := decodeLegacyInstallData(t, retried)
	if len(data) != 2 || data["squad_id"] != root || data["leader_agent_id"] == "" {
		t.Fatalf("retry data = %#v, want only squad_id %q and a leader agent", data, root)
	}
	if stub.createdCount() != 1 {
		t.Fatalf("retry created %d installs, want the original single install", stub.createdCount())
	}

	fresh := postLegacyInstall(router, path, "")
	if fresh.Code != http.StatusOK {
		t.Fatalf("fresh keyless status = %d, want 200: %s", fresh.Code, fresh.Body.String())
	}
	if data := decodeLegacyInstallData(t, fresh); data["squad_id"] == root || data["squad_id"] == "" {
		t.Fatalf("fresh keyless squad_id = %q, want a new install distinct from %q", data["squad_id"], root)
	}
	calls := stub.receivedKeys()
	if len(calls) != 3 {
		t.Fatalf("Fleet calls = %v, want one dispatch, one retry, one fresh install", calls)
	}
	if calls[2] != "generated-install-2" {
		t.Fatalf("fresh keyless Fleet key = %q, want a newly generated key", calls[2])
	}
	if stub.createdCount() != 2 {
		t.Fatalf("Fleet installs = %d, want the fresh install committed separately", stub.createdCount())
	}
}

func TestLegacyInstallDispatched5xxReturnsActualIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		path   string
		status int
		store  expertsvc.Store
	}{
		{
			name: "expert internal error", path: "/v1/experts/exp-1/install",
			status: http.StatusInternalServerError,
			store:  &legacyInstallStore{expert: recoveryExpertFixture()},
		},
		{
			name: "squad bad gateway", path: "/v1/squads/squad-1/install",
			status: http.StatusBadGateway,
			store:  &legacyInstallStore{squad: recoverySquadFixture()},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newRecoveryFleetStub(recoveryStatus, tc.status)
			router := recoveryInstallRouter(t, tc.store, stub, sequentialInstallKeys())

			recoveryKey := requireRecoveryIdempotencyKey(t, postLegacyInstall(router, tc.path, ""))
			if calls := stub.receivedKeys(); len(calls) != 1 || calls[0] != recoveryKey {
				t.Fatalf("Fleet calls = %v, want the reported key %q", calls, recoveryKey)
			}
		})
	}
}

func TestLegacyInstallExplicitIdempotencyKeyEchoedOnDispatched5xx(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name  string
		path  string
		key   string
		store expertsvc.Store
	}{
		{
			name: "expert", path: "/v1/experts/exp-1/install",
			key:   "marketplace/expert-install:v1+opaque_recovery",
			store: &legacyInstallStore{expert: recoveryExpertFixture()},
		},
		{
			name: "squad", path: "/v1/squads/squad-1/install",
			key:   "marketplace/squad-install:v1+opaque_recovery",
			store: &legacyInstallStore{squad: recoverySquadFixture()},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newRecoveryFleetStub(recoveryStatus, http.StatusInternalServerError)
			router := recoveryInstallRouter(t, tc.store, stub, sequentialInstallKeys())

			recoveryKey := requireRecoveryIdempotencyKey(t, postLegacyInstall(router, tc.path, tc.key))
			if recoveryKey != tc.key {
				t.Fatalf("error.details.idempotency_key = %q, want the caller key %q", recoveryKey, tc.key)
			}
			if calls := stub.receivedKeys(); len(calls) != 1 || calls[0] != tc.key {
				t.Fatalf("Fleet calls = %v, want the caller key %q", calls, tc.key)
			}
		})
	}
}

func TestLegacyInstallUndispatched503HasNoRecoveryKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name  string
		path  string
		store expertsvc.Store
	}{
		{name: "expert", path: "/v1/experts/exp-1/install", store: &legacyInstallStore{expert: recoveryExpertFixture()}},
		{name: "squad", path: "/v1/squads/squad-1/install", store: &legacyInstallStore{squad: recoverySquadFixture()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No Fleet client wired: nothing was dispatched, so there is no key
			// to recover and none may be invented.
			router := legacyInstallRouter(expertsvc.New(tc.store, nil, sequentialInstallKeys()))
			response := postLegacyInstall(router, tc.path, "")
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
			}
			if code, _ := decodeLegacyInstallError(t, response); code != "UPSTREAM_UNAVAILABLE" {
				t.Fatalf("error.code = %q, want UPSTREAM_UNAVAILABLE", code)
			}
			requireNoRecoveryIdempotencyKey(t, response)
		})
	}
}

func TestLegacyInstallValidationFailureDispatchesNothingAndHasNoRecoveryKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := newRecoveryFleetStub(recoveryCloseConnection, 0)
	router := recoveryInstallRouter(t, &legacyInstallStore{expert: recoveryExpertFixture()}, stub, sequentialInstallKeys())

	response := postLegacyInstallBody(router, "/v1/experts/exp-1/install", `{"runtime_id":"runtime-1"}`, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	if code, _ := decodeLegacyInstallError(t, response); code != "VALIDATION_ERROR" {
		t.Fatalf("error.code = %q, want VALIDATION_ERROR", code)
	}
	requireNoRecoveryIdempotencyKey(t, response)
	if calls := stub.receivedKeys(); len(calls) != 0 {
		t.Fatalf("Fleet calls = %v, want no dispatch for a validation failure", calls)
	}
}

func TestLegacyInstallCapability4xxKeepsStatusWithoutRecoveryKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const explicitKey = "marketplace/expert-install:v1+opaque_4xx"
	stub := newRecoveryFleetStub(recoveryStatus, http.StatusConflict)
	router := recoveryInstallRouter(t, &legacyInstallStore{expert: recoveryExpertFixture()}, stub, sequentialInstallKeys())

	response := postLegacyInstall(router, "/v1/experts/exp-1/install", explicitKey)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
	}
	if code, _ := decodeLegacyInstallError(t, response); code != "CONFLICT" {
		t.Fatalf("error.code = %q, want CONFLICT", code)
	}
	requireNoRecoveryIdempotencyKey(t, response)
}
