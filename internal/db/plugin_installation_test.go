package db

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/fleet"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestInstallationBatchScopeAndReleasedDelistedPinsMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	for _, p := range []seedPlugin{{id: "own", visibility: "private", listingState: "published"}, {id: "other-private", visibility: "private", owner: "someone-else", listingState: "published"}, {id: "space-delisted", visibility: "space", listingState: "delisted"}, {id: "foreign-space", visibility: "space", space: "space-b"}, {id: "global", visibility: "system"}, {id: "embedded", visibility: "private", embedded: true}} {
		seed(t, database, p)
	}
	for _, id := range []string{"own", "other-private", "space-delisted", "foreign-space", "global", "embedded"} {
		if _, err := database.Exec(`INSERT INTO plugin_versions(version_id,plugin_id,version,declared_version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,created_by,created_at,snapshot_schema_version,artifacts_json) VALUES (?,?,'1','1.0.0',JSON_OBJECT(),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),JSON_ARRAY(),'user-1',NOW(3),1,JSON_ARRAY())`, "v-"+id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO plugin_version_releases VALUES (?,?,'1.0.0','user-1',NOW(3))`, id, "v-"+id); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{"own", "other-private", "space-delisted", "foreign-space", "global", "embedded"}
	sc := pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a", Admin: true}
	got, err := repo.GetInstallPlugins(ctx, sc, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got["own"] == nil || got["space-delisted"] == nil || got["global"] == nil {
		t.Fatalf("scope bypass or delist regression: %+v", got)
	}
	keys := []pluginrepo.InstallVersionKey{}
	for _, id := range ids {
		keys = append(keys, pluginrepo.InstallVersionKey{PluginID: id, VersionID: "v-" + id})
	}
	keys = append(keys, pluginrepo.InstallVersionKey{PluginID: "own", VersionID: "v-global"})
	versions, err := repo.GetInstallVersions(ctx, sc, keys)
	if err != nil || len(versions) != 3 {
		t.Fatalf("released batch=%+v err=%v", versions, err)
	}
	if _, err = database.Exec(`DELETE FROM plugin_version_releases WHERE plugin_id='own'`); err != nil {
		t.Fatal(err)
	}
	versions, err = repo.GetInstallVersions(ctx, sc, []pluginrepo.InstallVersionKey{{PluginID: "own", VersionID: "v-own"}})
	if err != nil || len(versions) != 0 {
		t.Fatalf("unreleased version returned: %v %v", versions, err)
	}
}

func TestInstallationAttemptScopeAndConcurrentClaimMySQL(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	sc := pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a"}
	a := pluginrepo.InstallAttempt{WorkspaceID: "workspace-a", KeyDigest: strings.Repeat("a", 64), PluginID: "expert", RootVersionID: "v1", AdapterVersion: "mk1", InputFingerprint: strings.Repeat("b", 64), RequestFingerprint: strings.Repeat("c", 64)}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, vid := range []string{"v1", "v2"} {
		wg.Add(1)
		go func(vid string) {
			defer wg.Done()
			candidate := a
			candidate.RootVersionID = vid
			_, err := repo.ClaimInstallAttempt(ctx, sc, candidate)
			errs <- err
		}(vid)
	}
	wg.Wait()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	winner, err := repo.GetInstallAttempt(ctx, sc, a.WorkspaceID, a.KeyDigest)
	if err != nil || winner == nil {
		t.Fatalf("claim=%+v %v", winner, err)
	}
	for _, foreign := range []pluginrepo.Scope{{CallerUID: "other", SpaceID: sc.SpaceID}, {CallerUID: sc.CallerUID, SpaceID: "space-b", Admin: true}} {
		hidden, err := repo.GetInstallAttempt(ctx, foreign, a.WorkspaceID, a.KeyDigest)
		if err != nil || hidden != nil {
			t.Fatalf("cross-scope receipt %+v %v", hidden, err)
		}
	}
	winner.AgentID = "agent-1"
	if err = repo.CompleteInstallAttempt(ctx, sc, *winner); err != nil {
		t.Fatal(err)
	}
	wrongFingerprint := *winner
	wrongFingerprint.RequestFingerprint = strings.Repeat("d", 64)
	if err = repo.CompleteInstallAttempt(ctx, sc, wrongFingerprint); !errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatalf("receipt fingerprint mismatch accepted: %v", err)
	}
	winner.AgentID = "agent-2"
	if err = repo.CompleteInstallAttempt(ctx, sc, *winner); !errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatalf("receipt overwrite allowed %v", err)
	}
}

type dbCapabilityInstall struct {
	calls int
	in    fleet.CapabilityInstallRequest
}

func (f *dbCapabilityInstall) InstallCapability(_ context.Context, _, _, _, _ string, in fleet.CapabilityInstallRequest) (*fleet.CapabilityInstallResult, error) {
	f.calls++
	f.in = in
	return &fleet.CapabilityInstallResult{ExpertID: "fleet-expert"}, nil
}
func TestPinnedInstallationRealRepositoryFreezesRootAcrossRetryMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	remote := &dbCapabilityInstall{}
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true).WithPinnedInstallation(true).WithInstallationHMACKeys("test", map[string]string{"test": "synthetic-installation-test-key-32-bytes"}).WithCapabilityInstaller(remote)
	owner := managedOwner()
	d, err := svc.Create(ctx, owner, managedWrite("1.0.0", "first released", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: d.Plugin.ID}); err != nil {
		t.Fatal(err)
	}
	p := pluginsvc.InstallationParams{WorkspaceID: "workspace-a", RuntimeID: "7f63db70-6b66-4c2d-a52f-fb6de1d78ad9", IdempotencyKey: "fixture-operation", CustomEnv: map[string]string{"TEST_SECRET": "fixture-secret"}, Token: "fixture-token"}
	if _, err = svc.CreateInstallation(ctx, owner, d.Plugin.ID, p); err != nil {
		t.Fatalf("install %#v", err)
	}
	if remote.in.Definition.Experts[0].Instructions != "first released" {
		t.Fatal("wrong released bytes")
	}
	if _, err = svc.Update(ctx, owner, d.Plugin.ID, editingWrite(t, database, d.Plugin.ID, managedWrite("2.0.0", "second released", model.PluginVisibilityPrivate))); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: d.Plugin.ID}); err != nil {
		t.Fatal(err)
	}
	out, err := svc.CreateInstallation(ctx, owner, d.Plugin.ID, p)
	if err != nil || !out.Replayed || remote.calls != 1 {
		t.Fatalf("retry %+v %v calls%d", out, err, remote.calls)
	}
	var persisted string
	if err = database.QueryRow(`SELECT CONCAT_WS('|',space_id,caller_uid,workspace_id,key_digest,plugin_id,root_version_id,adapter_version,input_fingerprint,request_fingerprint,agent_id,team_id) FROM plugin_install_attempts`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-secret", "fixture-token", "fixture-operation", "first released"} {
		if strings.Contains(persisted, secret) {
			t.Fatal("sensitive request material persisted")
		}
	}
}
