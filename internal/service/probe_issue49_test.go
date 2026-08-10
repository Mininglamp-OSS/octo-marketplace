package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// Tests for issue #49: the Probe dialer must reject a resolution set that
// contains ANY unsafe address (whole-request rejection, not cherry-picking the
// safe IP), and must never leak resolved addresses / DNS detail to the client.

// fakeResolver injects a fixed DNS answer so mixed / IPv6 / failure cases are
// exercisable without real DNS.
type fakeResolver struct {
	ips    []net.IP
	err    error
	called bool
}

func (f *fakeResolver) LookupIP(_ context.Context, _, _ string) ([]net.IP, error) {
	f.called = true
	return f.ips, f.err
}

func ips(t *testing.T, raws ...string) []net.IP {
	t.Helper()
	out := make([]net.IP, 0, len(raws))
	for _, r := range raws {
		ip := net.ParseIP(r)
		if ip == nil {
			t.Fatalf("bad test IP %q", r)
		}
		out = append(out, ip)
	}
	return out
}

// probeServiceWithResolver builds a Service that resolves through r and keeps
// the default allowPrivate=false SSRF policy.
func probeServiceWithResolver(r ipResolver) *Service {
	return &Service{store: nil, now: time.Now, resolver: r}
}

// A hostname URL passes validateProbeURL (not a literal IP), so these cases
// reach the resolve-time gate in the dialer.
const probeHostURL = "http://probe-target.test/mcp"

func TestProbe_RejectsMixedPublicPrivateResolution(t *testing.T) {
	res := &fakeResolver{ips: ips(t, "93.184.216.34", "10.0.0.7")}
	svc := probeServiceWithResolver(res)

	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       probeHostURL,
	})
	if apiErr != nil {
		t.Fatalf("unexpected apiErr: %v", apiErr)
	}
	if !res.called {
		t.Fatal("resolver was not consulted")
	}
	if resp.OK {
		t.Fatal("mixed public/private resolution must be rejected")
	}
	// Generic message only — no resolved address may leak.
	if resp.Error == nil || resp.Error.Message != "probe target is not reachable" {
		t.Fatalf("expected generic message, got %+v", resp.Error)
	}
	if strings.Contains(resp.Error.Message, "10.0.0") {
		t.Fatalf("resolved private IP leaked to client: %q", resp.Error.Message)
	}
}

func TestProbe_RejectsAllPrivateResolution(t *testing.T) {
	svc := probeServiceWithResolver(&fakeResolver{ips: ips(t, "10.1.2.3", "192.168.0.9")})

	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       probeHostURL,
	})
	if apiErr != nil {
		t.Fatalf("unexpected apiErr: %v", apiErr)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("all-private resolution must be rejected, got %+v", resp)
	}
}

func TestProbe_RejectsIPv6LoopbackResolution(t *testing.T) {
	svc := probeServiceWithResolver(&fakeResolver{ips: ips(t, "::1")})

	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportSSE,
		URL:       probeHostURL,
	})
	if apiErr != nil {
		t.Fatalf("unexpected apiErr: %v", apiErr)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("IPv6 loopback resolution must be rejected, got %+v", resp)
	}
}

func TestProbe_RejectsLinkLocalMetadataResolution(t *testing.T) {
	svc := probeServiceWithResolver(&fakeResolver{ips: ips(t, "169.254.169.254")})

	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       probeHostURL,
	})
	if apiErr != nil {
		t.Fatalf("unexpected apiErr: %v", apiErr)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("cloud-metadata resolution must be rejected, got %+v", resp)
	}
}

func TestProbe_RejectsDNSLookupFailure(t *testing.T) {
	svc := probeServiceWithResolver(&fakeResolver{err: &net.DNSError{Err: "no such host", Name: "probe-target.test"}})

	resp, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       probeHostURL,
	})
	if apiErr != nil {
		t.Fatalf("unexpected apiErr: %v", apiErr)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("DNS failure must be reported as a failed probe, got %+v", resp)
	}
	// The DNS error string (host name / cause) must not reach the client.
	if strings.Contains(resp.Error.Message, "no such host") ||
		strings.Contains(resp.Error.Message, "probe-target.test") {
		t.Fatalf("DNS detail leaked to client: %q", resp.Error.Message)
	}
}

// allowPrivate=true is the trusted self-hosted escape hatch: a private
// resolution must NOT be rejected by the SSRF gate. We dial a closed loopback
// port so the attempt fails fast with an ordinary dial error (connection
// refused) rather than the blocked-target policy error — proving the gate was
// skipped without waiting on a black-hole timeout.
func TestProbe_AllowPrivateSkipsResolutionGate(t *testing.T) {
	res := &fakeResolver{ips: ips(t, "127.0.0.1")}
	client := newProbeHTTPClient(true, res)

	req, err := http.NewRequest(http.MethodGet, "http://probe-target.test:1/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("expected a dial failure to the closed port")
	}
	if errors.Is(err, errProbeTargetBlocked) {
		t.Fatalf("allow-private must skip the SSRF gate, got policy block: %v", err)
	}
	if !res.called {
		t.Fatal("resolver was not consulted under allow-private")
	}
}

// IPv6 loopback as a URL literal is rejected up front by validateProbeURL,
// before any resolution — complements the resolve-time IPv6 case above.
func TestProbe_BlocksIPv6LoopbackLiteral(t *testing.T) {
	svc := &Service{store: nil, now: time.Now} // allowPrivate defaults false
	_, apiErr := svc.Probe(context.Background(), ProbeRequest{
		Transport: model.TransportStreamableHTTP,
		URL:       "http://[::1]:8080/mcp",
	})
	if apiErr == nil || apiErr.Code != apierr.CodeInvalidRequest {
		t.Fatalf("expected IPv6 loopback literal rejection, got %v", apiErr)
	}
}

// ── P1-1: probe error redaction ─────────────────────────────────────────────

// A socket-level failure (*net.OpError, incl. one wrapped in *url.Error) must
// collapse to the opaque message: its text embeds the service's own local
// address, which must never reach the client.
func TestProbeFailRedactsNetOpError(t *testing.T) {
	opErr := &net.OpError{
		Op:     "read",
		Net:    "tcp",
		Source: &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 43450}, // pod-local
		Addr:   &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 80},
		Err:    errors.New("connection reset by peer"),
	}
	wrapped := &url.Error{Op: "Get", URL: "http://probe-target.test/mcp", Err: opErr}

	resp := probeFail(wrapped)
	if resp.OK || resp.Error == nil {
		t.Fatalf("expected failure, got %+v", resp)
	}
	if resp.Error.Message != "probe target is not reachable" {
		t.Fatalf("net.OpError not redacted: %q", resp.Error.Message)
	}
	for _, leak := range []string{"10.1.2.3", "43450", "connection reset"} {
		if strings.Contains(resp.Error.Message, leak) {
			t.Fatalf("message leaked %q: %q", leak, resp.Error.Message)
		}
	}
}

// An application-level cause (non-2xx status, JSON-RPC error, malformed payload)
// keeps its concrete message as a UI hint — it carries no network detail.
func TestProbeFailKeepsApplicationError(t *testing.T) {
	resp := probeFail(fmt.Errorf("probe target returned http 405"))
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "405") {
		t.Fatalf("application error hint lost: %+v", resp.Error)
	}
}

// ── P2-8: embedded-IPv4 (translated / NAT64) unsafe detection ────────────────

func TestIsUnsafeProbeIP_EmbeddedIPv4(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"::ffff:127.0.0.1", true},      // IPv4-mapped loopback (To4 handles)
		{"::ffff:0:7f00:1", true},       // IPv4-translated 127.0.0.1
		{"::ffff:0:a00:5", true},        // IPv4-translated 10.0.0.5
		{"64:ff9b::7f00:1", true},       // NAT64 well-known 127.0.0.1
		{"64:ff9b:1:2:3:4:a00:5", true}, // RFC 8215 local-use 10.0.0.5
		{"::ffff:0:808:808", false},     // IPv4-translated 8.8.8.8 (public) → allowed
		{"2001:db8::a00:5", false},      // global IPv6 whose tail looks like 10.0.0.5 → NOT over-blocked
	}
	for _, c := range cases {
		ip := net.ParseIP(c.raw)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.raw)
		}
		if got := isUnsafeProbeIP(ip); got != c.want {
			t.Fatalf("isUnsafeProbeIP(%s) = %v, want %v", c.raw, got, c.want)
		}
	}
}
