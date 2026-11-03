package db

import (
	"context"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"testing"
)

func TestSuspendedExpertRequiresAvailableUnpinnedConnector(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{true: "manual", false: "automatic"}[manual], func(t *testing.T) {
			ctx, owner := context.Background(), managedOwner()
			db := reviewDB(t)
			repo := pluginrepo.New(db)
			svc := pluginsvc.New(repo).WithVersionManagement(true)
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), !manual, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			const connector = "unversioned-connector"
			seed(t, db, seedPlugin{id: connector, typ: "connector", visibility: "space", listingState: "published"})
			req := managedWrite("1.0.0", "Expert using connector", model.PluginVisibilitySpace)
			req.Relations = []pluginsvc.RelationRequest{{Type: "expert_connector", TargetPluginID: connector}}
			expert, err := svc.Create(ctx, owner, req)
			if err != nil {
				t.Fatal(err)
			}
			id := expert.Plugin.ID
			publishManaged(t, svc, owner, id)
			original, err := repo.GetVersionState(ctx, tenantScope(), id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.Delist(ctx, managedReviewer(), pluginsvc.DelistParams{PluginID: connector}); err != nil {
				t.Fatal(err)
			}
			publish := func() (string, error) {
				p, e := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id, Version: "1.0.1"})
				if e != nil {
					return "", e
				}
				if p.Review == nil {
					return "", nil
				}
				_, e = svc.ApproveReview(ctx, managedReviewer(), p.Review.ID)
				return p.Review.ID, e
			}
			review, err := publish()
			if err == nil {
				t.Fatal("published while the unpinned connector was delisted")
			}
			if review != "" {
				if err := svc.CancelReview(ctx, owner, review); err != nil {
					t.Fatal(err)
				}
			}
			check := func(want string) {
				t.Helper()
				d, e := svc.Detail(ctx, owner, id, true)
				if e != nil {
					t.Fatal(e)
				}
				if d.Plugin.ListingState != model.PluginListingStateDelisted || (len(d.DependencyIssues) > 0) != (want == "unavailable") {
					t.Fatalf("listing=%s dependency=%+v", d.Plugin.ListingState, d.DependencyIssues)
				}
				v, e := repo.GetVersionState(ctx, tenantScope(), id)
				if e != nil || *v.PublishedVersionID != *original.PublishedVersionID {
					t.Fatal("published pointer changed")
				}
			}
			check("unavailable")
			if _, err = db.Exec("UPDATE plugins SET listing_state='published' WHERE plugin_id=?", connector); err != nil {
				t.Fatal(err)
			}
			check("restored")
			if _, err = publish(); err != nil {
				t.Fatal(err)
			}
			d, err := svc.Detail(ctx, owner, id, true)
			if err != nil || d.Plugin.ListingState != model.PluginListingStatePublished || len(d.DependencyIssues) != 0 {
				t.Fatalf("republish did not clear suspension: %v", err)
			}
		})
	}
}

func TestDelistIgnoresDraftOnlyMemberEdge(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	seed(t, db, seedPlugin{id: "draft-only", typ: "skill", visibility: "space", listingState: "published"})
	seedRelation(t, db, "draft-edge", "expert", "draft-only", "expert_skill")
	seedRelation(t, db, "live-team", "team", "expert", "expert_team_expert")
	if _, err := db.Exec("UPDATE plugin_relations SET target_version_id='expert-v1' WHERE relation_id='live-team'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE plugins SET published_version_id=NULL WHERE plugin_id='team'"); err != nil {
		t.Fatal(err)
	}
	r := pluginrepo.New(db).WithVersionManagement(true)
	if _, err := r.DelistPlugin(context.Background(), reviewerScope(), pluginrepo.DelistParams{PluginID: "draft-only", OperatorID: "admin-1"}); err != nil {
		t.Fatal(err)
	}
	var listing string
	var suspended bool
	if err := db.QueryRow("SELECT listing_state, EXISTS(SELECT 1 FROM plugin_dependency_suspensions WHERE plugin_id='team') FROM plugins WHERE plugin_id='team'").Scan(&listing, &suspended); err != nil || listing != "published" || suspended {
		t.Fatalf("legacy team: %s %t %v", listing, suspended, err)
	}
}
