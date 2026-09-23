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
	return CapabilityInstallRequest{Definition: CapabilityDefinition{SchemaVersion: "1.0", Name: "marketplace:p1", Experts: []CapabilityExpert{{Name: "Custom name", Instructions: "review"}}}, Bindings: CapabilityBindings{Experts: []CapabilityExpertBinding{{ExpertName: "Custom name", RuntimeID: "runtime", CustomEnv: map[string]string{"OCTOBUDDY_PROVIDER_ID": "provider"}}}}}
}

const expertResultJSON = `{"type":"expert","expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","skill_ids":[]}],"skills":[]}`

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
		w.Header().Set("Idempotency-Replayed", "true")
		io.WriteString(w, expertResultJSON)
	}))
	defer server.Close()
	client := New(server.URL).WithCapabilityInstall(true)
	for range 2 {
		out, err := client.InstallCapability(context.Background(), "user-token", "space", "workspace", "operation-key", capabilityRequest())
		if err != nil || out.ExpertID != "expert-1" || !out.Replayed {
			t.Fatalf("out=%+v err=%v", out, err)
		}
	}
	if bodies[0] != bodies[1] || !strings.Contains(bodies[0], `"custom_env":{"OCTOBUDDY_PROVIDER_ID":"provider"}`) {
		t.Fatal("request is not stable or environment binding was lost")
	}
}

func TestCapabilityInstallDisabledDoesNotContactFleet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("disabled client contacted Fleet") }))
	defer server.Close()
	_, err := New(server.URL).InstallCapability(context.Background(), "", "", "", "", capabilityRequest())
	if !errors.Is(err, ErrCapabilityInstallDisabled) {
		t.Fatal(err)
	}
}

type capabilityRoundTripper func(*http.Request) (*http.Response, error)

func (f capabilityRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCapabilityTransportCannotReplayPostAutomatically(t *testing.T) {
	client := New("https://fleet.example.test").WithCapabilityInstall(true)
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
		{"missing_experts", `{"type":"expert","expert_id":"e"}`, 200},
		{"wrong_id", strings.Replace(expertResultJSON, `"expert_id":"expert-1"`, `"expert_id":"other"`, 1), 200},
		{"wrong_type", strings.Replace(expertResultJSON, `"type":"expert"`, `"type":"expert_team"`, 1), 200},
		{"oversized", strings.Repeat("x", maxRespBytes+1), 200},
		{"conflict", `{"error":{"code":"IDEMPOTENCY_KEY_REUSED","message":"secret-value"}}`, 409},
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
			_, err := New(server.URL).WithCapabilityInstall(true).InstallCapability(context.Background(), "token", "space", "ws", "key", capabilityRequest())
			if err == nil || calls != 1 || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if test.status == 409 {
				var apiErr *CapabilityAPIError
				if !errors.As(err, &apiErr) || apiErr.Code != "IDEMPOTENCY_KEY_REUSED" {
					t.Fatal(err)
				}
			}
		})
	}
}
