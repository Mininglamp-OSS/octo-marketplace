package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

type migratedGraphInstaller struct {
	request fleet.CapabilityInstallRequest
	calls   int
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
