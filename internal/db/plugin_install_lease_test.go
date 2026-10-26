package db

import (
	"context"
	"encoding/json"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInstallFrozenPlanAndDispatchLease(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	sc := tenantScope()
	a := pluginrepo.InstallAttempt{WorkspaceID: "w", KeyDigest: strings.Repeat("a", 64), PluginID: "root", RootVersionID: "v1", AdapterVersion: "original-v1", InputFingerprint: strings.Repeat("b", 64), RequestFingerprint: strings.Repeat("c", 64), HMACKeyID: "test-key", IntentDigest: strings.Repeat("d", 64), FrozenPlan: json.RawMessage(`{"root_version_id":"v1","definition":{}}`)}
	first, err := repo.ClaimInstallAttempt(ctx, sc, a)
	if err != nil {
		t.Fatal(err)
	}
	a.RootVersionID = "v2"
	a.FrozenPlan = json.RawMessage(`{"root_version_id":"v2"}`)
	winner, err := repo.ClaimInstallAttempt(ctx, sc, a)
	if err != nil || winner.RootVersionID != "v1" || string(winner.FrozenPlan) != string(first.FrozenPlan) {
		t.Fatalf("claim replaced original plan: %+v %v", winner, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for _, token := range []string{"one", "two"} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			ok, err := repo.AcquireInstallDispatch(ctx, sc, *winner, token, now, time.Minute, 23*time.Hour)
			if err != nil {
				results <- "error"
			} else if ok {
				results <- token
			} else {
				results <- ""
			}
		}(token)
	}
	wg.Wait()
	close(results)
	lease := ""
	for token := range results {
		if token == "error" {
			t.Fatal("lease error")
		}
		if token != "" {
			if lease != "" {
				t.Fatal("two concurrent dispatches")
			}
			lease = token
		}
	}
	if lease == "" {
		t.Fatal("no sender")
	}
	leased, err := repo.GetInstallAttempt(ctx, sc, "w", a.KeyDigest)
	if err != nil || leased.RetryUntil == nil {
		t.Fatal("missing fixed retry cutoff", err)
	}
	cutoff := *leased.RetryUntil
	// Expired DISPATCHING implies uncertainty; the next sender must preserve it.
	ok, err := repo.AcquireInstallDispatch(ctx, sc, *winner, "recovery", now.Add(2*time.Minute), time.Minute, 23*time.Hour)
	if err != nil || !ok {
		t.Fatal("recovery lease failed", err)
	}
	recovered, err := repo.GetInstallAttempt(ctx, sc, "w", a.KeyDigest)
	if err != nil || !recovered.HadUnknown || !recovered.RetryUntil.Equal(cutoff) {
		t.Fatalf("uncertainty/cutoff lost: %+v %v", recovered, err)
	}
	if err = repo.SetInstallDispatchUnknown(ctx, sc, *winner, lease); err != nil {
		t.Fatal(err)
	}
	current, _ := repo.GetInstallAttempt(ctx, sc, "w", a.KeyDigest)
	if current.LeaseToken != "recovery" {
		t.Fatal("stale sender cleared active lease")
	}
	winner.AgentID = "result"
	if ok, err = repo.FinishInstallDispatch(ctx, sc, *winner, lease); err != nil || ok {
		t.Fatal("stale sender completed new lease", err)
	}
	if ok, err = repo.FinishInstallDispatch(ctx, sc, *winner, "recovery"); err != nil || !ok {
		t.Fatal("success transition failed", err)
	}
	if ok, err = repo.FinishInstallDispatch(ctx, sc, *winner, "recovery"); err != nil || ok {
		t.Fatal("success counted twice", err)
	}
	if err = repo.SetInstallDispatchUnknown(ctx, sc, *winner, "recovery"); err != nil {
		t.Fatal(err)
	}
	final, _ := repo.GetInstallAttempt(ctx, sc, "w", a.KeyDigest)
	if final.State != "SUCCEEDED" || !final.HadUnknown {
		t.Fatal("terminal result or uncertainty overwritten")
	}
	if ok, err = repo.AcquireInstallDispatch(ctx, pluginrepo.Scope{CallerUID: sc.CallerUID, SpaceID: "other"}, *winner, "foreign", now, time.Minute, time.Hour); err != nil || ok {
		t.Fatal("cross-space lease", err)
	}
}
