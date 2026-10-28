package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type connectorRegressionInstaller struct {
	calls  int
	in     fleet.CapabilityInstallRequest
	result *fleet.CapabilityInstallResult
}

func (f *connectorRegressionInstaller) InstallCapability(_ context.Context, _, _, _, _ string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	f.calls++
	f.in = in
	return f.result, nil
}

func connectorRegressionFixture() (*Service, *fakeStore, *connectorRegressionInstaller) {
	store := installFixture()
	store.plugins["skill-1"].CurrentVersion = stringPtr("1.0.0")
	store.plugins["skill-1"].Package = packageWith(rawAtt("SKILL.md", "# Deploy"), rawAtt("references/checklist.md", "check before deploy"))
	installer := &connectorRegressionInstaller{result: &fleet.CapabilityInstallResult{Type: "expert", ExpertID: "created-expert"}}
	return fixedService(store).WithCapabilityInstaller(installer), store, installer
}

func connectorRegressionPackage(id, rawConfig string) json.RawMessage {
	quotedConfig, _ := json.Marshal(rawConfig)
	return json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","connector":{"type":"mcp","source":"connector.` + id + `"},"attachments":[{"path":"mcp.json","content_type":"raw","mime_type":"application/json","raw_content":` + string(quotedConfig) + `}]}`)
}

func addConnectorRegressionPlugin(store *fakeStore, id, name, rawConfig string, version *string) {
	space := "space-a"
	categoryID := "cat-engineering"
	store.plugins[id] = &model.Plugin{
		ID: id, Name: name, Type: model.PluginTypeConnector, OwnerUID: "user-1", SpaceID: &space,
		Visibility: model.PluginVisibilitySpace, CategoryID: &categoryID, CreatorName: "Market Author",
		Tags: json.RawMessage(`["developer","catalog"]`), CurrentVersion: version,
		Manifest: json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"` + name + `","plugin_type":"connector","name":"` + id + `-slug","description":"Marketplace connector description","labels":["developer","catalog"],"examples":[],"category":"manifest-category-must-not-replace-category-id","documentation":"https://docs.example.test/connector"}`),
		Package:  connectorRegressionPackage(id, rawConfig),
	}
}
func TestWorkspaceConnectorModeImportsVisibleMarketplaceConnectorRelations(t *testing.T) {
	svc, store, installer := connectorRegressionFixture()
	const marketID = "market-repository"
	const rawConfig = `{"mcpServers":{"repository":{"url":"https://mcp.example.test/repository","headers":{"Authorization":"secret"},"providerOptions":{"region":"test"}}},"providerOptions":{"source":"market"}}`
	version := "2.4.1"
	addConnectorRegressionPlugin(store, marketID, "Market Repository", rawConfig, &version)
	store.relations["expert-1"] = append(store.relations["expert-1"], model.PluginRelation{
		ID: "r-market", SourcePluginID: "expert-1", TargetPluginID: marketID,
		TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1,
	})

	p := installationParams()
	p.WorkspaceConnectors = true
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", p); err != nil {
		t.Fatalf("expert installation failed: %v", err)
	}
	definition := installer.in.Definition
	if installer.calls != 1 || len(definition.Connectors) != 1 || len(definition.Experts) != 1 {
		t.Fatalf("market dependency assembly has connectors=%d experts=%d calls=%d", len(definition.Connectors), len(definition.Experts), installer.calls)
	}
	expert := definition.Experts[0]
	connector := definition.Connectors[0]
	if len(expert.MCPConfig) != 0 || !reflect.DeepEqual(expert.ConnectorNames, []string{"Market Repository"}) {
		t.Fatalf("expert dependency was not referenced by connector name: %#v", expert)
	}
	if connector.Name != "Market Repository" || connector.Version == nil || *connector.Version != version || connector.MarketMCPID != marketID ||
		connector.AuthorName == nil || *connector.AuthorName != "Market Author" ||
		connector.Category != "cat-engineering" ||
		!reflect.DeepEqual(connector.Tags, []string{"developer", "catalog"}) {
		t.Fatalf("connector snapshot did not use actual source identity and metadata: %#v", connector)
	}
	assertConnectorConfigJSON(t, connector.Config, rawConfig)
}

func TestWorkspaceConnectorModeSharesOneMarketDependencyAcrossTeamMembers(t *testing.T) {
	svc, store, installer := connectorRegressionFixture()
	const marketID = "market-shared"
	const rawConfig = `{"mcpServers":{"shared":{"command":"shared-mcp","args":["--safe"]}}}`
	addConnectorRegressionPlugin(store, marketID, "Shared Connector", rawConfig, nil)

	bob := *store.plugins["expert-1"]
	bob.ID, bob.Name = "expert-2", "Bob"
	bob.Manifest = json.RawMessage(`{"description":"Bob summary"}`)
	bob.Package = packageWith(rawAtt("AGENTS.md", "Bob instructions"))
	store.plugins[bob.ID] = &bob
	store.relations[bob.ID] = []model.PluginRelation{{
		ID: "r-bob-shared", SourcePluginID: bob.ID, TargetPluginID: marketID,
		TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1,
	}}
	store.relations["expert-1"] = append(store.relations["expert-1"], model.PluginRelation{
		ID: "r-alice-shared", SourcePluginID: "expert-1", TargetPluginID: marketID,
		TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1,
	})
	store.relations["team-1"] = append(store.relations["team-1"], model.PluginRelation{
		ID: "r-team-bob", SourcePluginID: "team-1", TargetPluginID: bob.ID,
		TargetPluginType: model.PluginTypeExpert, Type: "expert_team_expert", Status: 1,
		SortOrder: 1, Data: json.RawMessage(`{"role":"member"}`),
	})

	p := installationParams()
	p.WorkspaceConnectors = true
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team-1", p); err != nil {
		t.Fatalf("team installation failed: %v", err)
	}
	if installer.calls != 1 || len(installer.in.Definition.Connectors) != 1 || len(installer.in.Definition.Experts) != 2 {
		t.Fatalf("shared dependency was not emitted once: connectors=%d experts=%d calls=%d", len(installer.in.Definition.Connectors), len(installer.in.Definition.Experts), installer.calls)
	}
	for _, expert := range installer.in.Definition.Experts {
		if !reflect.DeepEqual(expert.ConnectorNames, []string{"Shared Connector"}) {
			t.Errorf("expert %q did not reference its own shared dependency: %v", expert.Name, expert.ConnectorNames)
		}
	}
	assertConnectorConfigJSON(t, installer.in.Definition.Connectors[0].Config, rawConfig)
	marketReads := 0
	for _, id := range store.getIDs {
		if id == marketID {
			marketReads++
		}
	}
	if marketReads != 1 {
		t.Fatalf("shared connector source was resolved %d times, want one ID-keyed snapshot", marketReads)
	}
}

func TestWorkspaceConnectorCacheChecksEachSourceAudience(t *testing.T) {
	svc, store, installer := connectorRegressionFixture()
	const marketID = "market-private-shared"
	addConnectorRegressionPlugin(store, marketID, "Private Connector", `{"mcpServers":{"server":{"command":"run"}}}`, nil)
	store.plugins[marketID].Visibility = model.PluginVisibilityPrivate

	bob := *store.plugins["expert-1"]
	bob.ID, bob.Name = "expert-2", "Bob"
	bob.Visibility = model.PluginVisibilitySpace
	bob.OwnerUID = "other-owner"
	bob.Package = packageWith(rawAtt("AGENTS.md", "Bob instructions"))
	store.plugins[bob.ID] = &bob
	store.relations[bob.ID] = []model.PluginRelation{{
		ID: "r-bob-private", SourcePluginID: bob.ID, TargetPluginID: marketID,
		TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1,
	}}
	store.relations["expert-1"] = append(store.relations["expert-1"], model.PluginRelation{
		ID: "r-alice-private", SourcePluginID: "expert-1", TargetPluginID: marketID,
		TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1,
	})
	store.relations["team-1"] = append(store.relations["team-1"], model.PluginRelation{
		ID: "r-team-bob-private", SourcePluginID: "team-1", TargetPluginID: bob.ID,
		TargetPluginType: model.PluginTypeExpert, Type: "expert_team_expert", Status: 1,
		SortOrder: 1, Data: json.RawMessage(`{"role":"member"}`),
	})

	p := installationParams()
	p.WorkspaceConnectors = true
	_, err := svc.CreateInstallation(context.Background(), testCaller, "team-1", p)
	if !errors.Is(err, ErrDependencyHidden) || installer.calls != 0 {
		t.Fatalf("connector cache bypassed the second source audience: err=%v calls=%d", err, installer.calls)
	}
	marketReads := 0
	for _, id := range store.getIDs {
		if id == marketID {
			marketReads++
		}
	}
	if marketReads != 1 {
		t.Fatalf("cached connector source was resolved %d times, want one source snapshot", marketReads)
	}
}

func TestWorkspaceConnectorModeRejectsAmbiguousMarketNamesBeforeFleet(t *testing.T) {
	svc, store, installer := connectorRegressionFixture()
	for _, id := range []string{"market-a", "market-b"} {
		addConnectorRegressionPlugin(store, id, "Same Name", `{"mcpServers":{"server":{"command":"run"}}}`, nil)
		store.relations["expert-1"] = append(store.relations["expert-1"], model.PluginRelation{
			ID: "r-" + id, SourcePluginID: "expert-1", TargetPluginID: id,
			TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1,
		})
	}
	p := installationParams()
	p.WorkspaceConnectors = true
	_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", p)
	var invalid *InstallationValidationError
	if !errors.As(err, &invalid) || installer.calls != 0 {
		t.Fatalf("ambiguous connector names were not rejected during preparation: err=%v calls=%d", err, installer.calls)
	}
}

func TestWorkspaceConnectorModeRejectsHiddenMarketDependencyBeforeFleet(t *testing.T) {
	svc, store, installer := connectorRegressionFixture()
	addConnectorRegressionPlugin(store, "market-hidden", "Hidden Connector", `{"mcpServers":{"server":{"command":"run"}}}`, nil)
	store.relations["expert-1"] = append(store.relations["expert-1"], model.PluginRelation{
		ID: "r-hidden", SourcePluginID: "expert-1", TargetPluginID: "market-hidden",
		TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", Status: 1,
	})
	store.declaredCounts = map[string]int{"expert-1": len(store.relations["expert-1"]) + 1}
	p := installationParams()
	p.WorkspaceConnectors = true
	_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", p)
	if !errors.Is(err, ErrDependencyHidden) || installer.calls != 0 {
		t.Fatalf("hidden market dependency was not refused before Fleet: err=%v calls=%d", err, installer.calls)
	}
}

func TestWorkspaceConnectorModeStillRequiresRuntimeForExperts(t *testing.T) {
	svc, _, installer := connectorRegressionFixture()
	p := installationParams()
	p.RuntimeID = ""
	p.WorkspaceConnectors = true
	_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", p)
	var invalid *InstallationValidationError
	if !errors.As(err, &invalid) || invalid.Field != "runtime_id" || installer.calls != 0 {
		t.Fatalf("workspace connector expert without a runtime was not rejected before Fleet: err=%v calls=%d", err, installer.calls)
	}
}

func assertConnectorConfigJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if json.Unmarshal(got, &gotValue) != nil || json.Unmarshal([]byte(want), &wantValue) != nil || !reflect.DeepEqual(gotValue, wantValue) {
		t.Error("connector configuration did not preserve the expected JSON fields")
	}
}

func TestWorkspaceConnectorRootDoesNotRequireRuntimeAndPreservesMarketSnapshot(t *testing.T) {
	svc, store, installer := connectorRegressionFixture()
	const pluginID = "market-repository"
	const rawConfig = `{"mcpServers":{"repository":{"url":"https://mcp.example.test/repository","headers":{"X-Trace":"catalog"},"providerOptions":{"region":"test"}}},"deploymentOptions":{"zone":"test"}}`
	addConnectorRegressionPlugin(store, pluginID, "Market Repository", rawConfig, nil)
	installer.result = &fleet.CapabilityInstallResult{
		Type:       "connectors",
		Connectors: []fleet.CapabilityConnectorResult{{ConnectorID: "workspace-connector-9", Name: "Workspace Repository", MarketMCPID: stringPtr(pluginID), Created: true}},
	}

	p := installationParams()
	p.RuntimeID = ""
	p.CustomEnv = nil
	p.ResourceName = "Workspace Repository"
	p.WorkspaceConnectors = true
	out, err := svc.CreateInstallation(context.Background(), testCaller, pluginID, p)
	if err != nil {
		t.Fatalf("connector installation failed: %v", err)
	}
	if installer.calls != 1 {
		t.Fatalf("Fleet calls = %d, want one", installer.calls)
	}
	definition := installer.in.Definition
	if len(definition.Connectors) != 1 || len(definition.Experts) != 0 || definition.ExpertTeam != nil || len(installer.in.Bindings.Experts) != 0 {
		t.Fatalf("connector-only request unexpectedly created or bound an expert: connectors=%d experts=%d bindings=%d", len(definition.Connectors), len(definition.Experts), len(installer.in.Bindings.Experts))
	}
	got := definition.Connectors[0]
	if got.Name != "Workspace Repository" || got.Description != "Marketplace connector description" || got.AuthorName == nil || *got.AuthorName != "Market Author" || got.Category != "cat-engineering" || got.Documentation != "https://docs.example.test/connector" || !reflect.DeepEqual(got.Tags, []string{"developer", "catalog"}) {
		t.Error("connector snapshot did not retain the source record's display metadata")
	}
	if got.Version != nil || got.MarketMCPID != pluginID {
		t.Error("connector snapshot did not preserve the nullable version and actual market identity")
	}
	if got.Slug != "" {
		t.Error("source display name must not become a service identifier when no service identifier is recorded")
	}
	assertConnectorConfigJSON(t, got.Config, rawConfig)
	if out == nil || out.AgentID != "" || out.SquadID != "" || len(out.Connectors) != 1 || out.Connectors[0].ConnectorID != "workspace-connector-9" || out.Connectors[0].Name != "Workspace Repository" || out.Connectors[0].Version != nil || out.Connectors[0].MarketMCPID == nil || *out.Connectors[0].MarketMCPID != pluginID || !out.Connectors[0].Created {
		t.Error("connector-only result did not report the installed connector identity")
	}
}

func TestWorkspaceConnectorModeKeepsLegacyExpertRuntimeRequirement(t *testing.T) {
	svc, _, installer := connectorRegressionFixture()
	p := installationParams()
	p.RuntimeID = ""
	_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", p)
	var invalid *InstallationValidationError
	if !errors.As(err, &invalid) || invalid.Field != "runtime_id" || installer.calls != 0 {
		t.Fatalf("legacy expert without a runtime was not rejected before Fleet: err=%v calls=%d", err, installer.calls)
	}
}

func TestWorkspaceConnectorModeUsesEachTeamMemberMCPAsItsOwnDependency(t *testing.T) {
	svc, store, installer := connectorRegressionFixture()
	const aliceConfig = `{"mcpServers":{"alice_docs":{"url":"https://mcp.example.test/alice","headers":{"X-Trace":"alice"},"providerOptions":{"mode":"read"}}},"providerOptions":{"workspace":"alice"}}`
	const bobConfig = `{"mcpServers":{"bob_search":{"command":"search-mcp","args":["--local"],"env":{"PROFILE":"bob"},"providerOptions":{"mode":"safe"}}}}`
	store.plugins["expert-1"].Package = packageWith(rawAtt("AGENTS.md", "Alice instructions"), rawAtt("mcp.json", aliceConfig))
	bob := *store.plugins["expert-1"]
	bob.ID, bob.Name = "expert-2", "Bob"
	bob.Manifest = json.RawMessage(`{"description":"Bob summary"}`)
	bob.Package = packageWith(rawAtt("AGENTS.md", "Bob instructions"), rawAtt("mcp.json", bobConfig))
	store.plugins[bob.ID] = &bob
	store.relations[bob.ID] = nil
	store.relations["team-1"] = append(store.relations["team-1"], model.PluginRelation{ID: "r-bob", SourcePluginID: "team-1", TargetPluginID: bob.ID, TargetPluginType: model.PluginTypeExpert, Type: "expert_team_expert", Status: 1, SortOrder: 1, Data: json.RawMessage(`{"role":"member"}`)})

	p := installationParams()
	p.WorkspaceConnectors = true
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "team-1", p); err != nil {
		t.Fatalf("team installation failed: %v", err)
	}
	if installer.calls != 1 {
		t.Fatalf("Fleet calls = %d, want one", installer.calls)
	}
	definition := installer.in.Definition
	if len(definition.Connectors) != 2 || len(definition.Experts) != 2 || len(installer.in.Bindings.Experts) != 2 {
		t.Fatalf("team dependency assembly has connectors=%d experts=%d bindings=%d", len(definition.Connectors), len(definition.Experts), len(installer.in.Bindings.Experts))
	}
	experts := make(map[string]fleet.CapabilityExpert, len(definition.Experts))
	for _, expert := range definition.Experts {
		experts[expert.Name] = expert
		if len(expert.MCPConfig) != 0 {
			t.Errorf("member %q contains a duplicate legacy MCP configuration", expert.Name)
		}
	}
	connectors := make(map[string]fleet.CapabilityConnector, len(definition.Connectors))
	for _, connector := range definition.Connectors {
		connectors[connector.Name] = connector
	}
	if len(connectors) != 2 {
		t.Error("team dependency connectors are missing or duplicated")
	}
	for name, config := range map[string]string{"Alice": aliceConfig, "Bob": bobConfig} {
		expert, ok := experts[name]
		if !ok || !reflect.DeepEqual(expert.ConnectorNames, []string{name}) {
			t.Errorf("member %q does not reference only its own connector", name)
		}
		connector, ok := connectors[name]
		if !ok {
			t.Errorf("member %q connector dependency is missing", name)
			continue
		}
		assertConnectorConfigJSON(t, connector.Config, config)
		if connector.Version != nil || connector.MarketMCPID != "" || connector.AuthorName != nil {
			t.Errorf("embedded MCP for %q fabricated marketplace identity", name)
		}
	}
}

func TestWorkspaceConnectorModeNamesEmbeddedDependencyAfterInstalledExpert(t *testing.T) {
	svc, store, installer := connectorRegressionFixture()
	const rawConfig = `{"mcpServers":{"local_tools":{"type":"stdio","providerOptions":{"mode":"safe","flags":[1,{"opaque":true}]}}},"rootOptions":{"provider":{"enabled":true}}}`
	store.plugins["expert-1"].Package = packageWith(rawAtt("AGENTS.md", "work"), rawAtt("mcp.json", rawConfig))
	p := installationParams()
	p.ResourceName = "Custom name"
	p.WorkspaceConnectors = true
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", p); err != nil {
		t.Fatalf("expert installation failed: %v", err)
	}
	definition := installer.in.Definition
	if len(definition.Experts) != 1 || definition.Experts[0].Name != "Custom name" || len(definition.Experts[0].MCPConfig) != 0 || !reflect.DeepEqual(definition.Experts[0].ConnectorNames, []string{"Custom name"}) || len(definition.Connectors) != 1 {
		t.Error("embedded MCP did not follow the installed expert name into the connector binding")
		return
	}
	connector := definition.Connectors[0]
	if connector.Name != "Custom name" || connector.Version != nil || connector.MarketMCPID != "" || connector.AuthorName != nil {
		t.Error("embedded MCP dependency fabricated source metadata or used another name")
	}
	assertConnectorConfigJSON(t, connector.Config, rawConfig)
}

func TestWorkspaceConnectorModeAllowsEmptyEmbeddedMCPWithoutDependency(t *testing.T) {
	svc, store, installer := connectorRegressionFixture()
	store.plugins["expert-1"].Package = packageWith(rawAtt("AGENTS.md", "work"), rawAtt("mcp.json", `{"mcpServers":{}}`))
	p := installationParams()
	p.WorkspaceConnectors = true
	if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", p); err != nil {
		t.Fatalf("empty embedded MCP should not block installation: %v", err)
	}
	definition := installer.in.Definition
	if installer.calls != 1 || len(definition.Connectors) != 0 || len(definition.Experts) != 1 || len(definition.Experts[0].ConnectorNames) != 0 || len(definition.Experts[0].MCPConfig) != 0 {
		t.Fatalf("empty embedded MCP created an unexpected dependency: calls=%d connectors=%d", installer.calls, len(definition.Connectors))
	}
}

func TestWorkspaceConnectorModeRejectsEmptyStandaloneConnectorBeforeFleet(t *testing.T) {
	svc, store, installer := connectorRegressionFixture()
	const pluginID = "empty-market-connector"
	addConnectorRegressionPlugin(store, pluginID, "Empty Connector", `{"mcpServers":{}}`, nil)
	p := installationParams()
	p.RuntimeID = ""
	p.CustomEnv = nil
	p.WorkspaceConnectors = true
	if _, err := svc.CreateInstallation(context.Background(), testCaller, pluginID, p); err == nil || installer.calls != 0 {
		t.Fatalf("empty standalone connector must fail before Fleet: err=%v calls=%d", err, installer.calls)
	}
}

func TestWorkspaceConnectorModeRejectsConnectorRootCustomEnv(t *testing.T) {
	svc, store, installer := connectorRegressionFixture()
	const pluginID = "market-with-env"
	addConnectorRegressionPlugin(store, pluginID, "Connector", `{"mcpServers":{"server":{"command":"run"}}}`, nil)
	p := installationParams()
	p.RuntimeID = ""
	p.WorkspaceConnectors = true
	if _, err := svc.CreateInstallation(context.Background(), testCaller, pluginID, p); err == nil || installer.calls != 0 {
		t.Fatalf("connector root accepted expert custom_env: err=%v calls=%d", err, installer.calls)
	}
}

func TestWorkspaceConnectorModeFalseKeepsLegacyMCPAssembly(t *testing.T) {
	const rawConfig = `{"mcpServers":{"legacy":{"command":"legacy-mcp","args":["--mode","read"]}}}`
	for _, tc := range []struct {
		name            string
		explicitlyFalse bool
	}{{name: "omitted"}, {name: "false", explicitlyFalse: true}} {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, installer := connectorRegressionFixture()
			store.plugins["expert-1"].Package = packageWith(rawAtt("AGENTS.md", "work"), rawAtt("mcp.json", rawConfig))
			p := installationParams()
			if tc.explicitlyFalse {
				p.WorkspaceConnectors = false
			}
			out, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", p)
			if err != nil {
				t.Fatalf("legacy install failed: %v", err)
			}
			if out == nil || out.Connectors != nil {
				t.Error("legacy install unexpectedly returned new connector results")
			}
			definition := installer.in.Definition
			if len(definition.Connectors) != 0 || len(definition.Experts) != 1 || len(definition.Experts[0].ConnectorNames) != 0 || string(definition.Experts[0].MCPConfig) != rawConfig {
				t.Error("legacy MCP was changed or duplicated as a Workspace connector")
			}
			if len(installer.in.Bindings.Experts) != 1 || installer.in.Bindings.Experts[0].RuntimeID != installationParams().RuntimeID {
				t.Error("legacy runtime binding changed")
			}
		})
	}
}

func TestWorkspaceConnectorModeFalseKeepsStrictLegacyMCPValidation(t *testing.T) {
	for _, tc := range []struct {
		name            string
		explicitlyFalse bool
	}{{name: "omitted"}, {name: "false", explicitlyFalse: true}} {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, installer := connectorRegressionFixture()
			store.plugins["expert-1"].Package = packageWith(rawAtt("AGENTS.md", "work"), rawAtt("mcp.json", `{"mcpServers":{"legacy":{"command":"run","type":"stdio","providerOptions":{"mode":"safe"}}},"rootOptions":{"opaque":true}}`))
			p := installationParams()
			if tc.explicitlyFalse {
				p.WorkspaceConnectors = false
			}
			_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", p)
			var invalid *InstallationValidationError
			if !errors.As(err, &invalid) || invalid.Field != "definition.experts.mcp_config" || installer.calls != 0 {
				t.Fatalf("legacy MCP validation was relaxed: err=%v calls=%d", err, installer.calls)
			}
		})
	}
}

func TestWorkspaceConnectorPreparationAndVisibilityFailuresPrecedeFleet(t *testing.T) {
	t.Run("invalid_embedded_config", func(t *testing.T) {
		svc, store, installer := connectorRegressionFixture()
		store.plugins["expert-1"].Package = packageWith(rawAtt("AGENTS.md", "work"), rawAtt("mcp.json", `{"mcpServers":{"broken":[]}}`))
		p := installationParams()
		p.WorkspaceConnectors = true
		if _, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", p); err == nil || installer.calls != 0 {
			t.Fatalf("invalid dependency config reached Fleet: err=%v calls=%d", err, installer.calls)
		}
	})
	t.Run("hidden_dependency", func(t *testing.T) {
		svc, store, installer := connectorRegressionFixture()
		store.declaredCounts = map[string]int{"expert-1": 2}
		p := installationParams()
		p.WorkspaceConnectors = true
		_, err := svc.CreateInstallation(context.Background(), testCaller, "expert-1", p)
		if !errors.Is(err, ErrDependencyHidden) || installer.calls != 0 {
			t.Fatalf("hidden dependency was not rejected before Fleet: err=%v calls=%d", err, installer.calls)
		}
	})
}
