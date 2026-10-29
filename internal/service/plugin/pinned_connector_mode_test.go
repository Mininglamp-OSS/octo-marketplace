package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

type pinnedConnectorModeInstaller struct {
	calls  int
	keys   []string
	wires  [][]byte
	result *fleet.CapabilityInstallResult
}

func (f *pinnedConnectorModeInstaller) InstallCapability(_ context.Context, _, _, _, key string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	f.calls++
	f.keys = append(f.keys, key)
	wire, _ := json.Marshal(in)
	f.wires = append(f.wires, wire)
	return f.result, nil
}

func setPinnedMarketConnector(f *pinnedFixtureStore, name, rawConfig string) {
	connector := f.plugins["connector"]
	connector.Name = name
	connector.CreatorName = "Marketplace Connector Author"
	connector.CategoryID = pin("cat-integrations")
	connector.CurrentVersion = pin("4.7.2")
	connector.Tags = json.RawMessage(`["mcp","workspace","shared"]`)
	connector.Manifest = json.RawMessage(`{"plugin_name":"` + name + `","description":"Marketplace connector description","documentation":"https://docs.example.test/connector"}`)
	connector.Package = packageWith(rawAtt("mcp.json", rawConfig))
}

func pinnedConnectorExtensionFixture() string {
	return `{"mcpServers":{"shared":{"url":"https://mcp.example.test/shared","headers":{"X-Trace":"keep-me"},"providerOptions":{"region":"west","features":["search",{"experimental":true}]},"transportOptions":{"retry":{"maxAttempts":3,"backoff":"linear"}}}},"extensions":{"vendor":{"version":2,"flags":{"streaming":true,"tools":["search","fetch"]}},"unknownField":{"nested":[1,"two",{"preserve":true}]}}}`
}

func TestPinnedWorkspaceConnectorModeSharesOneMarketConnectorAcrossTeamMembers(t *testing.T) {
	s, f, _ := fixturePinned()
	config := pinnedConnectorExtensionFixture()
	setPinnedMarketConnector(f, "Shared Connector", config)
	result := &fleet.CapabilityInstallResult{
		Type:         "expert_team",
		ExpertTeamID: "created-team",
		Connectors: []fleet.CapabilityConnectorResult{{
			ConnectorID: "workspace-shared-connector",
			Name:        "Shared Connector",
			Version:     pin("4.7.2"),
			MarketMCPID: pin("connector"),
			Created:     true,
		}},
	}
	installer := &pinnedConnectorModeInstaller{result: result}
	s.WithCapabilityInstaller(installer)

	p := installationParams()
	p.WorkspaceConnectors = true
	out, err := s.CreateInstallation(context.Background(), testCaller, "team", p)
	if err != nil {
		t.Fatalf("pinned team install failed: %#v", err)
	}
	definition := installerDefinition(t, installer)
	if installer.calls != 1 || len(definition.Connectors) != 1 || len(definition.Experts) != 2 || definition.ExpertTeam == nil {
		t.Fatalf("shared pinned dependency was not emitted once: calls=%d connectors=%d experts=%d team=%v", installer.calls, len(definition.Connectors), len(definition.Experts), definition.ExpertTeam)
	}
	connector := definition.Connectors[0]
	if connector.Name != "Shared Connector" || connector.Description != "Marketplace connector description" || connector.Documentation != "https://docs.example.test/connector" || connector.AuthorName == nil || *connector.AuthorName != "Marketplace Connector Author" || connector.Category != "cat-integrations" || !reflect.DeepEqual(connector.Tags, []string{"mcp", "workspace", "shared"}) || connector.Version == nil || *connector.Version != "4.7.2" || connector.MarketMCPID != "connector" {
		t.Fatalf("market connector source metadata was not preserved: %#v", connector)
	}
	assertConnectorConfigJSON(t, connector.Config, config)
	for _, expert := range definition.Experts {
		if !reflect.DeepEqual(expert.ConnectorNames, []string{"Shared Connector"}) {
			t.Errorf("expert %q did not reference the shared connector: %v", expert.Name, expert.ConnectorNames)
		}
		if len(expert.MCPConfig) != 0 {
			t.Errorf("expert %q also received a legacy inline MCP config: %s", expert.Name, expert.MCPConfig)
		}
		if expert.CustomEnv["OCTOBUDDY_PROVIDER_ID"] != "provider-current" {
			t.Errorf("expert %q lost custom environment binding: %#v", expert.Name, expert.CustomEnv)
		}
	}
	if out == nil || !reflect.DeepEqual(out.Connectors, result.Connectors) {
		t.Fatalf("installation outcome did not return connector metadata: %#v", out)
	}
}

func TestPinnedWorkspaceConnectorModeReplaysConnectorResultWithSameKey(t *testing.T) {
	s, f, _ := fixturePinned()
	setPinnedMarketConnector(f, "Shared Connector", pinnedConnectorExtensionFixture())
	result := &fleet.CapabilityInstallResult{
		Type:         "expert_team",
		ExpertTeamID: "created-team",
		Connectors: []fleet.CapabilityConnectorResult{{
			ConnectorID: "workspace-shared-connector",
			Name:        "Shared Connector",
			Version:     pin("4.7.2"),
			MarketMCPID: pin("connector"),
			Created:     true,
		}},
	}
	installer := &pinnedConnectorModeInstaller{result: result}
	s.WithCapabilityInstaller(installer)
	p := installationParams()
	p.WorkspaceConnectors = true

	first, err := s.CreateInstallation(context.Background(), testCaller, "team", p)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateInstallation(context.Background(), testCaller, "team", p)
	if err != nil {
		t.Fatalf("same-key pinned retry failed: %v", err)
	}
	if installer.calls != 2 || len(installer.keys) != 2 || installer.keys[0] == "" || installer.keys[0] != installer.keys[1] || !reflect.DeepEqual(installer.wires[0], installer.wires[1]) {
		t.Fatalf("same-key retry changed the request: calls=%d keys=%v", installer.calls, installer.keys)
	}
	if first == nil || second == nil || second.AgentID != "" || second.SquadID != "created-team" || !second.Replayed || !reflect.DeepEqual(second.Connectors, result.Connectors) {
		t.Fatalf("replayed result lost workspace connector metadata: first=%#v second=%#v", first, second)
	}
}

func TestPinnedWorkspaceConnectorModeAllowsConnectorOnlyWithoutRuntime(t *testing.T) {
	s, f, _ := fixturePinned()
	const pluginID = "root-market-connector"
	space := testCaller.SpaceID
	config := pinnedConnectorExtensionFixture()
	f.plugins[pluginID] = &model.Plugin{
		ID: pluginID, Name: "Standalone Connector", Type: model.PluginTypeConnector,
		OwnerUID: testCaller.UID, SpaceID: &space, Visibility: model.PluginVisibilityPrivate,
		ListingState: model.PluginListingStatePublished, Status: 1,
		Tags:     json.RawMessage(`[]`),
		Manifest: json.RawMessage(`{"plugin_name":"Standalone Connector","description":"Connector-only fixture","documentation":"https://docs.example.test/standalone"}`),
		Package:  packageWith(rawAtt("mcp.json", config)),
	}
	result := &fleet.CapabilityInstallResult{
		Type: "connectors",
		Connectors: []fleet.CapabilityConnectorResult{{
			ConnectorID: "workspace-standalone-connector",
			Name:        "Standalone Connector",
			MarketMCPID: pin(pluginID),
			Created:     true,
		}},
	}
	installer := &pinnedConnectorModeInstaller{result: result}
	s.WithCapabilityInstaller(installer)
	p := installationParams()
	p.RuntimeID = ""
	p.CustomEnv = nil
	p.ResourceName = "Standalone Connector"
	p.WorkspaceConnectors = true

	out, err := s.CreateInstallation(context.Background(), testCaller, pluginID, p)
	if err != nil {
		t.Fatalf("connector-only install without runtime failed: %#v", err)
	}
	definition := installerDefinition(t, installer)
	if installer.calls != 1 || len(definition.Experts) != 0 || len(definition.Connectors) != 1 || len(installer.inBindings().Experts) != 0 {
		t.Fatalf("connector-only install unexpectedly required or created an expert: calls=%d", installer.calls)
	}
	assertConnectorConfigJSON(t, definition.Connectors[0].Config, config)
	if out == nil || out.AgentID != "" || out.SquadID != "" || !reflect.DeepEqual(out.Connectors, result.Connectors) {
		t.Fatalf("connector-only result did not return installed connector identity: %#v", out)
	}
	foundAttempt := false
	for _, attempt := range f.attempts {
		if attempt.PluginID != pluginID {
			continue
		}
		foundAttempt = true
		if attempt.RootVersionID != "" {
			t.Fatalf("current connector root fabricated a release version %q", attempt.RootVersionID)
		}
		var plan pinnedInstallationPlan
		if err := json.Unmarshal(attempt.FrozenPlan, &plan); err != nil {
			t.Fatalf("decode connector-only frozen plan: %v", err)
		}
		if plan.RootVersionID != "" || !plan.ConnectorOnly || !reflect.DeepEqual(plan.NodeIDs, []string{pluginID}) || len(plan.Edges) != 0 {
			t.Fatalf("connector-only eligibility evidence is incomplete: %+v", plan)
		}
		if plan.EligibilityHMAC == "" {
			t.Fatal("connector-only frozen plan was not eligibility-signed")
		}
	}
	if !foundAttempt {
		t.Fatal("connector-only attempt was not persisted")
	}
}

func TestPinnedWorkspaceConnectorModeRejectsConnectorOnlyCustomEnv(t *testing.T) {
	s, f, _ := fixturePinned()
	const pluginID = "root-market-connector"
	space := testCaller.SpaceID
	f.plugins[pluginID] = &model.Plugin{
		ID: pluginID, Name: "Standalone Connector", Type: model.PluginTypeConnector,
		OwnerUID: testCaller.UID, SpaceID: &space, Visibility: model.PluginVisibilityPrivate,
		ListingState: model.PluginListingStatePublished, Status: 1,
		Tags:     json.RawMessage(`[]`),
		Manifest: json.RawMessage(`{"plugin_name":"Standalone Connector","description":"Connector-only fixture","documentation":"https://docs.example.test/standalone"}`),
		Package:  packageWith(rawAtt("mcp.json", `{"mcpServers":{"server":{"command":"run"}}}`)),
	}
	installer := &pinnedConnectorModeInstaller{result: &fleet.CapabilityInstallResult{Type: "connectors"}}
	s.WithCapabilityInstaller(installer)
	p := installationParams()
	p.RuntimeID = ""
	p.WorkspaceConnectors = true

	_, err := s.CreateInstallation(context.Background(), testCaller, pluginID, p)
	var validation *InstallationValidationError
	if !errors.As(err, &validation) || validation.Field != "custom_env" || validation.Reason != "unsupported_for_connector" || installer.calls != 0 {
		t.Fatalf("connector-only root accepted custom_env or reached Fleet: err=%v calls=%d", err, installer.calls)
	}
}

func TestPinnedWorkspaceConnectorModeStillRequiresRuntimeForExpertsAndTeams(t *testing.T) {
	for _, id := range []string{"expert-a", "team"} {
		t.Run(id, func(t *testing.T) {
			s, f, _ := fixturePinned()
			setPinnedMarketConnector(f, "Shared Connector", `{"mcpServers":{"shared":{"command":"run"}}}`)
			if id == "expert-a" {
				f.state.PublishedVersionID = pin("expert-v1")
			}
			installer := &pinnedConnectorModeInstaller{result: &fleet.CapabilityInstallResult{}}
			s.WithCapabilityInstaller(installer)
			p := installationParams()
			p.RuntimeID = ""
			p.WorkspaceConnectors = true
			_, err := s.CreateInstallation(context.Background(), testCaller, id, p)
			var validation *InstallationValidationError
			if !errors.As(err, &validation) || validation.Field != "runtime_id" || installer.calls != 0 {
				t.Fatalf("expert/team without runtime was not rejected before Fleet: err=%v calls=%d", err, installer.calls)
			}
		})
	}
}

func TestPinnedWorkspaceConnectorModeKeepsLegacyPathWhenOmittedOrFalse(t *testing.T) {
	for _, tc := range []struct {
		name     string
		explicit bool
	}{
		{name: "omitted"},
		{name: "false", explicit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := fixturePinned()
			f.plugins["connector"].Package = packageWith(rawAtt("mcp.json", `{"mcpServers":{"legacy":{"command":"current-config"}}}`))
			installer := &pinnedConnectorModeInstaller{result: &fleet.CapabilityInstallResult{Type: "expert_team", ExpertTeamID: "created-team"}}
			s.WithCapabilityInstaller(installer)
			p := installationParams()
			if tc.explicit {
				p.WorkspaceConnectors = false
			}
			if _, err := s.CreateInstallation(context.Background(), testCaller, "team", p); err != nil {
				t.Fatalf("legacy pinned mode failed: %v", err)
			}
			definition := installerDefinition(t, installer)
			if len(definition.Connectors) != 0 || len(definition.Experts) != 2 {
				t.Fatalf("legacy pinned mode emitted structured connectors: %+v", definition)
			}
			for _, expert := range definition.Experts {
				if len(expert.ConnectorNames) != 0 || !strings.Contains(string(expert.MCPConfig), "current-config") {
					t.Errorf("legacy pinned connector config was not kept inline for %q: %+v", expert.Name, expert)
				}
			}
		})
	}
}

func TestPinnedWorkspaceConnectorModePreservesInlineSnapshotRejection(t *testing.T) {
	s, f, _ := fixturePinned()
	root := f.versions[pluginrepo.InstallVersionKey{PluginID: "team", VersionID: "root-v1"}]
	root.Package = packageWith(rawAtt("AGENTS.md", "team instructions"), rawAtt("mcp.json", `{"mcpServers":{"inline":{"command":"must-not-install"}}}`))
	installer := &pinnedConnectorModeInstaller{result: &fleet.CapabilityInstallResult{}}
	s.WithCapabilityInstaller(installer)
	p := installationParams()
	p.WorkspaceConnectors = true

	_, err := s.CreateInstallation(context.Background(), testCaller, "team", p)
	var validation *InstallationValidationError
	if !errors.As(err, &validation) || validation.Reason != "unsupported_inline_capabilities" || installer.calls != 0 {
		t.Fatalf("workspace connector mode bypassed inline snapshot rejection: err=%v calls=%d", err, installer.calls)
	}
}

func TestPinnedWorkspaceConnectorModePreservesConnectorSourceAccessBoundary(t *testing.T) {
	s, f, _ := fixturePinned()
	f.plugins["expert-a"].Visibility = model.PluginVisibilitySpace
	f.plugins["connector"].OwnerUID = "another-owner"
	installer := &pinnedConnectorModeInstaller{result: &fleet.CapabilityInstallResult{Type: "expert_team"}}
	s.WithCapabilityInstaller(installer)
	p := installationParams()
	p.WorkspaceConnectors = true

	_, err := s.CreateInstallation(context.Background(), testCaller, "team", p)
	if !errors.Is(err, ErrDependencyHidden) || installer.calls != 0 {
		t.Fatalf("inaccessible connector source was included in pinned install: err=%v calls=%d", err, installer.calls)
	}
}

func installerDefinition(t *testing.T, installer *pinnedConnectorModeInstaller) fleet.CapabilityDefinition {
	t.Helper()
	if len(installer.wires) == 0 {
		t.Fatal("installer did not receive a request")
	}
	var request fleet.CapabilityInstallRequest
	if err := json.Unmarshal(installer.wires[len(installer.wires)-1], &request); err != nil {
		t.Fatalf("decode captured installer request: %v", err)
	}
	return request.Definition
}

func (f *pinnedConnectorModeInstaller) inBindings() fleet.CapabilityBindings {
	if len(f.wires) == 0 {
		return fleet.CapabilityBindings{}
	}
	var request fleet.CapabilityInstallRequest
	_ = json.Unmarshal(f.wires[len(f.wires)-1], &request)
	return request.Bindings
}
