package db

import (
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestReferenceExpectedHeadSerializesConcurrentSaves(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	ctx := context.Background()
	owner := managedOwner()
	detail, err := svc.Create(ctx, owner, managedWrite("1.0.0", "initial", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	head := *detail.Plugin.CurrentVersionID
	mutation := func(content string) pluginrepo.Mutation {
		req := managedWrite("2.0.0", content, model.PluginVisibilityPrivate)
		docs, err := pluginsvc.CanonicalizeDocuments(req.Name, req.Type, req.Tags, req.Manifest, req.Package, owner.SpaceID)
		if err != nil {
			t.Fatal(err)
		}
		p := *detail.Plugin
		label := "2.0.0"
		p.CurrentVersion = &label
		p.Manifest, p.Package, p.ManifestHash, p.PluginHash = docs.Manifest, docs.Package, docs.ManifestHash, docs.PluginHash
		return pluginrepo.Mutation{Plugin: p, SnapshotVersion: true, ExpectedCurrentVersionID: head, OperatorID: owner.UID}
	}
	mutations := []pluginrepo.Mutation{mutation("writer A"), mutation("writer B")}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, m := range mutations {
		go func(m pluginrepo.Mutation) { <-start; _, err := repo.Update(ctx, tenantScope(), m); results <- err }(m)
	}
	close(start)
	success, conflict := 0, 0
	for range mutations {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, pluginrepo.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	current, _, err := repo.GetWithRelations(ctx, tenantScope(), detail.Plugin.ID)
	if err != nil {
		t.Fatal(err)
	}
	currentHead := *current.CurrentVersionID
	// A stale retry fails without appending history, relations or audit rows.
	if _, err = repo.Update(ctx, tenantScope(), mutations[0]); !errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatalf("stale retry: %v", err)
	}
	for _, table := range []string{"plugin_versions", "plugin_audit_logs"} {
		var count int
		if err = database.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE plugin_id=?", detail.Plugin.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("%s rows=%d want2", table, count)
		}
	}
	// Existing callers that omit the internal expected head remain compatible.
	unguarded := mutation("unguarded compatibility")
	unguarded.ExpectedCurrentVersionID = ""
	if _, err = repo.Update(ctx, tenantScope(), unguarded); err != nil {
		t.Fatalf("unguarded save: %v", err)
	}
	current, _, err = repo.GetWithRelations(ctx, tenantScope(), detail.Plugin.ID)
	if err != nil || *current.CurrentVersionID == currentHead {
		t.Fatalf("compatibility head not advanced: %v", err)
	}
}
