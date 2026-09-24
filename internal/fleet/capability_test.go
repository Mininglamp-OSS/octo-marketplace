package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCapabilityResultValidatesTeamAndSkillBindings(t *testing.T) {
	in := capabilityRequest()
	in.Definition.Skills = []CapabilitySkill{{Name: "review"}, {Name: "deploy"}}
	in.Definition.Experts[0].SkillNames = []string{"review", "deploy"}
	in.Definition.ExpertTeam = &CapabilityExpertTeam{Name: "team", Members: []CapabilityTeamMember{{ExpertName: "Custom name", IsLeader: true}}}
	valid := `{"type":"expert_team","expert_team_id":"team-1","leader_expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","skill_ids":["skill-1","skill-2"],"is_leader":true}],"skills":[{"skill_id":"skill-1","name":"review"},{"skill_id":"skill-2","name":"deploy"}]}`
	for _, tc := range []struct {
		name   string
		mutate func(*CapabilityInstallResult)
		want   bool
	}{
		{name: "valid", want: true},
		{name: "wrong_leader", mutate: func(r *CapabilityInstallResult) { r.LeaderExpertID = "other" }},
		{name: "missing_ref", mutate: func(r *CapabilityInstallResult) { r.Experts[0].SkillIDs = nil }},
		{name: "wrong_ref", mutate: func(r *CapabilityInstallResult) { r.Experts[0].SkillIDs[0] = "other" }},
		{name: "duplicate_ref", mutate: func(r *CapabilityInstallResult) { r.Experts[0].SkillIDs[1] = "skill-1" }},
		{name: "duplicate_skill_id", mutate: func(r *CapabilityInstallResult) { r.Skills[1].SkillID = "skill-1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out CapabilityInstallResult
			if err := json.Unmarshal([]byte(valid), &out); err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(&out)
			}
			if got := validCapabilityResult(in.Definition, out); got != tc.want {
				t.Fatalf("valid=%v want=%v", got, tc.want)
			}
		})
	}
}

func capabilityRequest() CapabilityInstallRequest {
	return CapabilityInstallRequest{Definition: CapabilityDefinition{SchemaVersion: "1.0", Name: "marketplace:p1", Experts: []CapabilityExpert{{Name: "Custom name", Instructions: "review", CustomEnv: map[string]string{"OCTOBUDDY_PROVIDER_ID": "provider"}}}}, Bindings: CapabilityBindings{Experts: []CapabilityExpertBinding{{ExpertName: "Custom name", RuntimeID: "runtime"}}}}
}

const expertResultJSON = `{"type":"expert","expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","skill_ids":[]}],"skills":[]}`
const expertEnvelopeJSON = `{"data":` + expertResultJSON + `}`

func TestCapabilityInstallForwardsIdentityAndReplay(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/capabilities/install" {
			t.Errorf("unexpected endpoint %s %s", r.Method, r.URL.Path)
		}
		for header, want := range map[string]string{"Token": "user-token", "X-Workspace-ID": "workspace", "X-Space-ID": "space", "Idempotency-Key": "operation-key", "Content-Type": "application/json"} {
			if r.Header.Get(header) != want {
				t.Errorf("header %s was not forwarded", header)
			}
		}
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		var payload struct {
			Definition struct {
				Experts []struct {
					CustomEnv map[string]string `json:"custom_env"`
				} `json:"experts"`
			} `json:"definition"`
			Bindings struct {
				Experts []map[string]json.RawMessage `json:"experts"`
			} `json:"bindings"`
		}
		if json.Unmarshal(body, &payload) != nil || len(payload.Definition.Experts) != 1 || payload.Definition.Experts[0].CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider" || len(payload.Bindings.Experts) != 1 {
			t.Error("environment must be sent in definition.experts")
		} else if _, exists := payload.Bindings.Experts[0]["custom_env"]; exists {
			t.Error("runtime binding must not contain custom_env")
		}
		w.Header().Set("Idempotency-Replayed", "true")
		io.WriteString(w, expertEnvelopeJSON)
	}))
	defer server.Close()
	client := New(server.URL)
	for range 2 {
		out, err := client.InstallCapability(context.Background(), "user-token", "space", "workspace", "operation-key", capabilityRequest())
		if err != nil || out.ExpertID != "expert-1" || !out.Replayed || !out.ReplayKnown {
			t.Fatalf("out=%+v err=%v", out, err)
		}
	}
	if bodies[0] != bodies[1] || !strings.Contains(bodies[0], `"custom_env":{"OCTOBUDDY_PROVIDER_ID":"provider"}`) {
		t.Fatal("request is not stable or environment binding was lost")
	}
}

func TestCapabilityInstallReplayMetadataMustBeExplicit(t *testing.T) {
	for _, tc := range []struct {
		name            string
		headers         []string
		known, replayed bool
	}{
		{name: "absent"},
		{name: "empty", headers: []string{""}},
		{name: "replay", headers: []string{"true"}, known: true, replayed: true},
		{name: "new_install", headers: []string{"false"}, known: true},
		{name: "invalid", headers: []string{"unknown"}},
		{name: "uppercase", headers: []string{"TRUE"}},
		{name: "combined", headers: []string{"true, false"}},
		{name: "duplicate", headers: []string{"true", "false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, value := range tc.headers {
					w.Header().Add("Idempotency-Replayed", value)
				}
				io.WriteString(w, expertEnvelopeJSON)
			}))
			defer server.Close()
			out, err := New(server.URL).InstallCapability(context.Background(), "token", "space", "ws", "key", capabilityRequest())
			if err != nil || out == nil {
				t.Fatalf("valid envelope failed: %v", err)
			}
			if out.ReplayKnown != tc.known || out.Replayed != tc.replayed {
				t.Fatalf("replay known=%v replayed=%v", out.ReplayKnown, out.Replayed)
			}
		})
	}
}

type capabilityRoundTripper func(*http.Request) (*http.Response, error)

func (f capabilityRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCapabilityTransportCannotReplayPostAutomatically(t *testing.T) {
	client := New("https://fleet.example.test")
	client.http.Transport = capabilityRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.GetBody != nil {
			t.Fatal("POST exposes an automatic replay body")
		}
		return nil, errors.New("connection lost")
	})
	if _, err := client.InstallCapability(context.Background(), "token", "space", "ws", "key", capabilityRequest()); err == nil {
		t.Fatal("transport failure returned success")
	}
}

func TestCapabilityInstallRejectsBadRepliesWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"bare_result", expertResultJSON, 200},
		{"missing_data", `{}`, 200},
		{"null_data", `{"data":null}`, 200},
		{"wrong_data_type", `{"data":[]}`, 200},
		{"missing_experts", `{"data":{"type":"expert","expert_id":"e"}}`, 200},
		{"wrong_id", strings.Replace(expertEnvelopeJSON, `"expert_id":"expert-1"`, `"expert_id":"other"`, 1), 200},
		{"wrong_type", strings.Replace(expertEnvelopeJSON, `"type":"expert"`, `"type":"expert_team"`, 1), 200},
		{"oversized", strings.Repeat("x", maxRespBytes+1), 200},
		{"conflict", `{"error":{"code":"DUPLICATE","message":"secret-value","details":{"resource":"idempotency_key","name":"private-key"}}}`, 409},
		{"redirect", "", 302},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/do-not-follow")
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			_, err := New(server.URL).InstallCapability(context.Background(), "token", "space", "ws", "key", capabilityRequest())
			if err == nil || calls != 1 || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if test.status == 409 {
				var apiErr *CapabilityAPIError
				if !errors.As(err, &apiErr) || apiErr.Code != "DUPLICATE" || !apiErr.IdempotencyKeyReused {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCapabilityInstallRetainsOnlyConflictMessage(t *testing.T) {
	const conflictMessage = "  同名技能的内容不同，请确认后重试。\n"
	for _, tc := range []struct {
		name, body, wantMessage string
		status                  int
	}{
		{"conflict_original", `{"error":{"code":"CONFLICT","message":"  同名技能的内容不同，请确认后重试。\n"}}`, conflictMessage, 409},
		{"conflict_long", `{"error":{"code":"CONFLICT","message":"` + strings.Repeat("冲突", 300) + `"}}`, strings.Repeat("冲突", 300), 409},
		{"conflict_missing", `{"error":{"code":"CONFLICT"}}`, "", 409},
		{"conflict_blank", `{"error":{"code":"CONFLICT","message":" \t\n"}}`, "", 409},
		{"conflict_null", `{"error":{"code":"CONFLICT","message":null}}`, "", 409},
		{"conflict_wrong_type", `{"error":{"code":"CONFLICT","message":{"reason":"secret-value"}}}`, "", 409},
		{"bad_request", `{"error":{"code":"VALIDATION_ERROR","message":"secret-value"}}`, "", 400},
		{"rate_limited", `{"error":{"code":"RATE_LIMITED","message":"secret-value"}}`, "", 429},
		{"upstream_failure", `{"error":{"code":"INTERNAL_ERROR","message":"secret-value"}}`, "", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			_, err := New(server.URL).InstallCapability(context.Background(), "token", "space", "ws", "key", capabilityRequest())
			var apiErr *CapabilityAPIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected CapabilityAPIError, got %v", err)
			}
			if apiErr.Status != tc.status || apiErr.Code == "" || apiErr.RetryAfter != "3" || apiErr.Message != tc.wantMessage {
				t.Fatal("upstream error fields were not preserved as expected")
			}
			if apiErr.Error() != "Fleet capability installation failed" {
				t.Fatal("error text must not include upstream message")
			}
		})
	}
}

func TestCapabilityInstallRecognizesIdempotencyConflictDetails(t *testing.T) {
	for _, tc := range []struct {
		name, code, details string
		status              int
		want                bool
	}{
		{"idempotency_key", "DUPLICATE", `{"resource":"idempotency_key","name":"private-key","existing_id":"private-id"}`, 409, true},
		{"skill_conflict", "DUPLICATE", `{"resource":"skill","name":"private-name"}`, 409, false},
		{"expert_conflict", "DUPLICATE", `{"resource":"expert"}`, 409, false},
		{"wrong_code", "CONFLICT", `{"resource":"idempotency_key"}`, 409, false},
		{"wrong_status", "DUPLICATE", `{"resource":"idempotency_key"}`, 400, false},
		{"empty_details", "DUPLICATE", `{}`, 409, false},
		{"null_details", "DUPLICATE", `null`, 409, false},
		{"wrong_details_type", "DUPLICATE", `"private-detail"`, 409, false},
		{"wrong_resource_type", "DUPLICATE", `{"resource":42}`, 409, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, `{"error":{"code":"`+tc.code+`","message":"Fleet conflict message","details":`+tc.details+`}}`)
			}))
			defer server.Close()
			_, err := New(server.URL).InstallCapability(context.Background(), "token", "space", "ws", "key", capabilityRequest())
			var apiErr *CapabilityAPIError
			if !errors.As(err, &apiErr) || apiErr.IdempotencyKeyReused != tc.want {
				t.Fatalf("idempotency conflict classification mismatch: %v", err)
			}
			if tc.status == 409 && apiErr.Message != "Fleet conflict message" {
				t.Fatal("conflict details must not affect the original display message")
			}
			if apiErr.Error() != "Fleet capability installation failed" {
				t.Fatal("upstream details must not enter the error text")
			}
		})
	}
}
