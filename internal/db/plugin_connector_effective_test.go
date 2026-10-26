package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func TestConnectorEffectivePreservesLaterEditingDraft(t *testing.T) {
	db := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(db).WithVersionManagement(true)
	seed(t, db, seedPlugin{id: "effective", typ: "connector", visibility: "space", listingState: "draft", currentVersion: "1.0.0"})
	first := newRequest("effective", "1.0.0")
	snapshot := snapshotOf(`{"plugin_name":"approved","labels":["stable"]}`, `{"attachments":[{"path":"connector/tools.json","content_type":"raw","raw_content":"[{},{}]"}]}`, nil)
	if err := repo.InsertReviewRequest(ctx, tenantScope(), first, snapshot); err != nil {
		t.Fatal(err)
	}
	// A newer editing save must survive approval of the previous frozen payload.
	draft, rels, err := repo.GetWithRelations(ctx, tenantScope(), "effective")
	if err != nil {
		t.Fatal(err)
	}
	draft.Name = "new draft"
	draft.Manifest = json.RawMessage(`{"plugin_name":"new draft","labels":[]}`)
	draft.PluginHash = "sha256:new-draft"
	draft.ToolCount = 9
	rev := uint64(1)
	if _, err = repo.Update(ctx, tenantScope(), pluginrepo.Mutation{Plugin: *draft, Relations: rels, ExpectedEditRevision: &rev, RequireEditRevision: true, OperatorID: "user-1"}); err != nil {
		t.Fatal(err)
	}
	approved, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{ReviewID: first.ID, ReviewerUID: "admin-1"})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Name != "approved" || approved.ToolCount != 2 || approved.EffectiveRevision == nil || *approved.EffectiveRevision != 1 {
		t.Fatalf("approved=%+v", approved)
	}
	editing, err := repo.Get(ctx, tenantScope(), "effective")
	if err != nil {
		t.Fatal(err)
	}
	if editing.Name != "new draft" || editing.EditRevision != 2 || editing.ToolCount != 9 {
		t.Fatalf("editing was replaced: %+v", editing)
	}
	effective, err := repo.GetConnectorEffective(ctx, pluginrepo.Scope{CallerUID: "reader", SpaceID: "space-a"}, "effective")
	if err != nil || effective.Name != "approved" || effective.ToolCount != 2 {
		t.Fatalf("effective=%+v err=%v", effective, err)
	}
	if _, err = repo.GetConnectorEffective(ctx, pluginrepo.Scope{CallerUID: "user-1", SpaceID: "other-space"}, "effective"); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-Space=%v", err)
	}
	var versions, releases int
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id='effective'`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id='effective'`).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if versions != 0 || releases != 0 {
		t.Fatalf("connector versions=%d releases=%d", versions, releases)
	}
	// Public list projects the approved payload, including COUNT/search behavior.
	items, total, err := repo.List(ctx, pluginrepo.Scope{CallerUID: "reader", SpaceID: "space-a"}, pluginrepo.ListFilter{PlacementCode: "default", Keyword: "approved"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Name != "approved" || items[0].ToolCount != 2 {
		t.Fatalf("list=%+v total=%d", items, total)
	}
}

func TestAdminConnectorWriteUpdatesEffectiveWithoutReview(t *testing.T) {
	db := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(db).WithVersionManagement(true)
	seed(t, db, seedPlugin{id: "admin-effective", typ: "connector", visibility: "system", listingState: "published"})
	p, rels, err := repo.GetWithRelations(ctx, pluginrepo.Scope{Admin: true}, "admin-effective")
	if err != nil {
		t.Fatal(err)
	}
	for _, rev := range []uint64{1, 2} {
		p.Name = "direct"
		p.ManifestHash = "sha256:direct"
		p.PluginHash = "sha256:direct-package"
		if _, err = repo.Update(ctx, pluginrepo.Scope{Admin: true}, pluginrepo.Mutation{Plugin: *p, Relations: rels, ExpectedEditRevision: &rev, RequireEditRevision: true, OperatorID: "platform"}); err != nil {
			t.Fatal(err)
		}
	}
	effective, err := repo.GetConnectorEffective(ctx, pluginrepo.Scope{CallerUID: "reader", SpaceID: "other"}, p.ID)
	if err != nil || effective.EffectiveRevision == nil || *effective.EffectiveRevision != 1 {
		t.Fatalf("identical write effective=%+v err=%v", effective, err)
	}
	var reviews int
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_review_requests WHERE plugin_id=?`, p.ID).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if reviews != 0 {
		t.Fatalf("admin created %d reviews", reviews)
	}
}
