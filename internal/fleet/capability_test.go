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
	valid := `{"type":"expert_team","expert_team_id":"team-1","leader_expert_id":"expert-1","expert_team":{"expert_team_id":"team-1","name":"team","version":null,"created":true,"restored":false},"experts":[{"expert_id":"expert-1","name":"Custom name","version":null,"created":true,"restored":false,"skill_ids":["skill-1","skill-2"],"is_leader":true}],"skills":[{"skill_id":"skill-1","name":"review","version":null,"created":true},{"skill_id":"skill-2","name":"deploy","version":null,"created":true}]}`
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
			if got := validCapabilityResult(in.Definition, out, false); got != tc.want {
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

func TestCapabilityInstallValidatesSkillMetadata(t *testing.T) {
	version := "1.2.3"
	for _, tc := range []struct {
		name          string
		version       *string
		marketSkillID string
		metadata      string
		legacy        bool
		badBinding    bool
		wantSuccess   bool
	}{
		{name: "matching_version_and_source", version: &version, marketSkillID: "market-review", metadata: `"version":"1.2.3","market_skill_id":"market-review","created":true`, wantSuccess: true},
		{name: "different_version", version: &version, marketSkillID: "market-review", metadata: `"version":"2.0.0","market_skill_id":"market-review","created":true`},
		{name: "missing_version", version: &version, marketSkillID: "market-review", metadata: `"market_skill_id":"market-review","created":true`},
		{name: "null_version", marketSkillID: "market-review", metadata: `"version":null,"market_skill_id":"market-review","created":true`, wantSuccess: true},
		{name: "unexpected_version", marketSkillID: "market-review", metadata: `"version":"1.2.3","market_skill_id":"market-review","created":true`},
		{name: "empty_is_not_null", metadata: `"version":"","created":true`},
		{name: "different_source", version: &version, marketSkillID: "market-review", metadata: `"version":"1.2.3","market_skill_id":"market-other","created":true`},
		{name: "missing_source", version: &version, marketSkillID: "market-review", metadata: `"version":"1.2.3","created":true`},
		{name: "unexpected_source", metadata: `"version":null,"market_skill_id":"market-other","created":true`},
		{name: "no_version_or_source", metadata: `"version":null,"created":true`, wantSuccess: true},
		{name: "historical_wire_without_metadata", version: &version, marketSkillID: "market-review", metadata: `"created":false`, legacy: true, wantSuccess: true},
		{name: "historical_wire_still_validates_bindings", version: &version, marketSkillID: "market-review", metadata: `"created":false`, legacy: true, badBinding: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := capabilityRequest()
			request.Definition.Skills = []CapabilitySkill{{Name: "review", Version: tc.version, MarketSkillID: tc.marketSkillID}}
			request.Definition.Experts[0].SkillNames = []string{"review"}
			request.LegacySkillWire = tc.legacy
			fixture := `{"data":{"type":"expert","expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","version":null,"created":true,"restored":false,"skill_ids":["skill-1"]}],"skills":[{"skill_id":"skill-1","name":"review",` + tc.metadata + `}]}}`
			if tc.badBinding {
				fixture = strings.Replace(fixture, `"skill_ids":["skill-1"]`, `"skill_ids":["other"]`, 1)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				io.WriteString(w, fixture)
			}))
			defer server.Close()

			out, err := New(server.URL).InstallCapability(context.Background(), "token", "space", "workspace", "skill-key", request)
			if tc.wantSuccess {
				if err != nil {
					t.Fatalf("valid Skill installation rejected: %v", err)
				}
				if out.Skills[0].SkillID != "skill-1" || out.Experts[0].SkillIDs[0] != "skill-1" {
					t.Fatalf("unexpected installed Skill binding: %#v", out)
				}
			} else if err == nil || out != nil {
				t.Fatal("invalid Skill metadata or binding was accepted")
			}
			if calls != 1 {
				t.Fatalf("Fleet mutations=%d, want exactly one", calls)
			}
		})
	}
}

func capabilityConnectorFixture(name, marketMCPID string, version *string) CapabilityConnector {
	lowerName := strings.ToLower(name)
	return CapabilityConnector{
		Name:          name,
		Description:   name + " integration fixture",
		Slug:          lowerName,
		Category:      "testing",
		Documentation: "https://example.test/connectors/" + lowerName,
		Tags:          []string{"test"},
		Config:        json.RawMessage(`{"mcpServers":{"fixture":{"url":"https://mcp.example.test"}}}`),
		Version:       version,
		MarketMCPID:   marketMCPID,
	}
}

func connectorOnlyCapabilityRequest(names ...string) CapabilityInstallRequest {
	connectors := make([]CapabilityConnector, 0, len(names))
	for _, name := range names {
		connectors = append(connectors, capabilityConnectorFixture(name, "market-"+strings.ToLower(name), nil))
	}
	return CapabilityInstallRequest{Definition: CapabilityDefinition{
		SchemaVersion: "1.0",
		Name:          "workspace:connectors",
		Connectors:    connectors,
	}}
}

func TestCapabilityInstallAcceptsConnectorOnlyResult(t *testing.T) {
	const fixture = `{"data":{"type":"connectors","connectors":[{"connector_id":"11111111-1111-4111-8111-111111111111","name":"browser","version":"2.4.1","market_mcp_id":"market-browser","created":true}]}}`
	version := "2.4.1"
	request := CapabilityInstallRequest{Definition: CapabilityDefinition{
		SchemaVersion: "1.0",
		Name:          "workspace:connectors",
		Connectors: []CapabilityConnector{
			capabilityConnectorFixture("browser", "market-browser", &version),
		},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, fixture)
	}))
	defer server.Close()

	out, err := New(server.URL).InstallCapability(context.Background(), "token", "space", "workspace", "connector-key", request)
	if err != nil {
		t.Fatalf("connector-only result was rejected: %v", err)
	}
	if out.Type != "connectors" || len(out.Experts) != 0 || len(out.Skills) != 0 || len(out.Connectors) != 1 {
		t.Fatal("connector-only result did not preserve its resource shape")
	}
	got := out.Connectors[0]
	if got.ConnectorID != "11111111-1111-4111-8111-111111111111" || got.Name != "browser" || got.Version == nil || *got.Version != "2.4.1" || got.MarketMCPID == nil || *got.MarketMCPID != "market-browser" || !got.Created {
		t.Fatal("connector-only result identity or metadata was not preserved")
	}
}

func TestCapabilityInstallAcceptsTeamResultWithConnectorBindings(t *testing.T) {
	const fixture = `{"data":{"type":"expert_team","expert_team_id":"team-1","leader_expert_id":"expert-lead","experts":[{"expert_id":"expert-worker","name":"Worker","skill_ids":[],"connector_ids":["22222222-2222-4222-8222-222222222222"]},{"expert_id":"expert-lead","name":"Lead","skill_ids":[],"is_leader":true,"connector_ids":["33333333-3333-4333-8333-333333333333"]}],"skills":[],"connectors":[{"connector_id":"22222222-2222-4222-8222-222222222222","name":"private","version":null,"market_mcp_id":null,"created":false},{"connector_id":"33333333-3333-4333-8333-333333333333","name":"shared","version":"3.0","market_mcp_id":"market-shared","created":true}]}}`
	sharedVersion := "3.0"
	request := CapabilityInstallRequest{
		Definition: CapabilityDefinition{
			SchemaVersion: "1.0",
			Name:          "marketplace:team",
			Connectors: []CapabilityConnector{
				capabilityConnectorFixture("shared", "market-shared", &sharedVersion),
				capabilityConnectorFixture("private", "", nil),
			},
			Experts: []CapabilityExpert{
				{Name: "Lead", ConnectorNames: []string{"shared"}},
				{Name: "Worker", ConnectorNames: []string{"private"}},
			},
			ExpertTeam: &CapabilityExpertTeam{
				Name: "team",
				Members: []CapabilityTeamMember{
					{ExpertName: "Lead", IsLeader: true},
					{ExpertName: "Worker"},
				},
			},
		},
		Bindings: CapabilityBindings{Experts: []CapabilityExpertBinding{
			{ExpertName: "Lead", RuntimeID: "runtime-lead"},
			{ExpertName: "Worker", RuntimeID: "runtime-worker"},
		}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, fixture)
	}))
	defer server.Close()

	out, err := New(server.URL).InstallCapability(context.Background(), "token", "space", "workspace", "team-key", request)
	if err != nil {
		t.Fatalf("team result with connector bindings was rejected: %v", err)
	}
	if out.Type != "expert_team" || out.ExpertTeamID != "team-1" || out.LeaderExpertID != "expert-lead" || len(out.Experts) != 2 || len(out.Connectors) != 2 {
		t.Fatal("team connector result did not preserve its resource shape")
	}
	if out.Experts[0].Name != "Worker" || len(out.Experts[0].ConnectorIDs) != 1 || out.Experts[0].ConnectorIDs[0] != "22222222-2222-4222-8222-222222222222" || out.Experts[1].Name != "Lead" || len(out.Experts[1].ConnectorIDs) != 1 || out.Experts[1].ConnectorIDs[0] != "33333333-3333-4333-8333-333333333333" {
		t.Fatal("team connector bindings were not preserved by expert name")
	}
}

func TestCapabilityInstallAcceptsExpertResultWithConnectorBinding(t *testing.T) {
	const fixture = `{"data":{"type":"expert","expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","skill_ids":[],"connector_ids":["44444444-4444-4444-8444-444444444444"]}],"skills":[],"connectors":[{"connector_id":"44444444-4444-4444-8444-444444444444","name":"browser","version":null,"market_mcp_id":"market-browser","created":true}]}}`
	request := capabilityRequest()
	request.Definition.Connectors = []CapabilityConnector{capabilityConnectorFixture("browser", "market-browser", nil)}
	request.Definition.Experts[0].ConnectorNames = []string{"browser"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, fixture)
	}))
	defer server.Close()

	out, err := New(server.URL).InstallCapability(context.Background(), "token", "space", "workspace", "expert-key", request)
	if err != nil {
		t.Fatalf("expert result with connector binding was rejected: %v", err)
	}
	if out.Type != "expert" || out.ExpertID != "expert-1" || len(out.Connectors) != 1 || len(out.Experts) != 1 || len(out.Experts[0].ConnectorIDs) != 1 || out.Experts[0].ConnectorIDs[0] != "44444444-4444-4444-8444-444444444444" {
		t.Fatal("expert connector result did not preserve its resource identity and binding")
	}
}

func TestCapabilityInstallRejectsInvalidConnectorIdentitiesAndBindings(t *testing.T) {
	expertRequest := capabilityRequest()
	expertRequest.Definition.Connectors = []CapabilityConnector{capabilityConnectorFixture("browser", "market-browser", nil)}
	expertRequest.Definition.Experts[0].ConnectorNames = []string{"browser"}

	teamRequest := CapabilityInstallRequest{
		Definition: CapabilityDefinition{
			SchemaVersion: "1.0",
			Name:          "marketplace:team",
			Connectors: []CapabilityConnector{
				capabilityConnectorFixture("browser", "market-browser", nil),
				capabilityConnectorFixture("docs", "market-docs", nil),
			},
			Experts: []CapabilityExpert{
				{Name: "Lead", ConnectorNames: []string{"browser"}},
				{Name: "Worker", ConnectorNames: []string{"docs"}},
			},
			ExpertTeam: &CapabilityExpertTeam{
				Name:    "team",
				Members: []CapabilityTeamMember{{ExpertName: "Lead", IsLeader: true}, {ExpertName: "Worker"}},
			},
		},
	}
	nullableIdentityRequest := CapabilityInstallRequest{Definition: CapabilityDefinition{
		SchemaVersion: "1.0",
		Name:          "workspace:connectors",
		Connectors:    []CapabilityConnector{capabilityConnectorFixture("browser", "", nil)},
	}}

	tests := []struct {
		name    string
		request CapabilityInstallRequest
		fixture string
	}{
		{
			name:    "missing_connector_result",
			request: connectorOnlyCapabilityRequest("browser"),
			fixture: `{"data":{"type":"connectors","connectors":[]}}`,
		},
		{
			name:    "missing_connector_id",
			request: connectorOnlyCapabilityRequest("browser"),
			fixture: `{"data":{"type":"connectors","connectors":[{"name":"browser","version":null,"market_mcp_id":"market-browser","created":true}]}}`,
		},
		{
			name:    "duplicate_connector_id",
			request: connectorOnlyCapabilityRequest("browser", "alerts"),
			fixture: `{"data":{"type":"connectors","connectors":[{"connector_id":"55555555-5555-4555-8555-555555555555","name":"browser","version":null,"market_mcp_id":"market-browser","created":true},{"connector_id":"55555555-5555-4555-8555-555555555555","name":"alerts","version":null,"market_mcp_id":"market-alerts","created":true}]}}`,
		},
		{
			name:    "invalid_connector_uuid",
			request: connectorOnlyCapabilityRequest("browser"),
			fixture: `{"data":{"type":"connectors","connectors":[{"connector_id":"not-a-uuid","name":"browser","version":null,"market_mcp_id":"market-browser","created":true}]}}`,
		},
		{
			name:    "missing_connector_version",
			request: nullableIdentityRequest,
			fixture: `{"data":{"type":"connectors","connectors":[{"connector_id":"55555555-5555-4555-8555-555555555555","name":"browser","market_mcp_id":null,"created":true}]}}`,
		},
		{
			name:    "missing_connector_market_mcp_id",
			request: nullableIdentityRequest,
			fixture: `{"data":{"type":"connectors","connectors":[{"connector_id":"55555555-5555-4555-8555-555555555555","name":"browser","version":null,"created":true}]}}`,
		},
		{
			name:    "unknown_connector_name",
			request: connectorOnlyCapabilityRequest("browser"),
			fixture: `{"data":{"type":"connectors","connectors":[{"connector_id":"66666666-6666-4666-8666-666666666666","name":"unrequested","version":null,"market_mcp_id":"market-browser","created":true}]}}`,
		},
		{
			name:    "expert_binds_unknown_connector_id",
			request: expertRequest,
			fixture: `{"data":{"type":"expert","expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","skill_ids":[],"connector_ids":["88888888-8888-4888-8888-888888888888"]}],"skills":[],"connectors":[{"connector_id":"77777777-7777-4777-8777-777777777777","name":"browser","version":null,"market_mcp_id":"market-browser","created":true}]}}`,
		},
		{
			name:    "expert_bindings_are_swapped",
			request: teamRequest,
			fixture: `{"data":{"type":"expert_team","expert_team_id":"team-1","leader_expert_id":"expert-lead","expert_team":{"expert_team_id":"team-1","name":"team","version":null,"created":true,"restored":false},"experts":[{"expert_id":"expert-worker","name":"Worker","version":null,"created":true,"skill_ids":[],"connector_ids":["99999999-9999-4999-8999-999999999999"]},{"expert_id":"expert-lead","name":"Lead","version":null,"created":true,"skill_ids":[],"is_leader":true,"connector_ids":["aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"]}],"skills":[],"connectors":[{"connector_id":"99999999-9999-4999-8999-999999999999","name":"browser","version":null,"market_mcp_id":"market-browser","created":true},{"connector_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","name":"docs","version":null,"market_mcp_id":"market-docs","created":true}]}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				io.WriteString(w, tc.fixture)
			}))
			defer server.Close()

			out, err := New(server.URL).InstallCapability(context.Background(), "token", "space", "workspace", "connector-key", tc.request)
			if err == nil || out != nil {
				t.Fatal("invalid connector identity or binding was accepted")
			}
			if calls != 1 {
				t.Fatalf("Fleet calls=%d, want one fixed-fixture response", calls)
			}
		})
	}
}

func TestCapabilityInstallAcceptsExplicitNullConnectorIdentityFields(t *testing.T) {
	const fixture = `{"data":{"type":"connectors","connectors":[{"connector_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"browser","version":null,"market_mcp_id":null,"created":true}]}}`
	request := CapabilityInstallRequest{Definition: CapabilityDefinition{
		SchemaVersion: "1.0",
		Name:          "workspace:connectors",
		Connectors:    []CapabilityConnector{capabilityConnectorFixture("browser", "", nil)},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, fixture)
	}))
	defer server.Close()

	out, err := New(server.URL).InstallCapability(context.Background(), "token", "space", "workspace", "connector-key", request)
	if err != nil {
		t.Fatalf("explicit null connector identity fields were rejected: %v", err)
	}
	if len(out.Connectors) != 1 || out.Connectors[0].Version != nil || out.Connectors[0].MarketMCPID != nil {
		t.Fatal("explicit null connector identity fields were not preserved")
	}
}

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
		if err != nil || out.ExpertID != "expert-1" || len(out.Connectors) != 0 || !out.Replayed || !out.ReplayKnown {
			t.Fatalf("legacy result changed or was rejected: err=%v", err)
		}
	}
	if bodies[0] != bodies[1] || !strings.Contains(bodies[0], `"custom_env":{"OCTOBUDDY_PROVIDER_ID":"provider"}`) {
		t.Fatal("request is not stable or environment binding was lost")
	}
	var legacyRequest map[string]json.RawMessage
	if err := json.Unmarshal([]byte(bodies[0]), &legacyRequest); err != nil {
		t.Fatal("legacy request was not valid JSON")
	}
	var definition map[string]json.RawMessage
	if err := json.Unmarshal(legacyRequest["definition"], &definition); err != nil {
		t.Fatal("legacy definition was not valid JSON")
	}
	if _, exists := definition["connectors"]; exists {
		t.Fatal("legacy request gained a definition.connectors field")
	}
	var experts []map[string]json.RawMessage
	if err := json.Unmarshal(definition["experts"], &experts); err != nil || len(experts) != 1 {
		t.Fatal("legacy expert request changed")
	}
	if _, exists := experts[0]["connector_names"]; exists {
		t.Fatal("legacy request gained an expert connector binding")
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

func TestCapabilityInstallDiscardsUpstreamDisplayText(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"conflict-string", `{"error":{"code":"DUPLICATE","message":"private-display-secret","details":{"resource":"idempotency_key","name":"private-detail-secret"}}}`, 409},
		{"conflict-object", `{"error":{"code":"DUPLICATE","message":{"diagnostic":"private-display-secret"},"details":{"resource":"skill"}}}`, 409},
		{"conflict-missing", `{"error":{"code":"CONFLICT"}}`, 409},
		{"bad-request", `{"error":{"code":"VALIDATION_ERROR","message":"private-display-secret"}}`, 400},
		{"rate-limit", `{"error":{"code":"RATE_LIMITED","message":"private-display-secret"}}`, 429},
		{"server-error", `{"error":{"code":"INTERNAL_ERROR","message":"private-display-secret"}}`, 500},
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
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status || apiErr.RetryAfter != "3" {
				t.Fatalf("wrong safe metadata: %v", err)
			}
			raw, _ := json.Marshal(apiErr)
			if strings.Contains(string(raw), "private-") || apiErr.Error() != "Fleet capability installation failed" {
				t.Fatal("upstream text retained")
			}
			if tc.name == "conflict-string" && !apiErr.IdempotencyKeyReused {
				t.Fatal("safe key conflict lost")
			}
			if tc.name == "conflict-object" && apiErr.ConflictResource != "skill" {
				t.Fatal("display text type affected safe metadata")
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
			if apiErr.Error() != "Fleet capability installation failed" {
				t.Fatal("upstream details must not enter the error text")
			}
		})
	}
}

func TestCapabilityResultValidatesExpertSourceMetadata(t *testing.T) {
	version := "1.2.3"
	in := capabilityRequest()
	in.Definition.Experts[0].Version = &version
	in.Definition.Experts[0].MarketExpertID = "market-expert"

	for _, tc := range []struct {
		name string
		json string
		ok   bool
	}{
		{
			name: "matching",
			json: `{"type":"expert","expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","version":"1.2.3","market_expert_id":"market-expert","created":true,"restored":false,"skill_ids":[]}],"skills":[]}`,
			ok:   true,
		},
		{
			name: "wrong_version",
			json: `{"type":"expert","expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","version":"2.0.0","market_expert_id":"market-expert","skill_ids":[]}],"skills":[]}`,
		},
		{
			name: "missing_version",
			json: `{"type":"expert","expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","market_expert_id":"market-expert","skill_ids":[]}],"skills":[]}`,
		},
		{
			name: "reused_expert_keeps_first_source",
			json: `{"type":"expert","expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","version":"1.2.3","market_expert_id":"market-expert-old","created":false,"skill_ids":[]}],"skills":[]}`,
			ok:   true,
		},
		{
			name: "reused_expert_without_source_metadata",
			json: `{"type":"expert","expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","version":"1.2.3","skill_ids":[]}],"skills":[]}`,
			ok:   true,
		},
		{
			name: "created_expert_wrong_source",
			json: `{"type":"expert","expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","version":"1.2.3","market_expert_id":"other","created":true,"skill_ids":[]}],"skills":[]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out CapabilityInstallResult
			if err := json.Unmarshal([]byte(tc.json), &out); err != nil {
				t.Fatal(err)
			}
			if got := validCapabilityResult(in.Definition, out, false); got != tc.ok {
				t.Fatalf("validCapabilityResult=%v want %v", got, tc.ok)
			}
		})
	}
}

func TestCapabilityResultValidatesExpertTeamSourceMetadata(t *testing.T) {
	version := "team-4.0"
	memberVersion := "expert-1.0"
	stringPtr := func(value string) *string { return &value }
	in := capabilityRequest()
	in.Definition.Experts[0].Version = &memberVersion
	in.Definition.Experts[0].MarketExpertID = "market-expert"
	in.Definition.ExpertTeam = &CapabilityExpertTeam{
		Name: "Delivery", Version: &version, MarketExpertTeamID: "market-team",
		Members: []CapabilityTeamMember{{ExpertName: "Custom name", IsLeader: true}},
	}
	valid := `{"type":"expert_team","expert_team_id":"team-1","leader_expert_id":"expert-1","expert_team":{"expert_team_id":"team-1","name":"Delivery","version":"team-4.0","market_expert_team_id":"market-team","created":true,"restored":false},"experts":[{"expert_id":"expert-1","name":"Custom name","version":"expert-1.0","market_expert_id":"market-expert","is_leader":true,"skill_ids":[]}],"skills":[]}`
	for _, tc := range []struct {
		name   string
		json   string
		mutate func(*CapabilityInstallResult)
		want   bool
	}{
		{name: "matching", json: valid, want: true},
		{name: "missing_team_result", json: `{"type":"expert_team","expert_team_id":"team-1","leader_expert_id":"expert-1","experts":[{"expert_id":"expert-1","name":"Custom name","version":"expert-1.0","market_expert_id":"market-expert","is_leader":true,"skill_ids":[]}],"skills":[]}`},
		{name: "wrong_team_version", json: valid, mutate: func(r *CapabilityInstallResult) { r.ExpertTeam.Version = stringPtr("team-9.0") }},
		{name: "wrong_team_source", json: valid, mutate: func(r *CapabilityInstallResult) { r.ExpertTeam.MarketExpertTeamID = "other" }},
		{name: "wrong_member_version", json: valid, mutate: func(r *CapabilityInstallResult) { r.Experts[0].Version = stringPtr("expert-9.0") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out CapabilityInstallResult
			if err := json.Unmarshal([]byte(tc.json), &out); err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(&out)
			}
			if got := validCapabilityResult(in.Definition, out, false); got != tc.want {
				t.Fatalf("validCapabilityResult=%v want %v", got, tc.want)
			}
		})
	}
}

func TestLegacyCapabilityWireOmitsExpertSourceMetadata(t *testing.T) {
	version := "1.2.3"
	in := capabilityRequest()
	in.LegacySkillWire = true
	in.Definition.Experts[0].Version = &version
	in.Definition.Experts[0].MarketExpertID = "market-expert"
	in.Definition.ExpertTeam = &CapabilityExpertTeam{
		Name: "Delivery", Version: &version, MarketExpertTeamID: "market-team",
		Members: []CapabilityTeamMember{{ExpertName: "Custom name", IsLeader: true}},
	}

	body, err := MarshalCapabilityInstallRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Definition struct {
			Experts []map[string]json.RawMessage `json:"experts"`
			Team    map[string]json.RawMessage   `json:"expert_team"`
		} `json:"definition"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Definition.Experts) != 1 || raw.Definition.Experts[0]["version"] != nil || raw.Definition.Experts[0]["market_expert_id"] != nil {
		t.Fatalf("legacy expert metadata was added to replay bytes: %s", body)
	}
	if raw.Definition.Team["version"] != nil || raw.Definition.Team["market_expert_team_id"] != nil {
		t.Fatalf("legacy team metadata was added to replay bytes: %s", body)
	}
}

func TestCapabilityResultAcceptsReusedTeamLocalVersionGraph(t *testing.T) {
	teamVersion := "team-4.0"
	memberVersion := "expert-1.0"
	skillVersion := "review-3.0"
	in := capabilityRequest()
	in.Definition.Experts = []CapabilityExpert{{
		Name: "Local member", Version: &memberVersion, MarketExpertID: "market-member-new",
		SkillNames: []string{"Review"},
	}}
	in.Definition.Skills = []CapabilitySkill{{Name: "Review", Version: &skillVersion, MarketSkillID: "market-skill-new"}}
	in.Definition.ExpertTeam = &CapabilityExpertTeam{
		Name: "Delivery", Version: &teamVersion, MarketExpertTeamID: "market-team-new",
		Members: []CapabilityTeamMember{{ExpertName: "Local member", IsLeader: true}},
	}
	fixture := `{"type":"expert_team","expert_team_id":"team-local","leader_expert_id":"expert-v1","expert_team":{"expert_team_id":"team-local","name":"Delivery","version":"team-4.0","market_expert_team_id":"market-team-original","created":false,"restored":false},"experts":[{"expert_id":"expert-v1","name":"Local member","version":"expert-1.0","market_expert_id":"market-member-original","created":false,"skill_ids":["skill-v1"]},{"expert_id":"expert-v2","name":"Local member","version":"expert-2.0","market_expert_id":"market-member-v2","created":false,"skill_ids":["skill-v2"]}],"skills":[{"skill_id":"skill-v1","name":"Review","version":"review-2.0","market_skill_id":"market-skill-original","created":false},{"skill_id":"skill-v2","name":"Review","version":"review-3.0","market_skill_id":"market-skill-v2","created":false}]}`
	var out CapabilityInstallResult
	if err := json.Unmarshal([]byte(fixture), &out); err != nil {
		t.Fatal(err)
	}
	if !validCapabilityResult(in.Definition, out, false) {
		t.Fatal("valid committed local entity snapshot was rejected")
	}
}

func TestCapabilityResultAcceptsReusedExpertFirstSourceAndBindings(t *testing.T) {
	version := "expert-4.0"
	skillVersion := "review-3.0"
	in := capabilityRequest()
	in.Definition.Experts[0].Version = &version
	in.Definition.Experts[0].MarketExpertID = "market-expert-new"
	in.Definition.Experts[0].SkillNames = []string{"Review"}
	in.Definition.Skills = []CapabilitySkill{{Name: "Review", Version: &skillVersion, MarketSkillID: "market-skill-new"}}
	fixture := `{"type":"expert","expert_id":"expert-local","experts":[{"expert_id":"expert-local","name":"Custom name","version":"expert-4.0","market_expert_id":"market-expert-original","created":false,"skill_ids":["skill-local"]}],"skills":[{"skill_id":"skill-local","name":"Review","version":"review-3.0","market_skill_id":"market-skill-original","created":false}]}`
	var out CapabilityInstallResult
	if err := json.Unmarshal([]byte(fixture), &out); err != nil {
		t.Fatal(err)
	}
	if !validCapabilityResult(in.Definition, out, false) {
		t.Fatal("valid committed local entity snapshot was rejected")
	}
}
