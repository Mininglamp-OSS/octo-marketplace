package fleet

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCapabilityTimeoutDoesNotChangeLegacyClient(t *testing.T) {
	client := New("https://fleet.example.test")
	var deadlines []time.Duration
	client.http.Transport = capabilityRoundTripper(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("request has no deadline")
		}
		deadlines = append(deadlines, time.Until(deadline))
		body := expertEnvelopeJSON
		if r.URL.Path == "/api/skills" {
			body = `{"id":"skill-1"}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if _, err := client.InstallCapability(context.Background(), "token", "space", "ws", "key", capabilityRequest()); err != nil {
		t.Fatal(err)
	}
	if deadlines[0] < 119*time.Second || deadlines[0] > 2*time.Minute {
		t.Fatalf("capability timeout=%v, want 2 minutes", deadlines[0])
	}
	if client.http.Timeout != 30*time.Second {
		t.Fatal("shared legacy timeout changed")
	}
	if _, err := client.CreateSkill(context.Background(), "token", "space", "ws", SkillSpec{}); err != nil {
		t.Fatal(err)
	}
	if len(deadlines) != 2 || deadlines[1] < 29*time.Second || deadlines[1] > 30*time.Second {
		t.Fatalf("legacy deadlines=%v", deadlines)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.InstallCapability(ctx, "token", "space", "ws", "key", capabilityRequest()); err != nil {
		t.Fatal(err)
	}
	if len(deadlines) != 3 || deadlines[2] < 9*time.Second || deadlines[2] > 10*time.Second {
		t.Fatalf("shorter caller deadline ignored: %v", deadlines)
	}
}

func TestCapabilityTimeoutHonorsCallerCancellation(t *testing.T) {
	client := New("https://fleet.example.test")
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.http.Transport = capabilityRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	if _, err := client.InstallCapability(ctx, "token", "space", "ws", "key", capabilityRequest()); err == nil || calls != 1 {
		t.Fatalf("cancellation retried or ignored: err=%v calls=%d", err, calls)
	}
}
