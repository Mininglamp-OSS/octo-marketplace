package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// Interleave a real save after Service.Publish's unlocked checks, immediately
// before the real repository transaction. No sleeps or mocked database writes.
type interleavedConnectorPublishRepo struct {
	*pluginrepo.Repo
	beforePublish func()
}

func (r *interleavedConnectorPublishRepo) PublishPlugin(ctx context.Context, scope pluginrepo.Scope, p pluginrepo.PublishParams) (*model.Plugin, error) {
	r.beforePublish()
	return r.Repo.PublishPlugin(ctx, scope, p)
}

func TestPrivateConnectorPublishRechecksDraftRevisionUnderLock(t *testing.T) {
	for _, tc := range []struct {
		name      string
		save      bool
		condition bool
		conflict  bool
	}{
		{name: "interleaved save conflicts", save: true, condition: true, conflict: true},
		{name: "unchanged revision publishes", condition: true},
		{name: "omitted revision retains compatibility", save: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, owner := context.Background(), managedOwner()
			write := pluginsvc.WriteRequest{
				Name: "Original", Type: model.PluginTypeConnector, Visibility: model.PluginVisibilityPrivate,
				Manifest: json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Original","plugin_type":"connector","name":"connector-fixture","description":"Publication race fixture.","labels":[],"examples":[],"config":{}}`),
				Package:  json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","connector":{"type":"mcp","source":"connector.fixture"},"attachments":[{"path":"mcp.json","content_type":"raw","mime_type":"application/json","raw_content":"{\"mcpServers\":{}}"}]}`),
			}
			if _, err := pluginsvc.CanonicalizeDocuments(write.Name, write.Type, write.Tags, write.Manifest, write.Package, owner.SpaceID); err != nil {
				t.Fatalf("invalid connector fixture: %v", err)
			}
			database := reviewDB(t)
			repo := pluginrepo.New(database)
			svc := pluginsvc.New(repo).WithVersionManagement(true)
			created, err := svc.Create(ctx, owner, write)
			if err != nil {
				t.Fatal(err)
			}
			id := created.Plugin.ID
			params := pluginsvc.PublishParams{PluginID: id}
			if tc.condition {
				params.ExpectedDraftRevision = model.PluginDraftRevision(created.Plugin)
			}
			interleaved := false
			var savedRevision string
			wrapped := &interleavedConnectorPublishRepo{Repo: repo, beforePublish: func() {
				interleaved = true
				if tc.save {
					write.Name = "Saved in another tab"
					write.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Saved in another tab","plugin_type":"connector","name":"connector-fixture","description":"Publication race fixture.","labels":[],"examples":[],"config":{}}`)
					saved, err := svc.Update(ctx, owner, id, write)
					if err != nil {
						t.Fatal(err)
					}
					savedRevision = model.PluginDraftRevision(saved.Plugin)
					if savedRevision == params.ExpectedDraftRevision {
						t.Fatal("interleaved save did not change the revision")
					}
				}
			}}
			result, err := pluginsvc.New(wrapped).WithVersionManagement(true).Publish(ctx, owner, params)
			if !interleaved {
				t.Fatal("publication did not reach the repository after the service checks")
			}
			if tc.conflict {
				if !errors.Is(err, pluginsvc.ErrConflict) || result != nil {
					t.Fatalf("stale publication = %+v, %v; want nil, ErrConflict", result, err)
				}
			} else if err != nil || result == nil {
				t.Fatalf("publication = %+v, %v; want success", result, err)
			}
			current, _, err := repo.GetWithRelations(ctx, tenantScope(), id)
			if err != nil {
				t.Fatal(err)
			}
			wantState := model.PluginListingStatePublished
			wantAudits := 1
			if tc.conflict {
				wantState, wantAudits = model.PluginListingStateDraft, 0
			}
			if current.ListingState != wantState || current.Name != write.Name {
				t.Fatalf("stored state/name = %s/%s, want %s/%s", current.ListingState, current.Name, wantState, write.Name)
			}
			if tc.conflict && model.PluginDraftRevision(current) != savedRevision {
				t.Fatal("publication overwrote the saved draft")
			}
			var audits int
			if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_audit_logs WHERE plugin_id=? AND action='publish'`, id).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if audits != wantAudits {
				t.Fatalf("publication audit count = %d, want %d", audits, wantAudits)
			}
		})
	}
}
