package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	graphConnectorID       = "44444444-4444-4444-8444-444444444444"
	graphOrphanConnectorID = "55555555-5555-4555-8555-555555555555"
)

func graphString(value string) *string { return &value }

// installCapabilityGraphFixture replays one fixed successful Fleet response
// through the client, so these tests cover response decoding plus the
// returned-graph rules instead of the validator in isolation.
func installCapabilityGraphFixture(t *testing.T, request CapabilityInstallRequest, out CapabilityInstallResult) (CapabilityInstallResult, error) {
	t.Helper()
	body, err := json.Marshal(struct {
		Data CapabilityInstallResult `json:"data"`
	}{Data: out})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(body)
	}))
	defer server.Close()
	result, err := New(server.URL).InstallCapability(context.Background(), "token", "space", "workspace", "graph-key", request)
	if result == nil {
		return CapabilityInstallResult{}, err
	}
	return *result, err
}

// TestCapabilityInstallRejectsUnreferencedReturnedResources pins returned-graph
// closure: a reused or restored expert/team may rewrite the local graph, but
// every Skill and Connector Fleet reports must be bound by at least one expert
// it also reports. A top-level resource no returned expert references is a
// malformed response, not a legitimate local graph change.
func TestCapabilityInstallRejectsUnreferencedReturnedResources(t *testing.T) {
	teamVersion, memberVersion := "team-4.0", "expert-1.0"
	skillRequest := capabilityRequest()
	skillRequest.Definition.Skills = []CapabilitySkill{{Name: "Review", Version: graphString("review-3.0"), MarketSkillID: "market-skill"}}
	skillRequest.Definition.Experts[0].SkillNames = []string{"Review"}
	skillRequest.Definition.Experts[0].Version = &memberVersion

	connectorRequest := capabilityRequest()
	connectorRequest.Definition.Connectors = []CapabilityConnector{capabilityConnectorFixture("browser", "market-browser", nil)}
	connectorRequest.Definition.Experts[0].ConnectorNames = []string{"browser"}
	connectorRequest.Definition.Experts[0].Version = &memberVersion

	teamRequest := capabilityRequest()
	teamRequest.Definition.Experts = []CapabilityExpert{{Name: "Lead", Version: &memberVersion}, {Name: "Worker", Version: &memberVersion}}
	teamRequest.Definition.ExpertTeam = &CapabilityExpertTeam{
		Name: "Delivery", Version: &teamVersion, MarketExpertTeamID: "market-team",
		Members: []CapabilityTeamMember{{ExpertName: "Lead", IsLeader: true}, {ExpertName: "Worker"}},
	}

	teamResult := func(experts []CapabilityExpertResult, skills []CapabilitySkillResult, connectors []CapabilityConnectorResult) CapabilityInstallResult {
		return CapabilityInstallResult{
			Type: "expert_team", ExpertTeamID: "team-local", LeaderExpertID: "expert-lead",
			ExpertTeam: &CapabilityExpertTeamResult{
				ExpertTeamID: "team-local", Name: "Delivery", Version: &teamVersion,
				MarketExpertTeamID: "market-team-original",
			},
			Experts: experts, Skills: skills, Connectors: connectors,
		}
	}
	teamLeader := func(skillIDs, connectorIDs []string) CapabilityExpertResult {
		return CapabilityExpertResult{ExpertID: "expert-lead", Name: "Lead", SkillIDs: skillIDs, ConnectorIDs: connectorIDs, IsLeader: true}
	}
	teamWorker := CapabilityExpertResult{ExpertID: "expert-worker", Name: "Worker", SkillIDs: []string{}}
	restoredTeam := func(skills []CapabilitySkillResult) CapabilityInstallResult {
		out := teamResult([]CapabilityExpertResult{teamLeader(nil, nil)}, skills, nil)
		out.ExpertTeam.Restored = true
		return out
	}

	tests := []struct {
		name    string
		request CapabilityInstallRequest
		out     CapabilityInstallResult
	}{
		{
			name:    "reused_expert_orphan_market_skill",
			request: skillRequest,
			out: CapabilityInstallResult{
				Type: "expert", ExpertID: "expert-local",
				Experts: []CapabilityExpertResult{{ExpertID: "expert-local", Name: "Custom name", Version: &memberVersion, SkillIDs: []string{}}},
				Skills:  []CapabilitySkillResult{{SkillID: "skill-local", Name: "Review", Version: graphString("review-3.0"), MarketSkillID: "market-skill-original"}},
			},
		},
		{
			name:    "restored_expert_orphan_skill",
			request: skillRequest,
			out: CapabilityInstallResult{
				Type: "expert", ExpertID: "expert-local",
				Experts: []CapabilityExpertResult{{ExpertID: "expert-local", Name: "Custom name", Version: &memberVersion, Restored: true, SkillIDs: []string{}}},
				Skills:  []CapabilitySkillResult{{SkillID: "skill-local", Name: "Review", Version: graphString("review-3.0")}},
			},
		},
		{
			name:    "reused_expert_orphan_connector",
			request: connectorRequest,
			out: CapabilityInstallResult{
				Type: "expert", ExpertID: "expert-local",
				Experts: []CapabilityExpertResult{{ExpertID: "expert-local", Name: "Custom name", Version: &memberVersion, SkillIDs: []string{}}},
				Skills:  []CapabilitySkillResult{},
				Connectors: []CapabilityConnectorResult{{
					ConnectorID: graphConnectorID, Name: "browser", MarketMCPID: graphString("market-browser"),
				}},
			},
		},
		{
			name:    "reused_expert_duplicate_skill_ref",
			request: skillRequest,
			out: CapabilityInstallResult{
				Type: "expert", ExpertID: "expert-local",
				Experts: []CapabilityExpertResult{{ExpertID: "expert-local", Name: "Custom name", Version: &memberVersion, SkillIDs: []string{"skill-local", "skill-local"}}},
				Skills:  []CapabilitySkillResult{{SkillID: "skill-local", Name: "Review"}},
			},
		},
		{
			name:    "reused_expert_duplicate_connector_ref",
			request: connectorRequest,
			out: CapabilityInstallResult{
				Type: "expert", ExpertID: "expert-local",
				Experts: []CapabilityExpertResult{{ExpertID: "expert-local", Name: "Custom name", Version: &memberVersion, SkillIDs: []string{}, ConnectorIDs: []string{graphConnectorID, graphConnectorID}}},
				Skills:  []CapabilitySkillResult{},
				Connectors: []CapabilityConnectorResult{{
					ConnectorID: graphConnectorID, Name: "browser", MarketMCPID: graphString("market-browser"),
				}},
			},
		},
		{
			name:    "reused_team_orphan_skill",
			request: teamRequest,
			out: teamResult(
				[]CapabilityExpertResult{teamLeader([]string{"skill-shared"}, nil), teamWorker},
				[]CapabilitySkillResult{{SkillID: "skill-shared", Name: "Shared"}, {SkillID: "skill-orphan", Name: "Orphan"}},
				nil,
			),
		},
		{
			name:    "reused_team_orphan_connector",
			request: teamRequest,
			out: teamResult(
				[]CapabilityExpertResult{teamLeader(nil, []string{graphConnectorID}), teamWorker},
				[]CapabilitySkillResult{},
				[]CapabilityConnectorResult{
					{ConnectorID: graphConnectorID, Name: "browser", MarketMCPID: graphString("market-browser")},
					{ConnectorID: graphOrphanConnectorID, Name: "alerts", MarketMCPID: graphString("market-alerts")},
				},
			),
		},
		{
			name:    "reused_team_orphan_skill_after_market_member_removed",
			request: teamRequest,
			out: teamResult(
				[]CapabilityExpertResult{teamLeader(nil, nil)},
				[]CapabilitySkillResult{{SkillID: "skill-orphan", Name: "Orphan"}},
				nil,
			),
		},
		{
			name:    "restored_team_orphan_skill",
			request: teamRequest,
			out:     restoredTeam([]CapabilitySkillResult{{SkillID: "skill-orphan", Name: "Orphan"}}),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := installCapabilityGraphFixture(t, tc.request, tc.out)
			if err == nil {
				t.Fatalf("unreferenced returned resource was accepted: %+v", out)
			}
		})
	}
}

// TestCapabilityInstallAcceptsReusedGraphLocalChanges pins the legitimate local
// graph: reused or restored entities may drop every market Skill and Connector,
// replace resources by name/version/ID, add local resources, lose market
// members or gain local members, and several experts may share one resource.
func TestCapabilityInstallAcceptsReusedGraphLocalChanges(t *testing.T) {
	teamVersion, memberVersion := "team-4.0", "expert-1.0"
	skillRequest := capabilityRequest()
	skillRequest.Definition.Skills = []CapabilitySkill{{Name: "Review", Version: graphString("review-3.0"), MarketSkillID: "market-skill-new"}}
	skillRequest.Definition.Experts[0].SkillNames = []string{"Review"}
	skillRequest.Definition.Experts[0].Version = &memberVersion

	connectorRequest := capabilityRequest()
	connectorRequest.Definition.Connectors = []CapabilityConnector{capabilityConnectorFixture("browser", "market-browser", nil)}
	connectorRequest.Definition.Experts[0].ConnectorNames = []string{"browser"}
	connectorRequest.Definition.Experts[0].Version = &memberVersion

	teamRequest := capabilityRequest()
	teamRequest.Definition.Experts = []CapabilityExpert{{Name: "Lead", Version: &memberVersion}, {Name: "Worker", Version: &memberVersion}}
	teamRequest.Definition.ExpertTeam = &CapabilityExpertTeam{
		Name: "Delivery", Version: &teamVersion, MarketExpertTeamID: "market-team",
		Members: []CapabilityTeamMember{{ExpertName: "Lead", IsLeader: true}, {ExpertName: "Worker"}},
	}

	teamResult := func(experts []CapabilityExpertResult, skills []CapabilitySkillResult, connectors []CapabilityConnectorResult) CapabilityInstallResult {
		return CapabilityInstallResult{
			Type: "expert_team", ExpertTeamID: "team-local", LeaderExpertID: "expert-lead",
			ExpertTeam: &CapabilityExpertTeamResult{
				ExpertTeamID: "team-local", Name: "Delivery", Version: &teamVersion,
				MarketExpertTeamID: "market-team-original",
			},
			Experts: experts, Skills: skills, Connectors: connectors,
		}
	}
	teamLeader := func(skillIDs, connectorIDs []string) CapabilityExpertResult {
		return CapabilityExpertResult{ExpertID: "expert-lead", Name: "Lead", SkillIDs: skillIDs, ConnectorIDs: connectorIDs, IsLeader: true}
	}
	teamWorker := func(skillIDs, connectorIDs []string) CapabilityExpertResult {
		return CapabilityExpertResult{ExpertID: "expert-worker", Name: "Worker", SkillIDs: skillIDs, ConnectorIDs: connectorIDs}
	}
	expertResult := func(expert CapabilityExpertResult, skills []CapabilitySkillResult, connectors []CapabilityConnectorResult) CapabilityInstallResult {
		return CapabilityInstallResult{Type: "expert", ExpertID: expert.ExpertID, Experts: []CapabilityExpertResult{expert}, Skills: skills, Connectors: connectors}
	}

	tests := []struct {
		name    string
		request CapabilityInstallRequest
		out     CapabilityInstallResult
		check   func(t *testing.T, out CapabilityInstallResult)
	}{
		{
			name:    "reused_expert_replaces_market_skill",
			request: skillRequest,
			out: expertResult(
				CapabilityExpertResult{ExpertID: "expert-local", Name: "Custom name", Version: &memberVersion, SkillIDs: []string{"skill-local"}},
				[]CapabilitySkillResult{{SkillID: "skill-local", Name: "Local review", Version: graphString("review-9.0"), MarketSkillID: "market-skill-original"}},
				nil,
			),
			check: func(t *testing.T, out CapabilityInstallResult) {
				if len(out.Skills) != 1 || out.Skills[0].Name != "Local review" || out.Skills[0].Version == nil || *out.Skills[0].Version != "review-9.0" {
					t.Fatalf("local Skill replacement was not preserved: %+v", out.Skills)
				}
			},
		},
		{
			name:    "reused_expert_deletes_every_skill",
			request: skillRequest,
			out: expertResult(
				CapabilityExpertResult{ExpertID: "expert-local", Name: "Custom name", Version: &memberVersion, SkillIDs: []string{}},
				[]CapabilitySkillResult{},
				nil,
			),
			check: func(t *testing.T, out CapabilityInstallResult) {
				if len(out.Skills) != 0 {
					t.Fatalf("deleted local Skills were invented: %+v", out.Skills)
				}
			},
		},
		{
			name:    "restored_expert_deletes_every_skill",
			request: skillRequest,
			out: expertResult(
				CapabilityExpertResult{ExpertID: "expert-local", Name: "Custom name", Version: &memberVersion, Restored: true, SkillIDs: []string{}},
				[]CapabilitySkillResult{},
				nil,
			),
			check: func(t *testing.T, out CapabilityInstallResult) {
				if len(out.Skills) != 0 || !out.Experts[0].Restored {
					t.Fatalf("restored local graph was rewritten: %+v", out)
				}
			},
		},
		{
			name:    "reused_expert_replaces_market_connector",
			request: connectorRequest,
			out: expertResult(
				CapabilityExpertResult{ExpertID: "expert-local", Name: "Custom name", Version: &memberVersion, SkillIDs: []string{}, ConnectorIDs: []string{graphConnectorID}},
				[]CapabilitySkillResult{},
				[]CapabilityConnectorResult{{ConnectorID: graphConnectorID, Name: "Local browser", Version: graphString("9.0")}},
			),
			check: func(t *testing.T, out CapabilityInstallResult) {
				if len(out.Connectors) != 1 || out.Connectors[0].Name != "Local browser" {
					t.Fatalf("local Connector replacement was not preserved: %+v", out.Connectors)
				}
			},
		},
		{
			name:    "reused_expert_deletes_every_connector",
			request: connectorRequest,
			out: expertResult(
				CapabilityExpertResult{ExpertID: "expert-local", Name: "Custom name", Version: &memberVersion, SkillIDs: []string{}},
				[]CapabilitySkillResult{},
				[]CapabilityConnectorResult{},
			),
			check: func(t *testing.T, out CapabilityInstallResult) {
				if len(out.Connectors) != 0 || len(out.Experts[0].ConnectorIDs) != 0 {
					t.Fatalf("deleted local Connectors were invented: %+v", out)
				}
			},
		},
		{
			name:    "reused_team_drops_market_member",
			request: teamRequest,
			out: teamResult(
				[]CapabilityExpertResult{teamLeader(nil, nil)},
				[]CapabilitySkillResult{},
				nil,
			),
			check: func(t *testing.T, out CapabilityInstallResult) {
				if len(out.Experts) != 1 || out.Experts[0].ExpertID != "expert-lead" {
					t.Fatalf("locally removed team member came back: %+v", out.Experts)
				}
			},
		},
		{
			name:    "reused_team_adds_local_member",
			request: teamRequest,
			out: teamResult(
				[]CapabilityExpertResult{
					teamLeader(nil, nil),
					teamWorker(nil, nil),
					{ExpertID: "expert-local-only", Name: "Local helper", SkillIDs: []string{}},
				},
				[]CapabilitySkillResult{},
				nil,
			),
			check: func(t *testing.T, out CapabilityInstallResult) {
				if len(out.Experts) != 3 || out.Experts[2].Name != "Local helper" {
					t.Fatalf("locally added team member was rejected or dropped: %+v", out.Experts)
				}
			},
		},
		{
			name:    "reused_team_experts_share_one_skill",
			request: teamRequest,
			out: teamResult(
				[]CapabilityExpertResult{teamLeader([]string{"skill-shared"}, nil), teamWorker([]string{"skill-shared"}, nil)},
				[]CapabilitySkillResult{{SkillID: "skill-shared", Name: "Shared"}},
				nil,
			),
			check: func(t *testing.T, out CapabilityInstallResult) {
				if len(out.Skills) != 1 || len(out.Experts) != 2 {
					t.Fatalf("shared Skill dependency was not preserved: %+v", out)
				}
			},
		},
		{
			name:    "reused_team_experts_share_one_connector",
			request: teamRequest,
			out: teamResult(
				[]CapabilityExpertResult{teamLeader(nil, []string{graphConnectorID}), teamWorker(nil, []string{graphConnectorID})},
				[]CapabilitySkillResult{},
				[]CapabilityConnectorResult{{ConnectorID: graphConnectorID, Name: "browser", MarketMCPID: graphString("market-browser")}},
			),
			check: func(t *testing.T, out CapabilityInstallResult) {
				if len(out.Connectors) != 1 || len(out.Experts) != 2 {
					t.Fatalf("shared Connector dependency was not preserved: %+v", out)
				}
			},
		},
		{
			name:    "connector_only_install_is_unaffected",
			request: connectorOnlyCapabilityRequest("browser"),
			out: CapabilityInstallResult{
				Type: "connectors",
				Connectors: []CapabilityConnectorResult{{
					ConnectorID: graphConnectorID, Name: "browser", MarketMCPID: graphString("market-browser"), Created: true,
				}},
			},
			check: func(t *testing.T, out CapabilityInstallResult) {
				if out.Type != "connectors" || len(out.Connectors) != 1 {
					t.Fatalf("standalone Connector installation changed: %+v", out)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := installCapabilityGraphFixture(t, tc.request, tc.out)
			if err != nil {
				t.Fatalf("legitimate local graph was rejected: %v", err)
			}
			tc.check(t, out)
		})
	}
}

// TestCapabilityResultRejectsExtraExpertsInSingleExpertRoot pins the root
// shape: an expert result carries exactly the root expert, so a second expert
// cannot be appended to make an otherwise unreferenced resource look bound.
func TestCapabilityResultRejectsExtraExpertsInSingleExpertRoot(t *testing.T) {
	in := capabilityRequest()
	for _, tc := range []struct {
		name   string
		mutate func(*CapabilityInstallResult)
		want   bool
	}{
		{name: "root_only", want: true},
		{
			name: "extra_expert_binds_orphan_skill",
			mutate: func(r *CapabilityInstallResult) {
				r.Experts = append(r.Experts, CapabilityExpertResult{ExpertID: "expert-extra", Name: "Extra", SkillIDs: []string{"skill-orphan"}})
				r.Skills = append(r.Skills, CapabilitySkillResult{SkillID: "skill-orphan", Name: "Orphan"})
			},
		},
		{
			name: "extra_expert_without_bindings",
			mutate: func(r *CapabilityInstallResult) {
				r.Experts = append(r.Experts, CapabilityExpertResult{ExpertID: "expert-extra", Name: "Extra", SkillIDs: []string{}})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := CapabilityInstallResult{
				Type: "expert", ExpertID: "expert-root",
				Experts: []CapabilityExpertResult{{ExpertID: "expert-root", Name: "Custom name", SkillIDs: []string{"skill-root"}}},
				Skills:  []CapabilitySkillResult{{SkillID: "skill-root", Name: "Root skill"}},
			}
			if tc.mutate != nil {
				tc.mutate(&out)
			}
			if got := validCapabilityResult(in.Definition, out, false, false); got != tc.want {
				t.Fatalf("validCapabilityResult=%v want %v", got, tc.want)
			}
		})
	}
}
