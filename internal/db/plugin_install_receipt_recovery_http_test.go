package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

// This fixture models the Fleet capability receipt store, not the Marketplace
// adapter: it commits the exact request bytes under the idempotency key before
// returning a lost-response 503, and only replays when the bytes match.
type capabilityReceiptFixture struct {
	t *testing.T

	mu            sync.Mutex
	receipts      map[string][]byte
	results       map[string][]byte
	installKeys   []string
	installBodies [][]byte
	replayKeys    []string
	replayBodies  [][]byte
	receiptGets   int
	commits       int
	installReply  int
	hideReceipt   bool
	replayMissing bool
	getStatus     int
}

func newCapabilityReceiptFixture(t *testing.T) *capabilityReceiptFixture {
	t.Helper()
	return &capabilityReceiptFixture{
		t: t, receipts: map[string][]byte{}, results: map[string][]byte{},
	}
}

func (f *capabilityReceiptFixture) writeJSON(w http.ResponseWriter, status int, value any, replayed bool) {
	w.Header().Set("Content-Type", "application/json")
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.WriteHeader(status)
	raw, err := json.Marshal(value)
	if err != nil {
		f.t.Errorf("encode fixture response: %v", err)
		return
	}
	_, _ = w.Write(raw)
}

func (f *capabilityReceiptFixture) writeData(w http.ResponseWriter, status int, raw []byte, replayed bool) {
	w.Header().Set("Content-Type", "application/json")
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"data":`)
	_, _ = w.Write(raw)
	_, _ = io.WriteString(w, `}`)
}

func (f *capabilityReceiptFixture) writeError(w http.ResponseWriter, status int, code, resource string) {
	envelope := map[string]any{"error": map[string]any{"code": code}}
	if resource != "" {
		envelope["error"].(map[string]any)["details"] = map[string]any{"resource": resource}
	}
	f.writeJSON(w, status, envelope, false)
}

// receiptResult builds a Fleet success payload whose identities match the
// request the fixture actually committed. Connector IDs are synthetic UUIDs;
// the receipt only needs to be structurally valid for the client verifier.
func receiptResult(request []byte, agentID string) []byte {
	var in fleet.CapabilityInstallRequest
	if json.Unmarshal(request, &in) != nil || len(in.Definition.Experts) != 1 || in.Definition.ExpertTeam != nil {
		return nil
	}
	root := in.Definition.Experts[0]
	expert := map[string]any{
		"expert_id": agentID, "name": root.Name, "version": root.Version,
		"market_expert_id": root.MarketExpertID, "created": true, "restored": false,
		"skill_ids": []string{},
	}
	result := map[string]any{"type": "expert", "expert_id": agentID, "experts": []any{expert}, "skills": []any{}}
	if len(in.Definition.Connectors) > 0 {
		byName := make(map[string]string, len(in.Definition.Connectors))
		connectors := make([]any, 0, len(in.Definition.Connectors))
		for i, connector := range in.Definition.Connectors {
			id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
			byName[connector.Name] = id
			var marketMCPID any
			if connector.MarketMCPID != "" {
				marketMCPID = connector.MarketMCPID
			}
			connectors = append(connectors, map[string]any{
				"connector_id": id, "name": connector.Name, "version": connector.Version,
				"market_mcp_id": marketMCPID, "created": true,
			})
		}
		result["connectors"] = connectors
		ids := make([]string, 0, len(root.ConnectorNames))
		for _, name := range root.ConnectorNames {
			ids = append(ids, byName[name])
		}
		expert["connector_ids"] = ids
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	return raw
}

func (f *capabilityReceiptFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities/install/receipt":
		f.mu.Lock()
		f.receiptGets++
		status, hide := f.getStatus, f.hideReceipt
		_, exists := f.receipts[key]
		f.mu.Unlock()
		if status != 0 {
			f.writeError(w, status, "INTERNAL_ERROR", "")
			return
		}
		f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"has_receipt": exists && !hide}}, false)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/capabilities/install":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			f.t.Errorf("read install body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.installKeys = append(f.installKeys, key)
		f.installBodies = append(f.installBodies, append([]byte(nil), body...))
		previous, exists := f.receipts[key]
		if exists {
			if !bytes.Equal(previous, body) {
				f.mu.Unlock()
				f.writeError(w, http.StatusConflict, "DUPLICATE", "idempotency_key")
				return
			}
			result := append([]byte(nil), f.results[key]...)
			f.mu.Unlock()
			f.writeData(w, http.StatusOK, result, true)
			return
		}
		f.commits++
		result := receiptResult(body, fmt.Sprintf("agent-legacy-%d", f.commits))
		reply := f.installReply
		if reply == 0 {
			reply = http.StatusServiceUnavailable
		}
		if result == nil {
			f.mu.Unlock()
			f.writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "")
			return
		}
		f.receipts[key] = append([]byte(nil), body...)
		f.results[key] = result
		f.mu.Unlock()
		if reply == http.StatusOK {
			f.writeData(w, http.StatusOK, result, false)
			return
		}
		f.writeError(w, reply, "UPSTREAM_UNAVAILABLE", "")
	case r.Method == http.MethodPost && r.URL.Path == "/v1/capabilities/install/receipt":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			f.t.Errorf("read receipt body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.replayKeys = append(f.replayKeys, key)
		f.replayBodies = append(f.replayBodies, append([]byte(nil), body...))
		missing := f.replayMissing
		previous, exists := f.receipts[key]
		var result []byte
		if exists {
			result = append([]byte(nil), f.results[key]...)
		}
		f.mu.Unlock()
		if missing || !exists {
			f.writeError(w, http.StatusNotFound, "NOT_FOUND", "capability_installation_receipt")
			return
		}
		if !bytes.Equal(previous, body) {
			f.writeError(w, http.StatusConflict, "DUPLICATE", "idempotency_key")
			return
		}
		f.writeData(w, http.StatusOK, result, true)
	default:
		f.t.Errorf("unexpected Fleet request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (f *capabilityReceiptFixture) counts() (gets, installs, replays, commits int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.receiptGets, len(f.installBodies), len(f.replayBodies), f.commits
}

func (f *capabilityReceiptFixture) installCall(t *testing.T, index int) (string, []byte) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if index >= len(f.installBodies) {
		t.Fatalf("install call %d missing (have %d)", index+1, len(f.installBodies))
	}
	return f.installKeys[index], append([]byte(nil), f.installBodies[index]...)
}

func (f *capabilityReceiptFixture) replayCall(t *testing.T, index int) (string, []byte) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if index >= len(f.replayBodies) {
		t.Fatalf("receipt replay call %d missing (have %d)", index+1, len(f.replayBodies))
	}
	return f.replayKeys[index], append([]byte(nil), f.replayBodies[index]...)
}

func (f *capabilityReceiptFixture) setInstallReply(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installReply = status
}

func (f *capabilityReceiptFixture) setGetStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getStatus = status
}

func (f *capabilityReceiptFixture) setHideReceipt(hide bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hideReceipt = hide
}

func (f *capabilityReceiptFixture) setReplayMissing(missing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replayMissing = missing
}

type pluginReceiptTest struct {
	engine   *gin.Engine
	pluginID string
	fleet    *capabilityReceiptFixture
}

func receiptPluginWrite() pluginsvc.WriteRequest {
	manifest := json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Receipt Expert","plugin_type":"expert","name":"receipt-expert","description":"Receipt recovery fixture.","labels":[],"examples":[]}`)
	pkg := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"AGENTS.md","content_type":"raw","mime_type":"text/markdown","raw_content":"released instructions"},{"path":"mcp.json","content_type":"raw","mime_type":"application/json","raw_content":"{\"mcpServers\":{\"search\":{\"command\":\"search\"}}}"}]}`)
	return pluginsvc.WriteRequest{
		Name: "Receipt Expert", Type: model.PluginTypeExpert, Visibility: model.PluginVisibilityPrivate,
		Version: "1.0.0", Manifest: manifest, Package: pkg, Tags: json.RawMessage(`[]`),
	}
}

func newPluginReceiptTest(t *testing.T, write pluginsvc.WriteRequest) *pluginReceiptTest {
	t.Helper()
	database := reviewDB(t)
	fixture := newCapabilityReceiptFixture(t)
	upstream := httptest.NewServer(fixture)
	t.Cleanup(upstream.Close)
	svc := pluginsvc.New(pluginrepo.New(database)).
		WithVersionManagement(true).
		WithReferences(false).
		WithPinnedInstallation(false).
		WithCapabilityInstaller(fleet.New(upstream.URL))
	root, err := svc.Create(context.Background(), managedOwner(), write)
	if err != nil {
		t.Fatalf("create receipt fixture plugin: %v", err)
	}
	if _, err := svc.Publish(context.Background(), managedOwner(), explicitTestPublication(t, database, root.Plugin.ID)); err != nil {
		t.Fatalf("publish receipt fixture plugin: %v", err)
	}
	// Publish the fixture, then exercise recruitment with all install flags disabled.
	svc.WithVersionManagement(false)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "user-1", Name: "Owner"}, "space-a")
	pluginhandler.New(svc).Register(engine.Group("/api/v1", auth.Handler()))
	return &pluginReceiptTest{engine: engine, pluginID: root.Plugin.ID, fleet: fixture}
}

const receiptRuntimeID = "7f63db70-6b66-4d2c-a52f-fb6de1d78ad9"

func (p *pluginReceiptTest) send(t *testing.T, modern bool, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/v1/plugins/install"
	if modern {
		path = "/api/v1/plugins/" + p.pluginID + "/installations"
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Token", "fixture-token")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if modern {
		req.Header.Set("X-Workspace-ID", "workspace-1")
	}
	rec := httptest.NewRecorder()
	p.engine.ServeHTTP(rec, req)
	return rec
}

func (p *pluginReceiptTest) legacyBody(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"plugin_id": p.pluginID, "workspace_id": "workspace-1", "runtime_id": receiptRuntimeID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (p *pluginReceiptTest) modernBody(t *testing.T, selector, customEnv string) string {
	t.Helper()
	body := `{"runtime_id":"` + receiptRuntimeID + `"` + selector
	if customEnv != "" {
		body += `,"custom_env":` + customEnv
	}
	return body + `}`
}

type installWireView struct {
	Definition fleet.CapabilityDefinition `json:"definition"`
	Bindings   fleet.CapabilityBindings   `json:"bindings"`
}

func decodeInstallWire(t *testing.T, body []byte) installWireView {
	t.Helper()
	var view installWireView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode Fleet install wire: %v; body=%s", err, body)
	}
	return view
}

func installWireMode(t *testing.T, body []byte) (connectors bool, mcpConfig bool) {
	t.Helper()
	view := decodeInstallWire(t, body)
	if len(view.Definition.Connectors) > 0 {
		connectors = true
	}
	for _, expert := range view.Definition.Experts {
		if len(expert.ConnectorNames) > 0 {
			connectors = true
		}
		if len(expert.MCPConfig) > 0 {
			mcpConfig = true
		}
	}
	return connectors, mcpConfig
}

type receiptHTTPResponse struct {
	Data struct {
		AgentID    string `json:"agent_id"`
		SquadID    string `json:"squad_id"`
		Connectors []struct {
			Name string `json:"name"`
		} `json:"connectors"`
	} `json:"data"`
	Error struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func decodeReceiptResponse(t *testing.T, rec *httptest.ResponseRecorder) receiptHTTPResponse {
	t.Helper()
	var response receiptHTTPResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode HTTP response: %v; body=%s", err, rec.Body.String())
	}
	return response
}

func TestNonPinnedDefaultCurrentReceiptOutlivesLegacyCapacity(t *testing.T) {
	write := receiptPluginWrite()
	args := make([]string, 101)
	for i := range args {
		args[i] = "--query"
	}
	mcp, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"search": map[string]any{"command": "search", "args": args}}})
	if err != nil {
		t.Fatal(err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(write.Package, &pkg); err != nil {
		t.Fatal(err)
	}
	pkg["attachments"].([]any)[1].(map[string]any)["raw_content"] = string(mcp)
	write.Package, err = json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	harness := newPluginReceiptTest(t, write)
	harness.fleet.setInstallReply(http.StatusOK)
	const key = "receipt-current"
	body := harness.modernBody(t, "", "")
	first := harness.send(t, true, body, key)
	if first.Code != http.StatusOK {
		t.Fatalf("initial connector install status=%d body=%s", first.Code, first.Body.String())
	}
	_, installedBody := harness.fleet.installCall(t, 0)
	if connectors, legacy := installWireMode(t, installedBody); !connectors || legacy {
		t.Fatalf("initial install did not use connectors: %s", installedBody)
	}
	retried := harness.send(t, true, body, key)
	if retried.Code != http.StatusOK || retried.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("matching current receipt status=%d body=%s", retried.Code, retried.Body.String())
	}
	if !bytes.Equal(first.Body.Bytes(), retried.Body.Bytes()) {
		t.Fatalf("matching receipt returned different resources: first=%s replay=%s", first.Body.String(), retried.Body.String())
	}
	gets, installs, replays, commits := harness.fleet.counts()
	if gets != 2 || installs != 1 || replays != 1 || commits != 1 {
		t.Fatalf("current receipt counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
	replayedKey, replayedBody := harness.fleet.replayCall(t, 0)
	if replayedKey != key || !bytes.Equal(installedBody, replayedBody) {
		t.Fatalf("current receipt changed its operation key or request bytes")
	}
}

// A pre-upgrade non-pinned operation committed the legacy MCP wire under a
// caller key and then lost its response. After the connector-default upgrade,
// both plugin entry points must recover that exact operation by read-only
// replay instead of submitting the new default wire under the same key.
func TestNonPinnedDefaultRecoversLegacyReceiptOnBothPluginRoutes(t *testing.T) {
	harness := newPluginReceiptTest(t, receiptPluginWrite())
	const key = "receipt-legacy"

	original := harness.send(t, true, harness.modernBody(t, `,"workspace_connectors":false`, ""), key)
	if original.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial explicit-false install status=%d body=%s", original.Code, original.Body.String())
	}
	gets, installs, replays, commits := harness.fleet.counts()
	if gets != 0 || installs != 1 || replays != 0 || commits != 1 {
		t.Fatalf("initial send counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
	frozenKey, frozenBody := harness.fleet.installCall(t, 0)
	if frozenKey != key {
		t.Fatalf("initial Fleet key=%q want %q", frozenKey, key)
	}
	if connectors, mcpConfig := installWireMode(t, frozenBody); connectors || !mcpConfig {
		t.Fatalf("explicit-false baseline wire changed: connectors=%t mcp_config=%t body=%s", connectors, mcpConfig, frozenBody)
	}

	assertReplayed := func(rec *httptest.ResponseRecorder, wantReplay int, wantInstalls int) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("receipt recovery status=%d replay_header=%q body=%s", rec.Code, rec.Header().Get("Idempotency-Replayed"), rec.Body.String())
		}
		response := decodeReceiptResponse(t, rec)
		if response.Data.AgentID != "agent-legacy-1" {
			t.Fatalf("receipt recovery returned a different resource: %s", rec.Body.String())
		}
		gotGets, gotInstalls, gotReplays, gotCommits := harness.fleet.counts()
		if gotInstalls != wantInstalls || gotReplays != wantReplay || gotCommits != 1 || gotGets == 0 {
			t.Fatalf("recovery counts: gets=%d installs=%d replays=%d commits=%d", gotGets, gotInstalls, gotReplays, gotCommits)
		}
		replayKey, replayBody := harness.fleet.replayCall(t, wantReplay-1)
		if replayKey != key || !bytes.Equal(replayBody, frozenBody) {
			t.Fatalf("receipt replay changed the frozen key or wire: key=%q same_body=%t", replayKey, bytes.Equal(replayBody, frozenBody))
		}
	}

	// The old /plugins/install adapter omits the selector and must recover the
	// frozen legacy operation through the read-only receipt.
	assertReplayed(harness.send(t, false, harness.legacyBody(t), key), 1, 1)
	// The new route's omitted selector behaves the same.
	modernReplay := harness.send(t, true, harness.modernBody(t, "", ""), key)
	assertReplayed(modernReplay, 2, 1)
	if modernReplay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("modern receipt recovery lost its replay metadata")
	}

	// An exact explicit-false retry remains a normal same-wire replay.
	explicitFalse := harness.send(t, true, harness.modernBody(t, `,"workspace_connectors":false`, ""), key)
	if explicitFalse.Code != http.StatusOK || explicitFalse.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("explicit-false replay status=%d body=%s", explicitFalse.Code, explicitFalse.Body.String())
	}
	explicitFalseKey, explicitFalseBody := harness.fleet.installCall(t, 1)
	if explicitFalseKey != key || !bytes.Equal(explicitFalseBody, frozenBody) {
		t.Fatalf("explicit-false retry changed the frozen key or wire")
	}

	// Explicit true is still a mode selection and conflicts with the legacy
	// receipt; it never recovers the operation into the other mode.
	explicitTrue := harness.send(t, true, harness.modernBody(t, `,"workspace_connectors":true`, ""), key)
	if explicitTrue.Code != http.StatusConflict {
		t.Fatalf("explicit true status=%d body=%s", explicitTrue.Code, explicitTrue.Body.String())
	}
	gets, installs, replays, commits = harness.fleet.counts()
	if installs != 3 || replays != 2 || commits != 1 || gets == 0 {
		t.Fatalf("explicit true mutated the receipt: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}

	// A different caller key is a new operation and receives the connector
	// default. Its presence probe is read-only before the single write.
	harness.fleet.setInstallReply(http.StatusOK)
	beforeGets, _, _, _ := harness.fleet.counts()
	newOperation := harness.send(t, true, harness.modernBody(t, "", ""), "octo222-new-default-operation")
	if newOperation.Code != http.StatusOK || newOperation.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("new-key default install status=%d body=%s", newOperation.Code, newOperation.Body.String())
	}
	response := decodeReceiptResponse(t, newOperation)
	if response.Data.AgentID != "agent-legacy-2" || len(response.Data.Connectors) != 1 {
		t.Fatalf("new-key connector install response changed: %s", newOperation.Body.String())
	}
	gets, installs, replays, commits = harness.fleet.counts()
	if installs != 4 || replays != 2 || commits != 2 || gets <= beforeGets {
		t.Fatalf("new-key counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
	newKey, newBody := harness.fleet.installCall(t, 3)
	if newKey != "octo222-new-default-operation" {
		t.Fatalf("new operation Fleet key=%q", newKey)
	}
	if connectors, mcpConfig := installWireMode(t, newBody); !connectors || mcpConfig {
		t.Fatalf("new operation did not use connector mode: connectors=%t mcp_config=%t body=%s", connectors, mcpConfig, newBody)
	}

	// Changed input under the frozen key conflicts without any new mutation.
	changed := harness.send(t, true, harness.modernBody(t, "", `{"OCTOBUDDY_PROVIDER_ID":"changed"}`), key)
	if changed.Code != http.StatusConflict {
		t.Fatalf("changed-input retry status=%d body=%s", changed.Code, changed.Body.String())
	}
	gets, installs, replays, commits = harness.fleet.counts()
	if installs != 4 || commits != 2 {
		t.Fatalf("changed input wrote after a receipt mismatch: installs=%d commits=%d", installs, commits)
	}
	if replays != 4 {
		t.Fatalf("changed input replay attempts=%d, want current and legacy candidates", replays)
	}
}

// A concurrent same-key operation can commit between the presence read and the
// default-wire send. The reused key must be recovered by read-only replay, not
// by another mode's write.
func TestNonPinnedDefaultReceiptPresenceRaceUsesReadOnlyRecovery(t *testing.T) {
	harness := newPluginReceiptTest(t, receiptPluginWrite())
	const key = "octo222-receipt-presence-race"

	if rec := harness.send(t, true, harness.modernBody(t, `,"workspace_connectors":false`, ""), key); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial explicit-false install status=%d body=%s", rec.Code, rec.Body.String())
	}
	_, frozenBody := harness.fleet.installCall(t, 0)
	harness.fleet.setHideReceipt(true)

	recovered := harness.send(t, true, harness.modernBody(t, "", ""), key)
	if recovered.Code != http.StatusOK || recovered.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("presence-race recovery status=%d body=%s", recovered.Code, recovered.Body.String())
	}
	if agent := decodeReceiptResponse(t, recovered).Data.AgentID; agent != "agent-legacy-1" {
		t.Fatalf("presence-race recovered resource=%q", agent)
	}
	gets, installs, replays, commits := harness.fleet.counts()
	if gets != 1 || installs != 2 || replays != 1 || commits != 1 {
		t.Fatalf("presence-race counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
	_, replayBody := harness.fleet.replayCall(t, 0)
	if !bytes.Equal(replayBody, frozenBody) {
		t.Fatalf("presence-race recovery did not replay the frozen wire")
	}
}

// A failed presence read must not be converted into a mutation.
func TestNonPinnedDefaultReceiptReadFailureDoesNotWrite(t *testing.T) {
	harness := newPluginReceiptTest(t, receiptPluginWrite())
	const key = "octo222-receipt-read-failure"
	if rec := harness.send(t, true, harness.modernBody(t, `,"workspace_connectors":false`, ""), key); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial explicit-false install status=%d body=%s", rec.Code, rec.Body.String())
	}
	harness.fleet.setGetStatus(http.StatusInternalServerError)

	failed := harness.send(t, true, harness.modernBody(t, "", ""), key)
	if failed.Code != http.StatusServiceUnavailable {
		t.Fatalf("presence failure status=%d body=%s", failed.Code, failed.Body.String())
	}
	_, installs, replays, commits := harness.fleet.counts()
	if installs != 1 || replays != 0 || commits != 1 {
		t.Fatalf("presence failure wrote: installs=%d replays=%d commits=%d", installs, replays, commits)
	}
}

// A receipt that disappears between the presence read and the replay is not
// licensed to create a new operation from the read failure.
func TestNonPinnedDefaultMissingReceiptAfterPresenceDoesNotWrite(t *testing.T) {
	harness := newPluginReceiptTest(t, receiptPluginWrite())
	const key = "octo222-receipt-expired-boundary"
	if rec := harness.send(t, true, harness.modernBody(t, `,"workspace_connectors":false`, ""), key); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial explicit-false install status=%d body=%s", rec.Code, rec.Body.String())
	}
	harness.fleet.setReplayMissing(true)

	vanished := harness.send(t, true, harness.modernBody(t, "", ""), key)
	if vanished.Code != http.StatusConflict {
		t.Fatalf("vanished receipt status=%d body=%s", vanished.Code, vanished.Body.String())
	}
	_, installs, replays, commits := harness.fleet.counts()
	if installs != 1 || replays != 1 || commits != 1 {
		t.Fatalf("vanished receipt wrote a replacement: installs=%d replays=%d commits=%d", installs, replays, commits)
	}
}

// The legacy route without a caller key starts a new operation; a generated key
// cannot have a pre-existing receipt and must not trigger a presence read.
func TestLegacyPluginRouteKeylessInstallSkipsReceiptProbe(t *testing.T) {
	harness := newPluginReceiptTest(t, receiptPluginWrite())
	harness.fleet.setInstallReply(http.StatusOK)

	rec := harness.send(t, false, harness.legacyBody(t), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("keyless legacy install status=%d body=%s", rec.Code, rec.Body.String())
	}
	gets, installs, replays, commits := harness.fleet.counts()
	if gets != 0 || installs != 1 || replays != 0 || commits != 1 {
		t.Fatalf("keyless counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
	}
	fleetKey, body := harness.fleet.installCall(t, 0)
	if fleetKey == "" {
		t.Fatal("keyless install did not generate a Fleet idempotency key")
	}
	connectors, mcpConfig := installWireMode(t, body)
	if !connectors || mcpConfig {
		t.Fatalf("keyless legacy install did not use connector mode: connectors=%t mcp_config=%t", connectors, mcpConfig)
	}
}

func TestNonPinnedDefaultReceiptPermissionErrorDoesNotInstall(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			harness := newPluginReceiptTest(t, receiptPluginWrite())
			harness.fleet.setGetStatus(status)
			for _, modern := range []bool{false, true} {
				body := harness.legacyBody(t)
				if modern {
					body = harness.modernBody(t, "", "")
				}
				response := harness.send(t, modern, body, "receipt-auth")
				if response.Code != status {
					t.Fatalf("receipt permission status=%d want=%d body=%s", response.Code, status, response.Body.String())
				}
			}
			gets, installs, replays, commits := harness.fleet.counts()
			if gets != 2 || installs != 0 || replays != 0 || commits != 0 {
				t.Fatalf("receipt permission failure counts: gets=%d installs=%d replays=%d commits=%d", gets, installs, replays, commits)
			}
		})
	}
}
