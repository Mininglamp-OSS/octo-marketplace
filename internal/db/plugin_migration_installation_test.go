package db

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

type migratedGraphInstaller struct {
	request fleet.CapabilityInstallRequest
	calls   int
}

func TestMigratedSameExpertMCPAliasesFeedBothInstallationModesMySQL(t *testing.T) {
	database := reviewDB(t)
	seedMigrationRoot(t, database, "alias-team", "private", "published")
	manifest := `{"$schema":"cowork-plugin-manifest-2.0.json","plugin_type":"connector","plugin_name":"extra-connector","name":"extra-connector","description":"fixture","labels":[],"examples":[]}`
	pkg := `{"$schema":"cowork-plugin-package-2.0.json","connector":{"type":"mcp","source":"fixture"},"attachments":[{"path":"mcp.json","mime_type":"application/json","content_type":"raw","raw_content":"{\"mcpServers\":{\"search\":{\"command\":\"other-search\"}}}"},{"path":"scripts/client.py","mime_type":"text/plain","content_type":"raw","raw_content":"retained support"}]}`
	seed(t, database, seedPlugin{id: "extra-connector", typ: "connector", visibility: "private", listingState: "published", manifest: manifest, pkg: pkg})
	docs, err := pluginsvc.CanonicalizeMigratedDocuments("extra-connector", model.PluginTypeConnector, json.RawMessage(`[]`), json.RawMessage(manifest), json.RawMessage(pkg), "space-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE plugins SET manifest_hash=?,plugin_hash=? WHERE plugin_id='extra-connector'`, docs.ManifestHash, docs.PluginHash); err != nil {
		t.Fatal(err)
	}
	seedRelation(t, database, "extra-connector-edge", "alias-team-expert", "extra-connector", "expert_connector")
	applyCurrentMigration(t, database)
	for _, workspace := range []bool{false, true} {
		remote := &migratedGraphInstaller{}
		svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
		key := "alias-default-attempt"
		if workspace {
			key = "alias-workspace-attempt"
		}
		_, err := svc.CreateInstallation(context.Background(), managedOwner(), "alias-team", pluginsvc.InstallationParams{WorkspaceID: "fixture-workspace", RuntimeID: "7f63db70-6b66-4c2d-a52f-fb6de1d78ad9", IdempotencyKey: key, Token: "fixture-token", WorkspaceConnectors: workspace})
		if err != nil || remote.calls != 1 {
			t.Fatalf("aliased install workspace=%v err=%v calls=%d", workspace, err, remote.calls)
		}
		configs := []json.RawMessage{remote.request.Definition.Experts[0].MCPConfig}
		if workspace {
			configs = nil
			for _, connector := range remote.request.Definition.Connectors {
				configs = append(configs, connector.Config)
			}
		}
		commands := map[string]bool{}
		for _, config := range configs {
			var doc struct {
				Servers map[string]struct {
					Command string `json:"command"`
				} `json:"mcpServers"`
			}
			if json.Unmarshal(config, &doc) != nil {
				t.Fatal("invalid captured config")
			}
			for alias, server := range doc.Servers {
				if alias == "search" {
					t.Fatal("conflicting original key was not aliased")
				}
				commands[server.Command] = true
			}
		}
		if len(commands) != 2 || !commands["search"] || !commands["other-search"] {
			t.Fatal("one of the independent MCP configurations was lost")
		}
	}
}

// A team may legitimately have two members using the same MCP server key:
// default mode merges per Expert, while Workspace mode needs distinct names.
func TestMigratedSameKeyTeamFeedsBothInstallationModesMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedMigrationRoot(t, database, "mode-team", "private", "published")
	seedMigrationRoot(t, database, "other-team", "private", "published")
	var manifest, raw []byte
	if err := database.QueryRow(`SELECT manifest_json,plugin_json FROM plugins WHERE plugin_id='other-team-expert'`).Scan(&manifest, &raw); err != nil {
		t.Fatal(err)
	}
	var pkg map[string]any
	if json.Unmarshal(raw, &pkg) != nil {
		t.Fatal("invalid fixture package")
	}
	for _, value := range pkg["attachments"].([]any) {
		a := value.(map[string]any)
		if a["path"] == "mcp.json" {
			a["raw_content"] = `{"mcpServers":{"search":{"command":"other-search"}}}`
		}
	}
	raw, _ = json.Marshal(pkg)
	hash, err := libplugin.ComputePluginHash(manifest, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE plugins SET plugin_json=?,plugin_hash=? WHERE plugin_id='other-team-expert'`, string(raw), hash); err != nil {
		t.Fatal(err)
	}
	seedRelation(t, database, "other-member", "mode-team", "other-team-expert", "expert_team_expert")
	if _, err := database.Exec(`UPDATE plugin_relations SET relation_json=JSON_OBJECT('member_key','other','role','worker','is_leader',false),sort_order=8 WHERE relation_id='other-member'`); err != nil {
		t.Fatal(err)
	}
	applyCurrentMigration(t, database)
	for _, workspace := range []bool{false, true} {
		remote := &migratedGraphInstaller{}
		svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
		key := "default-mode-attempt"
		if workspace {
			key = "workspace-mode-attempt"
		}
		_, err := svc.CreateInstallation(ctx, managedOwner(), "mode-team", pluginsvc.InstallationParams{WorkspaceID: "fixture-workspace", RuntimeID: "7f63db70-6b66-4c2d-a52f-fb6de1d78ad9", IdempotencyKey: key, Token: "fixture-token", WorkspaceConnectors: workspace})
		if err != nil || remote.calls != 1 || len(remote.request.Definition.Experts) != 2 {
			t.Fatalf("migrated mode workspace=%v: err=%v calls=%d", workspace, err, remote.calls)
		}
		definition := remote.request.Definition
		if workspace {
			if len(definition.Connectors) != 2 || definition.Connectors[0].Name == definition.Connectors[1].Name {
				t.Fatal("Workspace connector names collide")
			}
			for _, c := range definition.Connectors {
				if !strings.Contains(string(c.Config), `"search"`) {
					t.Fatal("original server key changed")
				}
			}
		} else {
			if len(definition.Connectors) != 0 {
				t.Fatal("default mode changed to Workspace mode implicitly")
			}
			for _, expert := range definition.Experts {
				if !strings.Contains(string(expert.MCPConfig), `"search"`) {
					t.Fatal("default per-expert MCP config missing")
				}
			}
		}
	}
}

func (r *migratedGraphInstaller) InstallCapability(_ context.Context, _, _, _, _ string, request fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	r.request = request
	r.calls++
	return &fleet.CapabilityInstallResult{ExpertTeamID: "fixture-fleet-team"}, nil
}

// Test the seam between the current single-database converter and version resolver.
// The remote mutation is captured here; the separate rehearsal tool runs the
// unchanged real Fleet handler/Postgres and Daemon filesystem implementation.
func TestMigratedGraphFeedsPinnedInstallationMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedMigrationRoot(t, database, "migration-install", "private", "published")
	gateSQL, err := os.ReadFile("../../scripts/pinned-release-readiness.sql")
	if err != nil {
		t.Fatal(err)
	}
	checkGate := func(wantClear bool) {
		t.Helper()
		var name string
		var blockers int
		if err := database.QueryRow(string(gateSQL)).Scan(&name, &blockers); err != nil {
			t.Fatal(err)
		}
		if name != "pinned_readiness_blockers" || (blockers == 0) != wantClear {
			t.Fatalf("gate=%s blockers=%d wantClear=%v", name, blockers, wantClear)
		}
	}
	checkGate(false)
	applyCurrentMigration(t, database)
	checkGate(true)
	remote := &migratedGraphInstaller{}
	svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
	out, err := svc.CreateInstallation(ctx, managedOwner(), "migration-install", pluginsvc.InstallationParams{WorkspaceID: "fixture-workspace", RuntimeID: "7f63db70-6b66-4c2d-a52f-fb6de1d78ad9", IdempotencyKey: "migration-install-attempt", Token: "fixture-token"})
	if err != nil || out.SquadID != "fixture-fleet-team" || remote.calls != 1 {
		t.Fatalf("migrated installation: %+v %v calls=%d", out, err, remote.calls)
	}
	d := remote.request.Definition
	if len(d.Experts) != 1 || len(d.Skills) != 1 || d.ExpertTeam == nil || len(d.ExpertTeam.Members) != 1 {
		t.Fatalf("wrong expanded counts: experts=%d skills=%d team=%+v", len(d.Experts), len(d.Skills), d.ExpertTeam)
	}
	if d.Experts[0].Instructions != "member instructions" || d.ExpertTeam.Instructions != "team instructions" {
		t.Fatal("migrated instruction bytes changed")
	}
	name := d.Skills[0].Name
	if name != "migration-install-skill" || !strings.Contains(d.Skills[0].Content, "name: skill") || len(d.Experts[0].SkillNames) != 1 || d.Experts[0].SkillNames[0] != name {
		t.Fatal("migrated skill alias/content/binding mismatch")
	}
	if !strings.Contains(string(d.Experts[0].MCPConfig), `"search"`) {
		t.Fatal("independent connector not loaded")
	}
	if !d.ExpertTeam.Members[0].IsLeader || d.ExpertTeam.Members[0].ExpertName != d.Experts[0].Name {
		t.Fatal("team role/member wiring lost")
	}
	// Probe a corrupt/legacy released member behind a healthy root. The gate
	// must follow pinned versions, not merely inspect the root or live graph.
	if _, err = database.Exec(`UPDATE plugin_versions v JOIN plugins p ON p.published_version_id=v.version_id SET v.plugin_json=JSON_SET(v.plugin_json,'$.mcp_config','legacy') WHERE p.plugin_type='expert' AND p.is_embedded=0`); err != nil {
		t.Fatal(err)
	}
	checkGate(false)
}
