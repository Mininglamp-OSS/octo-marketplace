package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

var (
	cgnatPrefix = mustParseCIDR("100.64.0.0/10")
	nat64Prefix = mustParseCIDR("64:ff9b::/96")
)

// Probe subsystem — runs an MCP initialize + tools/list handshake against a
// remote server so the create wizard can auto-populate the tool list. See
// docs/api/mcp-v1.md §4.7 for the wire contract.
//
// Only remote transports (streamable-http / sse) are supported. stdio probing
// is rejected up front: the marketplace host must not spawn arbitrary user
// commands (CLAUDE.md "Never execute Skill code or launch MCP servers inside
// Marketplace") — the desktop client owns that path.

const (
	// probeTimeout caps the entire handshake (initialize + notif +
	// tools/list). A remote MCP that can't answer inside this window is
	// treated as unreachable — the frontend renders it as a `timeout`.
	probeTimeout = 15 * time.Second
	// probeMaxRespBytes bounds each individual response body so a hostile or
	// broken server cannot exhaust marketplace memory.
	probeMaxRespBytes = 4 << 20 // 4 MiB
	// MCP protocol version we advertise (spec 2024-11-05).
	probeProtocolVersion = "2024-11-05"
	// Header used by the streamable-http transport to carry session ids the
	// server assigns after initialize.
	mcpSessionHeader = "Mcp-Session-Id"
)

// ProbeErrorCode enumerates the in-body error codes the frontend switches on.
// Keep in sync with packages/dmworkmcp/src/types/mcp.ts McpProbeErrorCode.
type ProbeErrorCode string

const (
	ProbeErrCommandNotFound   ProbeErrorCode = "command_not_found"
	ProbeErrTimeout           ProbeErrorCode = "timeout"
	ProbeErrInitFailed        ProbeErrorCode = "init_failed"
	ProbeErrNoToolsCapability ProbeErrorCode = "no_tools_capability"
)

// ProbeRequest mirrors the frontend McpProbeRequest shape. All connection
// fields ride the request body; the endpoint stamps identity from the token.
type ProbeRequest struct {
	Transport model.Transport   `json:"transport"`
	URL       string            `json:"url"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	Headers   map[string]string `json:"headers"`
}

// ProbeError is the in-body operational-failure shape (HTTP 200 responses).
type ProbeError struct {
	Code    ProbeErrorCode `json:"code"`
	Message string         `json:"message"`
}

// ProbeServerInfo carries what MCP servers advertise about themselves.
type ProbeServerInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// ProbeResponse mirrors the frontend McpProbeResult. The endpoint returns
// HTTP 200 in both success and operational-failure cases; only auth /
// malformed-body failures use the standard apierr envelope (§4.7).
type ProbeResponse struct {
	OK         bool             `json:"is_ok"`
	Tools      []model.Tool     `json:"tools"`
	ServerInfo *ProbeServerInfo `json:"server_info,omitempty"`
	Error      *ProbeError      `json:"error,omitempty"`
}

// Probe runs the full initialize + tools/list handshake and returns the
// tool catalog, or a structured in-body error on failure.
func (s *Service) Probe(ctx context.Context, req ProbeRequest) (ProbeResponse, *apierr.Error) {
	if !model.ValidTransport(req.Transport) {
		return ProbeResponse{}, apierr.InvalidTransport()
	}
	if req.Transport == model.TransportStdio {
		return ProbeResponse{}, apierr.ProbeUnsupported(
			"stdio probing must run in the local runtime; the marketplace server does not spawn user commands")
	}
	endpoint, apiErr := validateProbeURL(req.URL, s.probeAllowPrivate)
	if apiErr != nil {
		return ProbeResponse{}, apiErr
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	client := newProbeHTTPClient(s.probeAllowPrivate)
	sess := &probeSession{
		client:    client,
		url:       endpoint,
		headers:   sanitizedHeaders(req.Headers),
		transport: req.Transport,
	}

	// Legacy SSE transport is a two-endpoint dance: hold a persistent GET on
	// the SSE URL, wait for the server's first `endpoint` event that names
	// the JSON-RPC POST target, then run the handshake through that pair.
	// Streamable-http skips this — it's a single URL that answers POSTs.
	if req.Transport == model.TransportSSE {
		if err := sess.openSSE(ctx); err != nil {
			return probeFail(err), nil
		}
		defer sess.closeSSE()
	}

	initRes, err := sess.initialize(ctx)
	if err != nil {
		return probeFail(err), nil
	}
	if !hasToolsCapability(initRes) {
		return ProbeResponse{
			OK:         false,
			Tools:      []model.Tool{},
			ServerInfo: initRes.serverInfo(),
			Error: &ProbeError{
				Code:    ProbeErrNoToolsCapability,
				Message: "server does not advertise a tools capability",
			},
		}, nil
	}
	// Notification — servers respond 200/202/204 or close the stream. Any
	// wire error here is non-fatal; some servers don't bother replying.
	_ = sess.notifyInitialized(ctx)

	tools, err := sess.listTools(ctx)
	if err != nil {
		return probeFail(err), nil
	}
	return ProbeResponse{
		OK:         true,
		Tools:      tools,
		ServerInfo: initRes.serverInfo(),
	}, nil
}

// validateProbeURL rejects unsafe schemes, credentials, and literal private
// addresses. Hostnames are resolved and checked by the transport immediately
// before dialing so DNS rebinding cannot bypass the policy.
func validateProbeURL(raw string, allowPrivate bool) (string, *apierr.Error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", apierr.InvalidRequest("url is required for remote transports",
			apierr.Detail{Field: "url", Reason: "required"})
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", apierr.InvalidRequest("url is not a valid absolute URL",
			apierr.Detail{Field: "url", Reason: "malformed"})
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", apierr.InvalidRequest("url must be http or https",
			apierr.Detail{Field: "url", Reason: "scheme"})
	}
	if u.User != nil {
		return "", apierr.InvalidRequest("url must not include credentials",
			apierr.Detail{Field: "url", Reason: "credentials"})
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !allowPrivate && isUnsafeProbeIP(ip) {
		return "", apierr.InvalidRequest("url targets a private or local network address",
			apierr.Detail{Field: "url", Reason: "private_address"})
	}
	return u.String(), nil
}

func newProbeHTTPClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: probeTimeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid probe address: %w", err)
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("resolve probe host: %w", err)
			}
			for _, ip := range ips {
				if !allowPrivate && isUnsafeProbeIP(ip) {
					continue
				}
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				err = dialErr
			}
			if !allowPrivate {
				return nil, errors.New("probe target resolves only to private or local network addresses")
			}
			return nil, fmt.Errorf("connect to probe target: %w", err)
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   probeTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			_, apiErr := validateProbeURL(req.URL.String(), allowPrivate)
			if apiErr != nil {
				return errors.New("redirect target is not permitted")
			}
			return nil
		},
	}
}

func isUnsafeProbeIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		return isUnsafeProbeIPv4(ip4)
	}
	if nat64Prefix.Contains(ip) {
		if embedded, ok := embeddedNAT64IPv4(ip); ok {
			return isUnsafeProbeIPv4(embedded)
		}
	}
	return ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

func isUnsafeProbeIPv4(ip net.IP) bool {
	return ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		cgnatPrefix.Contains(ip)
}

func embeddedNAT64IPv4(ip net.IP) (net.IP, bool) {
	ip = ip.To16()
	if ip == nil || !nat64Prefix.Contains(ip) {
		return nil, false
	}
	return net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4(), true
}

func mustParseCIDR(raw string) *net.IPNet {
	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		panic("invalid cidr: " + raw)
	}
	return network
}

// sanitizedHeaders drops the reserved secret placeholder (frontend sends it
// literally on token-like keys when the user cleared the field). We MUST NOT
// forward the ASCII sentinel as the actual Authorization value.
func sanitizedHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if v == model.SecretPlaceholderSentinel {
			continue
		}
		out[k] = v
	}
	return out
}

func hasToolsCapability(res *initializeResult) bool {
	if res == nil {
		return false
	}
	if len(res.Capabilities) == 0 {
		return false
	}
	var caps map[string]json.RawMessage
	if err := json.Unmarshal(res.Capabilities, &caps); err != nil {
		return false
	}
	_, ok := caps["tools"]
	return ok
}

// probeFail translates a transport-layer error into the in-body ProbeResponse
// shape. Deadline exceeded → timeout; anything else → init_failed (with the
// concrete message so the UI can surface a hint).
func probeFail(err error) ProbeResponse {
	if err == nil {
		return ProbeResponse{OK: false, Tools: []model.Tool{}, Error: &ProbeError{
			Code:    ProbeErrInitFailed,
			Message: "unknown probe failure",
		}}
	}
	if errors.Is(err, context.DeadlineExceeded) || isNetTimeout(err) {
		return ProbeResponse{OK: false, Tools: []model.Tool{}, Error: &ProbeError{
			Code:    ProbeErrTimeout,
			Message: "probe timed out",
		}}
	}
	return ProbeResponse{OK: false, Tools: []model.Tool{}, Error: &ProbeError{
		Code:    ProbeErrInitFailed,
		Message: truncateErr(err.Error()),
	}}
}

func isNetTimeout(err error) bool {
	type timeoutish interface{ Timeout() bool }
	var t timeoutish
	if errors.As(err, &t) {
		return t.Timeout()
	}
	return false
}

func truncateErr(s string) string {
	const cap = 200
	if len(s) <= cap {
		return s
	}
	return s[:cap] + "…"
}

// ─── JSON-RPC / MCP wire helpers ────────────────────────────────────────────

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    map[string]any  `json:"capabilities"`
	ClientInfo      clientInfoBlock `json:"clientInfo"`
}

type clientInfoBlock struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

func (r *initializeResult) serverInfo() *ProbeServerInfo {
	if r == nil {
		return nil
	}
	if r.ServerInfo.Name == "" && r.ServerInfo.Version == "" {
		return nil
	}
	return &ProbeServerInfo{Name: r.ServerInfo.Name, Version: r.ServerInfo.Version}
}

type toolsListResult struct {
	Tools []wireTool `json:"tools"`
}

type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// probeSession is a stateful http client for one probe run. It carries the
// endpoint, user headers, and the streamable-http session id (if any). For
// legacy SSE it also holds the persistent GET stream and the message-post
// URL the server hands back in its first `endpoint` event.
type probeSession struct {
	client    *http.Client
	url       string
	headers   map[string]string
	transport model.Transport
	sessionID string

	// SSE-only. sseStream is the persistent GET body kept open for the whole
	// probe. sseReader wraps it for line-oriented event parsing. sseEndpoint
	// is the URL that JSON-RPC messages get POSTed to — advertised by the
	// server's first `endpoint` event on the stream.
	sseStream   io.ReadCloser
	sseReader   *bufio.Reader
	sseEndpoint string
}

// openSSE opens the SSE stream for legacy-SSE probes and captures the POST
// endpoint the server advertises in its first `endpoint` event. Called before
// initialize(); Probe() runs closeSSE() as a defer partner.
//
// Wire shape (MCP legacy SSE):
//
//	GET  /sse            → HTTP 200, Content-Type: text/event-stream
//	                        event: endpoint
//	                        data: /messages/xxx?session_id=yyy
//
//	POST /messages/... {jsonrpc}  → 202 (fire), response arrives on the SSE stream
//	                        event: message
//	                        data: {"jsonrpc":"2.0","id":1,"result":{...}}
//
// The endpoint data may be relative (spec-required); resolve against the SSE
// URL. Absolute URLs work too and are common in the wild.
func (s *probeSession) openSSE(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return fmt.Errorf("probe target returned http %d", resp.StatusCode)
	}
	s.sseStream = resp.Body
	s.sseReader = bufio.NewReader(io.LimitReader(resp.Body, probeMaxRespBytes))

	// Wait for the first `endpoint` event. Any comment lines / retry lines /
	// unrelated events (server may push a hello ping first) are skipped.
	// A ctx-cancel goroutine closes the body so we don't block forever on a
	// silent stream.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.sseStream.Close()
		case <-done:
		}
	}()
	defer close(done)

	ev, err := readSSEEvent(s.sseReader, probeMaxRespBytes)
	for err == nil {
		if ev.event == "endpoint" && ev.data != "" {
			resolved, resolveErr := resolveEndpoint(s.url, ev.data)
			if resolveErr != nil {
				return fmt.Errorf("endpoint event: %w", resolveErr)
			}
			s.sseEndpoint = resolved
			return nil
		}
		ev, err = readSSEEvent(s.sseReader, probeMaxRespBytes)
	}
	if err == io.EOF {
		return errors.New("sse stream closed before endpoint event")
	}
	return fmt.Errorf("sse endpoint discovery: %w", err)
}

// closeSSE releases the persistent GET stream. Safe to call even if openSSE
// never ran; used as a defer partner in Probe().
func (s *probeSession) closeSSE() {
	if s.sseStream != nil {
		_ = s.sseStream.Close()
	}
}

// resolveEndpoint turns whatever the server puts in the `endpoint` event's
// data (relative path, absolute path, or fully qualified URL) into an
// absolute URL rooted at the SSE URL's authority. Fails on malformed input
// so the caller can surface an init_failed with a clear message.
func resolveEndpoint(sseURL, endpointData string) (string, error) {
	endpointData = strings.TrimSpace(endpointData)
	if endpointData == "" {
		return "", errors.New("empty endpoint data")
	}
	base, err := url.Parse(sseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	ref, err := url.Parse(endpointData)
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	resolved := base.ResolveReference(ref)
	if !sameEndpointOrigin(base, resolved) {
		return "", errors.New("endpoint must remain on the original probe origin")
	}
	return resolved.String(), nil
}

// sseEvent is one parsed Server-Sent Event: the event name (default "message"
// per spec) and the concatenated data payload.
type sseEvent struct {
	event string
	data  string
}

// readSSEEvent consumes lines from the SSE stream until a blank line signals
// the end of an event, then returns the accumulated event/data pair. Comment
// lines (starting with ':') and unknown fields are ignored per spec.
func readSSEEvent(r *bufio.Reader, maxDataBytes int) (sseEvent, error) {
	var ev sseEvent
	ev.event = "message"
	var dataBuf strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return sseEvent{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if dataBuf.Len() == 0 && ev.event == "message" && err == io.EOF {
				return sseEvent{}, io.EOF
			}
			ev.data = dataBuf.String()
			return ev, nil
		}
		if strings.HasPrefix(line, ":") {
			continue // comment/keep-alive
		}
		field, value := splitSSEField(line)
		switch field {
		case "event":
			ev.event = value
		case "data":
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			if dataBuf.Len()+len(value) > maxDataBytes {
				return sseEvent{}, fmt.Errorf("sse event exceeded %d bytes", maxDataBytes)
			}
			dataBuf.WriteString(value)
		}
		if err == io.EOF {
			if dataBuf.Len() > 0 {
				ev.data = dataBuf.String()
				return ev, nil
			}
			return sseEvent{}, io.EOF
		}
	}
}

func sameEndpointOrigin(base, resolved *url.URL) bool {
	return strings.EqualFold(base.Scheme, resolved.Scheme) &&
		strings.EqualFold(canonicalHostPort(base), canonicalHostPort(resolved))
}

func canonicalHostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return strings.ToLower(u.Hostname()) + ":" + port
}

// splitSSEField splits a field line into (name, value) per the SSE spec: the
// separator is the first ':', and one leading space in the value is dropped.
func splitSSEField(line string) (string, string) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return line, ""
	}
	name := line[:idx]
	value := line[idx+1:]
	value = strings.TrimPrefix(value, " ")
	return name, value
}

func (s *probeSession) initialize(ctx context.Context) (*initializeResult, error) {
	resp, err := s.rpc(ctx, 1, "initialize", initializeParams{
		ProtocolVersion: probeProtocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo:      clientInfoBlock{Name: "octo-marketplace-probe", Version: "1"},
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Error != nil {
		return nil, rpcErrorFrom(resp)
	}
	var res initializeResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, fmt.Errorf("initialize response: %w", err)
	}
	return &res, nil
}

func (s *probeSession) notifyInitialized(ctx context.Context) error {
	_, err := s.rpc(ctx, nil, "notifications/initialized", struct{}{})
	return err
}

func (s *probeSession) listTools(ctx context.Context) ([]model.Tool, error) {
	resp, err := s.rpc(ctx, 2, "tools/list", struct{}{})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Error != nil {
		return nil, rpcErrorFrom(resp)
	}
	var res toolsListResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, fmt.Errorf("tools/list response: %w", err)
	}
	out := make([]model.Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		out = append(out, model.Tool{Name: t.Name, Description: t.Description})
	}
	return out, nil
}

func rpcErrorFrom(resp *jsonRPCResponse) error {
	if resp == nil {
		return errors.New("no response")
	}
	if resp.Error == nil {
		return errors.New("empty response")
	}
	return fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
}

// rpc sends a single JSON-RPC message and returns the (first matching)
// response. Handles both content types the streamable-http transport uses:
// application/json (a single response body) and text/event-stream (SSE-framed
// responses, one per `data:` event). A notification (id == nil) does not
// expect a response — the server may 200/202/204 or close, all fine.
//
// For legacy SSE transport, hand off to rpcSSE which uses the pre-opened
// stream (see openSSE) and the discovered message-post endpoint.
func (s *probeSession) rpc(ctx context.Context, id any, method string, params any) (*jsonRPCResponse, error) {
	if s.transport == model.TransportSSE {
		return s.rpcSSE(ctx, id, method, params)
	}
	body, err := json.Marshal(jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	if s.sessionID != "" {
		req.Header.Set(mcpSessionHeader, s.sessionID)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Server may assign a session id after initialize; carry it forward.
	if sid := resp.Header.Get(mcpSessionHeader); sid != "" && s.sessionID == "" {
		s.sessionID = sid
	}

	// Notifications don't expect a response body.
	if id == nil {
		return nil, nil
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil, fmt.Errorf("server returned %d with no body for request %s", resp.StatusCode, method)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("probe target returned http %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, probeMaxRespBytes)
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/event-stream") {
		return readSSEResponse(limited, id)
	}
	var out jsonRPCResponse
	if err := json.NewDecoder(limited).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// rpcSSE runs one JSON-RPC round-trip over the legacy SSE transport:
//  1. POST the marshaled request body to s.sseEndpoint (server acks with
//     HTTP 200/202/204 — the response payload is NOT on this connection).
//  2. If a response is expected (id != nil), consume events on the persistent
//     SSE stream (opened by openSSE) until a `message` event carries a
//     JSON-RPC response whose id matches. Non-matching messages (server
//     notifications, other pending replies) are ignored.
//
// Requires openSSE() to have populated s.sseEndpoint and s.sseReader.
func (s *probeSession) rpcSSE(ctx context.Context, id any, method string, params any) (*jsonRPCResponse, error) {
	if s.sseEndpoint == "" || s.sseReader == nil {
		return nil, errors.New("sse session not established")
	}
	body, err := json.Marshal(jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.sseEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	// Legacy SSE uses the POST purely for delivery; drain and close.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("probe target returned http %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()

	if id == nil {
		return nil, nil
	}
	// Read events until one matches the request id. A context deadline cancel
	// closes the stream via the goroutine set up in openSSE — this Read will
	// then error out, which we surface as a timeout above.
	for {
		ev, err := readSSEEvent(s.sseReader, probeMaxRespBytes)
		if err != nil {
			if err == io.EOF {
				return nil, errors.New("sse stream closed without a matching response")
			}
			return nil, fmt.Errorf("read sse: %w", err)
		}
		if ev.event != "message" && ev.event != "" {
			// Some servers emit "endpoint"/"ping"/"error" events. Skip.
			continue
		}
		if ev.data == "" {
			continue
		}
		var msg jsonRPCResponse
		if err := json.Unmarshal([]byte(ev.data), &msg); err != nil {
			continue // Not a JSON-RPC frame; skip.
		}
		if msg.ID == nil {
			continue // Server-initiated notification.
		}
		if idsEqual(msg.ID, id) {
			return &msg, nil
		}
	}
}

// readSSEResponse parses a JSON-RPC response out of an SSE stream. It
// accumulates `data:` lines within a single event, dispatches on the blank
// line, and returns the first message whose id matches `wantID` (server
// notifications sent alongside the response are skipped).
func readSSEResponse(r io.Reader, wantID any) (*jsonRPCResponse, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	var buf strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if buf.Len() == 0 {
				continue
			}
			var msg jsonRPCResponse
			if err := json.Unmarshal([]byte(buf.String()), &msg); err == nil {
				if msg.ID != nil && idsEqual(msg.ID, wantID) {
					return &msg, nil
				}
			}
			buf.Reset()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			piece := strings.TrimPrefix(line, "data:")
			// SSE spec: strip exactly one leading space if present.
			piece = strings.TrimPrefix(piece, " ")
			buf.WriteString(piece)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read sse: %w", err)
	}
	// Handle streams that end without a trailing blank line.
	if buf.Len() > 0 {
		var msg jsonRPCResponse
		if err := json.Unmarshal([]byte(buf.String()), &msg); err == nil {
			if msg.ID != nil && idsEqual(msg.ID, wantID) {
				return &msg, nil
			}
		}
	}
	return nil, errors.New("sse stream closed without a matching response")
}

// idsEqual compares a wire JSON-RPC id (RawMessage) with the numeric id we
// sent. MCP servers echo the id as a number in nearly all cases.
func idsEqual(raw json.RawMessage, want any) bool {
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		switch w := want.(type) {
		case int:
			return int(n) == w
		case int64:
			return int64(n) == w
		case float64:
			return n == w
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return fmt.Sprintf("%v", want) == s
	}
	return false
}
