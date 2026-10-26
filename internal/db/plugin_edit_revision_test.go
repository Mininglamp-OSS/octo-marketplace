package db

import (
	"context"
	"database/sql"
	"errors"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"sync"
	"testing"

	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func TestEditingRevisionSerializesConcurrentSaves(t *testing.T) {
	database := reviewDB(t)
	seed(t, database, seedPlugin{id: "cas-root", typ: "expert", visibility: "private"})
	ctx := context.Background()
	repo := pluginrepo.New(database)
	scope := tenantScope()
	original, relations, err := repo.GetWithRelations(ctx, scope, "cas-root")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := repo.GetEditRevision(ctx, scope, original.ID)
	if err != nil || revision != 1 {
		t.Fatalf("initial revision=%d err=%v", revision, err)
	}
	mutation := pluginrepo.Mutation{Plugin: *original, Relations: relations, OperatorID: scope.CallerUID, ExpectedEditRevision: &revision, RequireEditRevision: true, SnapshotVersion: true}
	// Both writers read the same revision before either starts its transaction.
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); <-start; _, err := repo.Update(ctx, scope, mutation); results <- err }()
	}
	close(start)
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, pluginrepo.ErrEditConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	current, err := repo.GetEditRevision(ctx, scope, original.ID)
	if err != nil || current != 2 {
		t.Fatalf("revision=%d err=%v", current, err)
	}
	var audits, versions int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_audit_logs WHERE plugin_id=?`, original.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, original.ID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || versions != 0 {
		t.Fatalf("identical save should audit once without duplicating snapshot: audits=%d versions=%d", audits, versions)
	}
	// Replaying a stale editing token cannot append audit or alter content.
	if _, err = repo.Update(ctx, scope, mutation); !errors.Is(err, pluginrepo.ErrEditConflict) {
		t.Fatalf("stale: %v", err)
	}
	mutation.ExpectedEditRevision = nil
	if _, err = repo.Update(ctx, scope, mutation); !errors.Is(err, pluginrepo.ErrEditConflict) {
		t.Fatalf("missing required: %v", err)
	}
	mutation.ExpectedEditRevision = &current
	updated, err := repo.Update(ctx, scope, mutation)
	if err != nil || updated.EditRevision != 3 {
		t.Fatalf("second same-content save: %+v %v", updated, err)
	}
}

func TestEditingRevisionDoesNotExposeOtherScopes(t *testing.T) {
	database := reviewDB(t)
	seed(t, database, seedPlugin{id: "scope-root", visibility: "private"})
	repo := pluginrepo.New(database)
	ctx := context.Background()
	for _, scope := range []pluginrepo.Scope{{CallerUID: "other", SpaceID: "space-a"}, {CallerUID: "user-1", SpaceID: "space-b"}} {
		if _, err := repo.GetEditRevision(ctx, scope, "scope-root"); !errors.Is(err, pluginrepo.ErrNotFound) {
			t.Fatalf("scope %+v: %v", scope, err)
		}
	}
	if revision, err := repo.GetEditRevision(ctx, pluginrepo.Scope{Admin: true}, "scope-root"); err != nil || revision != 1 {
		t.Fatalf("admin revision=%d err=%v", revision, err)
	}
}

// editingWrite refreshes the token for legacy regression tests whose intention
// is a new user save. Stale-token tests use explicit requests instead.
func editingWrite(t *testing.T, database *sql.DB, id string, req pluginsvc.WriteRequest) pluginsvc.WriteRequest {
	t.Helper()
	var revision uint64
	if err := database.QueryRow(`SELECT edit_revision FROM plugins WHERE plugin_id=?`, id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	req.ExpectedEditRevision = &revision
	if req.Relations == nil {
		req.Relations = []pluginsvc.RelationRequest{}
	}
	return req
}
