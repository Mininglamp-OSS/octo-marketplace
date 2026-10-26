package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
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

// Test the seam between M3's actual SQL writes and I's actual version resolver.
// The remote mutation is captured here; the separate rehearsal tool runs the
// unchanged real Fleet handler/Postgres and Daemon filesystem implementation.
func TestMigratedGraphFeedsPinnedInstallationMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedStockRoot(t, database, "migration-install", "private", "published")
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
	applyOfflineMigrationForTest(t, database, "migration-install", "migration-install-batch")
	// The migration publishes through the normal managed-release path, so every
	// released snapshot it writes already carries frozen pins. No test-only
	// pin backfill stands in for it: the install below would fail closed if the
	// migration writer regressed to emitting NULL pins.
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
	// The new adapter preserves the market name and original SKILL.md bytes; it
	// must not mint the obsolete mk1 alias or rewrite the frontmatter name.
	if name != "migration-install-skill" || strings.Contains(name, "-mk1-") || !strings.Contains(d.Skills[0].Content, "name: skill") || len(d.Experts[0].SkillNames) != 1 || d.Experts[0].SkillNames[0] != name {
		t.Fatalf("migrated skill name/content/binding mismatch: name=%q content=%q skills=%v", name, d.Skills[0].Content, d.Experts[0].SkillNames)
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

// stripReleasedSnapshotPinsForTest rewrites the root's published snapshot back
// into the pre-P-model shape: formally released and index-consistent, but with
// every Skill/Expert edge unpinned. It simulates the snapshots already migrated
// before publish-time freezing existed; those remain the backfill CLI's job.
func stripReleasedSnapshotPinsForTest(t *testing.T, database *sql.DB, pluginID string) {
	t.Helper()
	var versionID string
	var raw []byte
	if err := database.QueryRow(`SELECT v.version_id,v.relations_json FROM plugins p JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND v.version_id=p.published_version_id WHERE p.plugin_id=?`, pluginID).Scan(&versionID, &raw); err != nil {
		t.Fatal(err)
	}
	rels, err := model.DecodeSnapshotRelations(raw)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rels {
		if rels[i].Type != "expert_connector" {
			rels[i].TargetVersionID = nil
		}
	}
	encoded, err := model.EncodeSnapshotRelations(rels)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE plugin_versions SET relations_json=? WHERE plugin_id=? AND version_id=?`, string(encoded), pluginID, versionID); err != nil {
		t.Fatal(err)
	}
	// MySQL normalizes JSON on storage; hash what the column actually holds so
	// the fixture stays a real, indexed historical snapshot.
	var stored []byte
	if err = database.QueryRow(`SELECT relations_json FROM plugin_versions WHERE plugin_id=? AND version_id=?`, pluginID, versionID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(stored)
	if _, err = database.Exec(`UPDATE plugin_version_reference_index_state SET snapshot_digest=? WHERE source_plugin_id=? AND source_version_id=?`, hex.EncodeToString(sum[:]), pluginID, versionID); err != nil {
		t.Fatal(err)
	}
}

// Snapshots migrated before the P model carry no pins and must stay fail-closed:
// reads never lazily freeze them, so A34/install refuse them instead of silently
// following live heads until the backfill CLI repairs them.
func TestMigratedGraphWithoutPinsFailsClosedMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedStockRoot(t, database, "migration-unpinned", "private", "published")
	applyOfflineMigrationForTest(t, database, "migration-unpinned", "migration-unpinned-batch")
	// New migration writes are frozen at publish. Re-create the historical shape
	// explicitly so this test keeps covering already-migrated snapshots.
	stripReleasedSnapshotPinsForTest(t, database, "migration-unpinned")
	repo := pluginrepo.New(database)
	if _, err := repo.ResolvePinnedGraph(ctx, pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a"}, "migration-unpinned"); !errors.Is(err, pluginrepo.ErrInvalidRelation) {
		t.Fatalf("unpinned migrated snapshot resolved: %v", err)
	}
	remote := &migratedGraphInstaller{}
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
	if _, err := svc.CreateInstallation(ctx, managedOwner(), "migration-unpinned", pluginsvc.InstallationParams{WorkspaceID: "fixture-workspace", RuntimeID: "7f63db70-6b66-4c2d-a52f-fb6de1d78ad9", IdempotencyKey: "migration-unpinned-attempt", Token: "fixture-token"}); err == nil || remote.calls != 0 {
		t.Fatalf("unpinned migrated snapshot reached Fleet: err=%v calls=%d", err, remote.calls)
	}
}
