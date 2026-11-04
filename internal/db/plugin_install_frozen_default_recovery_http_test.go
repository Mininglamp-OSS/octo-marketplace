package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

type frozenRecoveryFleetRequest struct {
	body []byte
	key  string
}

type frozenRecoveryEvidence struct {
	keyDigest          string
	intentDigest       string
	inputFingerprint   string
	requestFingerprint string
	hmacKeyID          string
	frozenPlan         string
	retryUntil         string
}

// A frozen legacy-mode attempt can be retried through either selector-free
// route without changing the request Fleet already saw or its receipt window.
func TestPinnedFrozenLegacyModeRecoversOnOmittedHTTPSelectorMySQL(t *testing.T) {
	database := reviewDB(t)
	seedMigrationRoot(t, database, "octo222-recovery-team", "private", "published")
	applyCurrentMigration(t, database)

	var fleetRequests []frozenRecoveryFleetRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/capabilities/install" {
			t.Errorf("unexpected Fleet request: %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read Fleet request: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fleetRequests = append(fleetRequests, frozenRecoveryFleetRequest{
			body: append([]byte(nil), body...), key: r.Header.Get("Idempotency-Key"),
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"synthetic unknown result"}}`)
	}))
	defer upstream.Close()

	service := pluginsvc.New(pluginrepo.New(database)).
		WithVersionManagement(true).
		WithReferences(true).
		WithPinnedInstallation(true).
		WithInstallationHMACKeys("test", map[string]string{
			"test": "synthetic-installation-test-key-32-bytes",
		}).
		WithCapabilityInstaller(fleet.New(upstream.URL))
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Owner"}, "space-a")
	pluginhandler.New(service).Register(engine.Group("/api/v1", auth.Handler()))

	const (
		pluginID    = "octo222-recovery-team"
		workspaceID = "fixture-workspace"
		runtimeID   = "7f63db70-6b66-4d2c-a52f-fb6de1d78ad9"
		key         = "octo222-frozen-legacy-mode"
		fleetKey    = "7b669385e9e89d264129dc6eaa1f73b35d163574fbfe5d1484e39e0c7e6d0a03"
		legacyRoute = "/api/v1/plugins/install"
	)
	modernRoute := "/api/v1/plugins/" + pluginID + "/installations"
	modernBody := func(selector string) string {
		return `{"runtime_id":"` + runtimeID + `"` + selector + `}`
	}
	send := func(path, body, idempotencyKey string, modern bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Token", "fixture-token")
		req.Header.Set("Idempotency-Key", idempotencyKey)
		if modern {
			req.Header.Set("X-Workspace-ID", workspaceID)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}
	sendLegacy := func(idempotencyKey string) *httptest.ResponseRecorder {
		return send(legacyRoute, `{"plugin_id":"`+pluginID+`","workspace_id":"`+workspaceID+`","runtime_id":"`+runtimeID+`"}`, idempotencyKey, false)
	}
	sendModern := func(selector, idempotencyKey string) *httptest.ResponseRecorder {
		return send(modernRoute, modernBody(selector), idempotencyKey, true)
	}
	assertUnknown := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		var response struct {
			Error struct {
				Details map[string]any `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode HTTP error: %v; body=%s", err, rec.Body.String())
		}
		if rec.Code != http.StatusServiceUnavailable || response.Error.Details["reason"] != "installation_result_unknown" {
			t.Fatalf("want unknown-result 503, got status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	assertMode := func(body []byte, workspaceConnectors bool) {
		t.Helper()
		var request fleet.CapabilityInstallRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("decode captured Fleet request: %v", err)
		}
		if len(request.Definition.Experts) == 0 || len(request.Definition.Experts) != len(request.Bindings.Experts) {
			t.Fatalf("captured Fleet request lost expert runtime bindings: %#v", request)
		}
		if workspaceConnectors {
			if len(request.Definition.Connectors) == 0 {
				t.Fatalf("connector-default Fleet request omitted connectors: %s", body)
			}
			for _, expert := range request.Definition.Experts {
				if len(expert.ConnectorNames) == 0 || len(expert.MCPConfig) != 0 {
					t.Fatalf("connector-default Fleet request lost expert bindings: %s", body)
				}
			}
			return
		}
		if len(request.Definition.Connectors) != 0 {
			t.Fatalf("explicit-false Fleet request included connector resources: %s", body)
		}
		for _, expert := range request.Definition.Experts {
			if len(expert.ConnectorNames) != 0 || len(expert.MCPConfig) == 0 {
				t.Fatalf("explicit-false Fleet request did not retain legacy MCP config: %s", body)
			}
		}
	}
	assertCapturedOriginal := func(index int, original []byte) {
		t.Helper()
		if len(fleetRequests) != index+1 {
			t.Fatalf("Fleet calls=%d, want %d", len(fleetRequests), index+1)
		}
		if fleetRequests[index].key != fleetKey || string(fleetRequests[index].body) != string(original) {
			t.Fatalf("retry changed frozen Fleet key or wire: call=%d key=%q want_key=%q same_body=%t", index+1, fleetRequests[index].key, fleetKey, string(fleetRequests[index].body) == string(original))
		}
	}

	// Start with an explicitly selected legacy request, pin its plan and make
	// Fleet's result unknown through the real HTTP client.
	original := sendModern(`,"workspace_connectors":false`, key)
	assertUnknown(original)
	if len(fleetRequests) != 1 {
		t.Fatalf("initial explicit-false installation sent %d Fleet requests", len(fleetRequests))
	}
	if fleetRequests[0].key != fleetKey {
		t.Fatalf("initial Fleet request used key %q, want frozen Fleet key %q", fleetRequests[0].key, fleetKey)
	}
	assertMode(fleetRequests[0].body, false)

	readEvidence := func(idempotencyKey string) frozenRecoveryEvidence {
		t.Helper()
		keyJSON, err := json.Marshal(idempotencyKey)
		if err != nil {
			t.Fatal(err)
		}
		keySum := sha256.Sum256(keyJSON)
		keyDigest := hex.EncodeToString(keySum[:])
		var plan []byte
		var retryUntil sql.NullTime
		var evidence frozenRecoveryEvidence
		err = database.QueryRow(`SELECT key_digest,intent_digest,input_fingerprint,request_fingerprint,hmac_key_id,frozen_plan_json,retry_until FROM plugin_install_attempts WHERE space_id='space-a' AND caller_uid='user-1' AND workspace_id=? AND plugin_id=? AND key_digest=?`, workspaceID, pluginID, keyDigest).Scan(
			&evidence.keyDigest, &evidence.intentDigest, &evidence.inputFingerprint, &evidence.requestFingerprint, &evidence.hmacKeyID, &plan, &retryUntil,
		)
		if err != nil {
			t.Fatalf("read frozen installation evidence: %v", err)
		}
		if !retryUntil.Valid {
			t.Fatal("pinned unknown attempt did not persist its original retry deadline")
		}
		evidence.frozenPlan = string(plan)
		evidence.retryUntil = retryUntil.Time.UTC().Format(time.RFC3339Nano)
		return evidence
	}
	beforeRetry := readEvidence(key)
	if beforeRetry.intentDigest == "" || beforeRetry.inputFingerprint == "" || beforeRetry.requestFingerprint == "" || beforeRetry.hmacKeyID != "test" || beforeRetry.frozenPlan == "" {
		t.Fatalf("pinned attempt lacks frozen fingerprint evidence: %+v", beforeRetry)
	}
	assertEvidenceUnchanged := func(idempotencyKey string, before frozenRecoveryEvidence) {
		t.Helper()
		if after := readEvidence(idempotencyKey); after != before {
			t.Fatalf("selector recovery changed frozen attempt evidence: before=%+v after=%+v", before, after)
		}
	}

	// The old endpoint omits the selector too; it must recover this exact
	// pinned false-mode operation rather than treating it as a new choice.
	legacyRetry := sendLegacy(key)
	assertUnknown(legacyRetry)
	assertCapturedOriginal(1, fleetRequests[0].body)
	assertEvidenceUnchanged(key, beforeRetry)

	// The new endpoint also distinguishes omission from explicit true/false.
	// Omission retries the frozen false plan, while explicit true still conflicts.
	modernRetry := sendModern("", key)
	assertUnknown(modernRetry)
	assertCapturedOriginal(2, fleetRequests[0].body)
	assertEvidenceUnchanged(key, beforeRetry)

	// JSON null remains selector-defaulted and therefore recovers the same
	// frozen false-mode plan without changing the Fleet request.
	nullRetry := sendModern(`,"workspace_connectors":null`, key)
	assertUnknown(nullRetry)
	assertCapturedOriginal(3, fleetRequests[0].body)
	assertEvidenceUnchanged(key, beforeRetry)

	// An exact explicit-false retry remains supported and keeps the same receipt
	// deadline; recovery does not rewrite the persisted input or request MACs.
	explicitFalseRetry := sendModern(`,"workspace_connectors":false`, key)
	assertUnknown(explicitFalseRetry)
	assertCapturedOriginal(4, fleetRequests[0].body)
	assertEvidenceUnchanged(key, beforeRetry)

	beforeConflict := len(fleetRequests)
	explicitTrue := sendModern(`,"workspace_connectors":true`, key)
	if explicitTrue.Code != http.StatusConflict || len(fleetRequests) != beforeConflict {
		t.Fatalf("explicit true changed the frozen legacy operation: status=%d fleet_calls=%d body=%s", explicitTrue.Code, len(fleetRequests)-beforeConflict, explicitTrue.Body.String())
	}
	assertEvidenceUnchanged(key, beforeRetry)

	// A different key is a new operation and therefore gets the connector mode
	// default, independently of the frozen legacy plan above.
	newKey := "octo222-new-default-connectors"
	newDefault := sendModern("", newKey)
	assertUnknown(newDefault)
	if len(fleetRequests) != beforeConflict+1 {
		t.Fatalf("new-key omitted-selector request sent %d Fleet calls", len(fleetRequests)-beforeConflict)
	}
	assertMode(fleetRequests[len(fleetRequests)-1].body, true)
	assertEvidenceUnchanged(key, beforeRetry)

	// The reverse mismatch is also a conflict: a selector-free new-key request
	// freezes connector mode, and explicit false cannot change that operation.
	newDefaultEvidence := readEvidence(newKey)
	beforeExplicitFalse := len(fleetRequests)
	explicitFalseAfterDefault := sendModern(`,"workspace_connectors":false`, newKey)
	if explicitFalseAfterDefault.Code != http.StatusConflict || len(fleetRequests) != beforeExplicitFalse {
		t.Fatalf("explicit false changed the frozen connector operation: status=%d fleet_calls=%d body=%s", explicitFalseAfterDefault.Code, len(fleetRequests)-beforeExplicitFalse, explicitFalseAfterDefault.Body.String())
	}
	assertEvidenceUnchanged(newKey, newDefaultEvidence)
	assertEvidenceUnchanged(key, beforeRetry)
}
