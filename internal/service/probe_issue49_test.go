package service

import (
	"context"
	"errors"
	"net"
	"net/http"
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
