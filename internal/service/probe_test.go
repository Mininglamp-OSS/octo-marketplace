package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// newProbeService returns a Service with a nil store — Probe never touches
// persistence, so the store methods are unused. Using nil surfaces any
// accidental store call as an immediate nil deref instead of hiding it.
func newProbeService() *Service {
	return (&Service{store: nil, now: time.Now}).WithProbeAllowPrivate(true)
}

// mcpFakeServer stubs out an MCP streamable-http server that can serve
// initialize → notifications/initialized → tools/list. Each hook is optional;
// missing methods return HTTP 500 with a JSON-RPC error.
type mcpFakeServer struct {
	mu     sync.Mutex
	calls  []string
	tools  []wireTool
	caps   map[string]any
	server *httptest.Server
	// sseMode: when true, responds with text/event-stream framing.
	sseMode bool
	// requireHeader: when set, requests missing this header get 401.
	requireHeader string
}

func newFakeMCP(t *testing.T, tools []wireTool, caps map[string]any) *mcpFakeServer {
	t.Helper()
	f := &mcpFakeServer{tools: tools, caps: caps}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *mcpFakeServer) URL() string { return f.server.URL }

func (f *mcpFakeServer) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *mcpFakeServer) handle(w http.ResponseWriter, r *http.Request) {
	if f.requireHeader != "" && r.Header.Get(f.requireHeader) == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, req.Method)
	f.mu.Unlock()

	switch req.Method {
	case "initialize":
		capsJSON, _ := json.Marshal(f.caps)
		res := map[string]any{
			"protocolVersion": probeProtocolVersion,
			"capabilities":    json.RawMessage(capsJSON),
			"serverInfo":      map[string]string{"name": "fake-mcp", "version": "0.1.0"},
		}
		f.writeResult(w, req.ID, res)
	case "notifications/initialized":
		// Notification — respond 202 with empty body (server may also 200/204).
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		res := map[string]any{"tools": f.tools}
		f.writeResult(w, req.ID, res)
	default:
		f.writeError(w, req.ID, -32601, "Method not found")
	}
}

func (f *mcpFakeServer) writeResult(w http.ResponseWriter, id any, result any) {
	body := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	raw, _ := json.Marshal(body)
	if f.sseMode {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (f *mcpFakeServer) writeError(w http.ResponseWriter, id any, code int, message string) {
	body := map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	}
	raw, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// ─── Tests ───────────────────────────────────────────────────────────────────

func TestProbe_HappyPath_JSON(t *testing.T) {
	fake := newFakeMCP(t, []wireTool{
		{Name: "list_repos", Description: "List repos"},
		{Name: "create_issue", Description: "Create issue"},
	}, map[string]any{"tools": map[string]any{"listChanged": false}})

	svc := newProbeService()
	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       fake.URL(),
	})
	if apiErr != nil {
		t.Fatalf("unexpected apierr: %v", apiErr)
	}
	if !resp.OK {
		t.Fatalf("expected ok=true, got %+v", resp)
	}
	if len(resp.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(resp.Tools))
	}
	if resp.Tools[0].Name != "list_repos" {
		t.Fatalf("expected first tool list_repos, got %q", resp.Tools[0].Name)
	}
	if resp.ServerInfo == nil || resp.ServerInfo.Name != "fake-mcp" {
		t.Fatalf("expected serverInfo.name=fake-mcp, got %+v", resp.ServerInfo)
	}
	calls := fake.Calls()
	if len(calls) < 3 || calls[0] != "initialize" || calls[2] != "tools/list" {
		t.Fatalf("expected initialize -> notif -> tools/list, got %v", calls)
	}
}

func TestProbe_HappyPath_SSE(t *testing.T) {
	fake := newFakeMCP(t, []wireTool{{Name: "ping", Description: "ping"}},
		map[string]any{"tools": map[string]any{}})
	fake.sseMode = true

	svc := newProbeService()
	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       fake.URL(),
	})
	if apiErr != nil {
		t.Fatalf("unexpected apierr: %v", apiErr)
	}
	if !resp.OK || len(resp.Tools) != 1 || resp.Tools[0].Name != "ping" {
		t.Fatalf("bad response: %+v", resp)
	}
}

func TestProbe_NoToolsCapability(t *testing.T) {
	// initialize succeeds but declares no tools capability → no_tools_capability.
	fake := newFakeMCP(t, nil, map[string]any{"prompts": map[string]any{}})

	svc := newProbeService()
	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       fake.URL(),
	})
	if apiErr != nil {
		t.Fatalf("unexpected apierr: %v", apiErr)
	}
	if resp.OK {
		t.Fatalf("expected ok=false")
	}
	if resp.Error == nil || resp.Error.Code != ProbeErrNoToolsCapability {
		t.Fatalf("expected no_tools_capability, got %+v", resp.Error)
	}
}

func TestProbe_ServerErrorOnInitialize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer srv.Close()

	svc := newProbeService()
	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       srv.URL,
	})
	if apiErr != nil {
		t.Fatalf("unexpected apierr: %v", apiErr)
	}
	if resp.OK {
		t.Fatalf("expected ok=false, got %+v", resp)
	}
	if resp.Error == nil || resp.Error.Code != ProbeErrInitFailed {
		t.Fatalf("expected init_failed, got %+v", resp.Error)
	}
}

func TestProbe_Timeout(t *testing.T) {
	// Server sleeps longer than the client's context deadline. The client
	// aborts with DeadlineExceeded; the server handler eventually returns
	// so httptest.Server.Close() doesn't block the whole test run.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	svc := newProbeService()
	resp, apiErr := svc.Probe(ctx, ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       srv.URL,
	})
	if apiErr != nil {
		t.Fatalf("unexpected apierr: %v", apiErr)
	}
	if resp.OK {
		t.Fatalf("expected ok=false")
	}
	if resp.Error == nil || resp.Error.Code != ProbeErrTimeout {
		t.Fatalf("expected timeout, got %+v", resp.Error)
	}
}

func TestProbe_ForwardsHeaders(t *testing.T) {
	fake := newFakeMCP(t, []wireTool{{Name: "x", Description: "x"}},
		map[string]any{"tools": map[string]any{}})
	fake.requireHeader = "X-Custom-Auth"

	svc := newProbeService()
	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       fake.URL(),
		Headers:   map[string]string{"X-Custom-Auth": "abc"},
	})
	if apiErr != nil || !resp.OK {
		t.Fatalf("expected ok, got apiErr=%v resp=%+v", apiErr, resp)
	}
}

func TestProbe_DropsSecretSentinelInHeaders(t *testing.T) {
	// If the user leaves Authorization blank, the frontend submits the ASCII
	// sentinel — Probe must NOT forward the literal sentinel to the server.
	fake := newFakeMCP(t, []wireTool{{Name: "x", Description: "x"}},
		map[string]any{"tools": map[string]any{}})
	fake.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); strings.Contains(got, model.SecretPlaceholderSentinel) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		fake.handle(w, r)
	})

	svc := newProbeService()
	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       fake.URL(),
		Headers:   map[string]string{"Authorization": model.SecretPlaceholderSentinel},
	})
	if apiErr != nil {
		t.Fatalf("unexpected apierr: %v", apiErr)
	}
	if !resp.OK {
		t.Fatalf("expected ok=true, got %+v", resp)
	}
}

func TestProbe_StdioRejected(t *testing.T) {
	svc := newProbeService()
	_, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStdio,
		Command:   "npx",
	})
	if apiErr == nil {
		t.Fatalf("expected apierr for stdio")
	}
	if apiErr.Code != apierr.CodeProbeUnsupported {
		t.Fatalf("expected probe_unsupported, got %q", apiErr.Code)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", apiErr.Status)
	}
}

func TestProbe_InvalidURL(t *testing.T) {
	svc := newProbeService()
	cases := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"non-http", "ftp://example.com/mcp"},
		{"file", "file:///etc/passwd"},
		{"malformed", "://nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, apiErr := svc.Probe(context.Background(), ProbeRequest{
				Transport: model.TransportStreamableHTTP,
				URL:       tc.url,
			})
			if apiErr == nil {
				t.Fatalf("expected apierr for %q", tc.url)
			}
			if apiErr.Code != apierr.CodeInvalidRequest {
				t.Fatalf("expected invalid_request, got %q", apiErr.Code)
			}
		})
	}
}

func TestProbe_BlocksPrivateTargetsByDefault(t *testing.T) {
	svc := New(nil)
	for _, rawURL := range []string{
		"http://127.0.0.1:8080/mcp",
		"http://[::1]:8080/mcp",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.1/mcp",
		"http://100.64.0.1/mcp",
		"http://[64:ff9b::7f00:1]/mcp",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, apiErr := svc.Probe(context.Background(), ProbeRequest{
				Transport: model.TransportStreamableHTTP,
				URL:       rawURL,
			})
			if apiErr == nil || apiErr.Code != apierr.CodeInvalidRequest {
				t.Fatalf("expected private target rejection, got %v", apiErr)
			}
		})
	}
}

func TestIsUnsafeProbeIP_NAT64EmbeddedIPv4(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "64:ff9b::7f00:1", want: true},
		{raw: "64:ff9b::0a00:1", want: true},
		{raw: "64:ff9b::808:808", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			ip := net.ParseIP(tt.raw)
			if ip == nil {
				t.Fatalf("failed to parse %q", tt.raw)
			}
			if got := isUnsafeProbeIP(ip); got != tt.want {
				t.Fatalf("isUnsafeProbeIP(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestProbeTransport_BlocksDNSResolvedLoopback(t *testing.T) {
	client := newProbeHTTPClient(false)
	req, err := http.NewRequest(http.MethodGet, "http://localhost:8080/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	if err == nil || !strings.Contains(err.Error(), "private or local") {
		t.Fatalf("expected DNS-resolved loopback rejection, got %v", err)
	}
}

func TestProbeTransport_BlocksPrivateRedirect(t *testing.T) {
	client := newProbeHTTPClient(false)
	req, err := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = client.CheckRedirect(req, []*http.Request{{}})
	if err == nil {
		t.Fatal("expected private redirect rejection")
	}
}

func TestProbe_InvalidTransport(t *testing.T) {
	svc := newProbeService()
	_, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.Transport("bogus"),
		URL:       "http://example.com",
	})
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidTransport {
		t.Fatalf("expected invalid_transport, got %v", apiErr)
	}
}

// mcpFakeSSE stubs a legacy-SSE MCP server. Two endpoints:
//
//	GET  /sse         → persistent text/event-stream. First event is `endpoint`
//	                    naming the POST url. Subsequent `message` events carry
//	                    JSON-RPC responses.
//	POST /messages/x  → 202. Response is delivered on the SSE stream.
//
// Requests are gated by a session_id query param to prove openSSE handed the
// endpoint URL through correctly (a common source of "hangs forever" in the
// wild — clients that ignore the endpoint event and POST to the SSE URL just
// get 405s or the wrong endpoint).
type mcpFakeSSE struct {
	tools  []wireTool
	caps   map[string]any
	server *httptest.Server

	// events pipes JSON-RPC responses from the POST handler into the GET stream.
	events chan string

	// sessionID discriminates the message endpoint the server names.
	sessionID string

	// initFail: when true, initialize returns a JSON-RPC error instead of
	// a result. Used to exercise the failure path over SSE.
	initFail bool
}

func newFakeSSE(t *testing.T, tools []wireTool, caps map[string]any) *mcpFakeSSE {
	t.Helper()
	f := &mcpFakeSSE{
		tools:     tools,
		caps:      caps,
		events:    make(chan string, 8),
		sessionID: "sess-abc-123",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", f.handleSSE)
	mux.HandleFunc("/messages/", f.handlePost)
	f.server = httptest.NewServer(mux)
	t.Cleanup(func() {
		f.server.Close()
		// Drain / close the events channel so any stray goroutine unblocks.
		select {
		case <-f.events:
		default:
		}
	})
	return f
}

// URL returns the SSE entry point clients hit first.
func (f *mcpFakeSSE) URL() string { return f.server.URL + "/sse" }

func (f *mcpFakeSSE) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	// First event: hand the client the message-post endpoint. Use a relative
	// path so we also exercise the resolver.
	fmt.Fprintf(w, "event: endpoint\ndata: /messages/x?session_id=%s\n\n", f.sessionID)
	if flusher != nil {
		flusher.Flush()
	}
	// Pump JSON-RPC responses queued by handlePost into the stream until the
	// client disconnects.
	for {
		select {
		case body, ok := <-f.events:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
			if flusher != nil {
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (f *mcpFakeSSE) handlePost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Query().Get("session_id") != f.sessionID {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Ack the POST first (spec: 202 fire-and-forget). Response arrives on SSE.
	w.WriteHeader(http.StatusAccepted)

	// Notifications get no response frame.
	if req.ID == nil {
		return
	}
	var body map[string]any
	switch req.Method {
	case "initialize":
		if f.initFail {
			body = map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32603, "message": "sse initialize failed"}}
		} else {
			capsJSON, _ := json.Marshal(f.caps)
			body = map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"protocolVersion": probeProtocolVersion,
					"capabilities":    json.RawMessage(capsJSON),
					"serverInfo":      map[string]string{"name": "fake-sse", "version": "0.1.0"},
				}}
		}
	case "tools/list":
		body = map[string]any{"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{"tools": f.tools}}
	default:
		body = map[string]any{"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]any{"code": -32601, "message": "Method not found"}}
	}
	raw, _ := json.Marshal(body)
	select {
	case f.events <- string(raw):
	case <-time.After(2 * time.Second):
		// If nobody's reading the stream, drop it. Test will observe the
		// timeout and fail loudly rather than deadlock.
	}
}

// TestProbe_HappyPath_LegacySSE end-to-ends the two-endpoint SSE dance: GET
// the SSE URL → parse endpoint event → POST initialize → read reply from the
// stream → POST tools/list → read reply. Pins the transport this codepath
// was added for (modelscope MCP catalog, LSC-140).
func TestProbe_HappyPath_LegacySSE(t *testing.T) {
	fake := newFakeSSE(t, []wireTool{
		{Name: "list_datasets", Description: "List datasets"},
		{Name: "get_dataset", Description: "Get a dataset"},
	}, map[string]any{"tools": map[string]any{}})

	svc := newProbeService()
	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportSSE,
		URL:       fake.URL(),
	})
	if apiErr != nil {
		t.Fatalf("unexpected apierr: %v", apiErr)
	}
	if !resp.OK {
		t.Fatalf("expected ok, got: %+v", resp)
	}
	if len(resp.Tools) != 2 || resp.Tools[0].Name != "list_datasets" {
		t.Fatalf("unexpected tools: %+v", resp.Tools)
	}
	if resp.ServerInfo == nil || resp.ServerInfo.Name != "fake-sse" {
		t.Fatalf("expected serverInfo from initialize, got %+v", resp.ServerInfo)
	}
}

// TestProbe_LegacySSE_InitializeError verifies a JSON-RPC error on initialize
// returns init_failed with the server's message threaded through, same as the
// streamable-http path.
func TestProbe_LegacySSE_InitializeError(t *testing.T) {
	fake := newFakeSSE(t, nil, map[string]any{"tools": map[string]any{}})
	fake.initFail = true

	svc := newProbeService()
	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportSSE,
		URL:       fake.URL(),
	})
	if apiErr != nil {
		t.Fatalf("unexpected apierr: %v", apiErr)
	}
	if resp.OK {
		t.Fatalf("expected ok=false, got: %+v", resp)
	}
	if resp.Error == nil || resp.Error.Code != ProbeErrInitFailed {
		t.Fatalf("expected init_failed, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "sse initialize failed") {
		t.Fatalf("expected server error message threaded through, got %q", resp.Error.Message)
	}
}

// TestProbe_LegacySSE_NoEndpointEvent covers the branch where the server
// opens the SSE stream but never emits an `endpoint` event before closing —
// the client must surface init_failed rather than hang until probeTimeout.
func TestProbe_LegacySSE_NoEndpointEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Send a comment then close — no endpoint event.
		fmt.Fprint(w, ": ping\n\n")
	}))
	t.Cleanup(srv.Close)

	svc := newProbeService()
	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportSSE,
		URL:       srv.URL,
	})
	if apiErr != nil {
		t.Fatalf("unexpected apierr: %v", apiErr)
	}
	if resp.OK {
		t.Fatalf("expected ok=false, got: %+v", resp)
	}
	if resp.Error == nil || resp.Error.Code != ProbeErrInitFailed {
		t.Fatalf("expected init_failed, got %+v", resp.Error)
	}
}

func TestProbe_LegacySSE_RejectsCrossOriginEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "event: endpoint\ndata: https://attacker.example/messages\n\n")
	}))
	t.Cleanup(srv.Close)

	svc := newProbeService()
	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportSSE,
		URL:       srv.URL,
	})
	if apiErr != nil {
		t.Fatalf("unexpected apierr: %v", apiErr)
	}
	if resp.OK {
		t.Fatalf("expected ok=false, got %+v", resp)
	}
	if resp.Error == nil || resp.Error.Code != ProbeErrInitFailed {
		t.Fatalf("expected init_failed, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "original probe origin") {
		t.Fatalf("expected origin validation message, got %q", resp.Error.Message)
	}
}

func TestReadSSEEventRejectsOversizedPayload(t *testing.T) {
	payload := strings.Repeat("x", probeMaxRespBytes+1)
	reader := bufio.NewReader(strings.NewReader("event: message\ndata: " + payload + "\n\n"))

	_, err := readSSEEvent(reader, probeMaxRespBytes)
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("expected oversized payload error, got %v", err)
	}
}
