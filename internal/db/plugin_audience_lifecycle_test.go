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

func TestAudienceGroupDecisionIsAtomic(t *testing.T) {
	for _, decision := range []string{"approve", "reject", "cancel", "stale", "auto"} {
		t.Run(decision, func(t *testing.T) {
			db := reviewDB(t)
			ctx := context.Background()
			owner := managedOwner()
			repo := pluginrepo.New(db)
			svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), decision == "auto", owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			skill, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "Batch Skill", model.PluginVisibilityPrivate))
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, svc, owner, skill.Plugin.ID)
			ss, _ := repo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
			req := managedWrite("1.0.0", "batch expert", model.PluginVisibilityPrivate)
			req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.Plugin.ID, TargetVersionID: ss.PublishedVersionID, Type: "expert_skill"}}
			seed(t, db, seedPlugin{id: "batch-connector", typ: "connector", visibility: "private", listingState: "published"})
			req.Relations = append(req.Relations, pluginsvc.RelationRequest{TargetPluginID: "batch-connector", Type: "expert_connector"})
			expert, err := svc.Create(ctx, owner, req)
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, svc, owner, expert.Plugin.ID)
			es, _ := repo.GetVersionState(ctx, tenantScope(), expert.Plugin.ID)
			plan, err := svc.PreviewVisibility(ctx, owner, expert.Plugin.ID, model.PluginVisibilitySpace)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Dependencies) != 2 {
				t.Fatalf("plan: %+v", plan)
			}
			if _, err := svc.PreviewVisibility(ctx, pluginsvc.Caller{UID: owner.UID, SpaceID: "other"}, expert.Plugin.ID, model.PluginVisibilitySpace); !errors.Is(err, pluginsvc.ErrNotFound) {
				t.Fatalf("cross-space: %v", err)
			}
			params := pluginsvc.VisibilityParams{PluginID: expert.Plugin.ID, ExpectedPublishedVersionID: *es.PublishedVersionID, Visibility: model.PluginVisibilitySpace}
			if _, err := svc.ChangeVisibility(ctx, owner, params); err == nil {
				t.Fatal("batch widening without confirmation")
			}
			params.PlanRevision = plan.PlanRevision
			params.Confirm = true
			result, err := svc.ChangeVisibility(ctx, owner, params)
			if err != nil {
				t.Fatal(err)
			}
			if result.Pending != (decision != "auto") || result.GroupID == "" {
				t.Fatalf("result: %+v", result)
			}
			var childReview string
			if decision != "auto" {
				if err := db.QueryRow(`SELECT review_id FROM plugin_review_requests WHERE plugin_id=? AND status='pending'`, "batch-connector").Scan(&childReview); err != nil {
					t.Fatal(err)
				}
				rr, e := svc.GetReview(ctx, managedReviewer(), childReview)
				if e != nil {
					t.Fatal(e)
				}
				if rr.ReviewGroupID != result.GroupID || len(rr.AudienceMembers) != 3 {
					t.Fatalf("missing batch metadata: %+v", rr)
				}
				if _, err := svc.Update(ctx, owner, skill.Plugin.ID, managedSkillWrite("1.1.0", "Batch Skill", model.PluginVisibilityPrivate)); !errors.Is(err, pluginsvc.ErrReviewPending) {
					t.Fatalf("pending dependency editable: %v", err)
				}
			}
			switch decision {
			case "approve":
				_, err = svc.ApproveReview(ctx, managedReviewer(), childReview)
			case "reject":
				err = svc.RejectReview(ctx, managedReviewer(), childReview, "not approved")
			case "cancel":
				err = svc.CancelReview(ctx, owner, result.ReviewID)
			case "stale":
				_, err = db.Exec(`UPDATE plugins SET plugin_hash='changed' WHERE plugin_id=?`, skill.Plugin.ID)
				if err != nil {
					t.Fatal(err)
				}
				_, err = svc.ApproveReview(ctx, managedReviewer(), result.ReviewID)
				if !errors.Is(err, pluginsvc.ErrConflict) {
					t.Fatalf("stale approval: %v", err)
				}
				err = nil
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{skill.Plugin.ID, expert.Plugin.ID, "batch-connector"} {
				var visibility string
				if err := db.QueryRow(`SELECT visibility FROM plugins WHERE plugin_id=?`, id).Scan(&visibility); err != nil {
					t.Fatal(err)
				}
				expected := "private"
				if decision == "approve" || decision == "auto" {
					expected = "space"
				}
				if visibility != expected {
					t.Fatalf("partial decision %s: %s", id, visibility)
				}
			}
			state, _ := repo.GetVersionState(ctx, tenantScope(), expert.Plugin.ID)
			if *state.PublishedVersionID != *es.PublishedVersionID {
				t.Fatal("audience changed immutable content")
			}
			var pending int
			if err := db.QueryRow(`SELECT COUNT(*) FROM plugin_review_requests rr JOIN plugin_visibility_reviews vr ON vr.review_id=rr.review_id WHERE vr.group_id=? AND rr.status='pending'`, result.GroupID).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if decision == "stale" && pending != 3 || decision != "stale" && pending != 0 {
				t.Fatalf("partial review settlement: %d", pending)
			}
		})
	}
}

func TestAudienceNarrowingSuspendsUntilExplicitRepublish(t *testing.T) {
	db := reviewDB(t)
	ctx := context.Background()
	owner := managedOwner()
	repo := pluginrepo.New(db)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	skill, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "Narrow Skill", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, skill.Plugin.ID)
	ss, _ := repo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
	req := managedWrite("1.0.0", "narrow expert", model.PluginVisibilitySpace)
	req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.Plugin.ID, TargetVersionID: ss.PublishedVersionID, Type: "expert_skill"}}
	expert, err := svc.Create(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, expert.Plugin.ID)
	es, _ := repo.GetVersionState(ctx, tenantScope(), expert.Plugin.ID)
	teamReq := managedWrite("1.0.0", "team", model.PluginVisibilitySpace)
	teamReq.Name = "Narrow Team"
	teamReq.Type = model.PluginTypeExpertTeam
	teamReq.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Narrow Team","plugin_type":"expert_team","name":"narrow-team","description":"fixture","labels":[],"examples":[]}`)
	teamReq.Relations = []pluginsvc.RelationRequest{{TargetPluginID: expert.Plugin.ID, TargetVersionID: es.PublishedVersionID, Type: "expert_team_expert", Data: json.RawMessage(`{"member_key":"leader","is_leader":true,"role":"leader"}`)}}
	team, err := svc.Create(ctx, owner, teamReq)
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, team.Plugin.ID)
	plan, err := svc.PreviewVisibility(ctx, owner, skill.Plugin.ID, model.PluginVisibilityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Affected) != 2 {
		t.Fatalf("affected: %+v", plan)
	}
	params := pluginsvc.VisibilityParams{PluginID: skill.Plugin.ID, ExpectedPublishedVersionID: *ss.PublishedVersionID, Visibility: model.PluginVisibilityPrivate, Confirm: true, PlanRevision: "stale"}
	if _, err = svc.ChangeVisibility(ctx, owner, params); !errors.Is(err, pluginsvc.ErrConflict) {
		t.Fatalf("stale plan: %v", err)
	}
	params.PlanRevision = plan.PlanRevision
	if _, err = svc.ChangeVisibility(ctx, owner, params); err != nil {
		t.Fatal(err)
	}
	check := func(expected string) {
		t.Helper()
		detail, err := svc.CurrentDetail(ctx, owner, expert.Plugin.ID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Plugin.ListingState != model.PluginListingStateDelisted || detail.Plugin.DependencyStatus != expected {
			t.Fatalf("suspension: %+v", detail.Plugin)
		}
	}
	check("unavailable")
	if _, err = svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: skill.Plugin.ID, ExpectedPublishedVersionID: *ss.PublishedVersionID, Visibility: model.PluginVisibilitySpace}); err != nil {
		t.Fatal(err)
	}
	check("restored")
	teamDetail, err := svc.CurrentDetail(ctx, owner, team.Plugin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if teamDetail.Plugin.ListingState != model.PluginListingStateDelisted || teamDetail.Plugin.DependencyStatus != "unavailable" {
		t.Fatal("team resumed while member remains suspended")
	}
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: expert.Plugin.ID, Version: "1.0.1"}); err != nil {
		t.Fatal(err)
	}
	var markers int
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_dependency_suspensions WHERE plugin_id=?`, expert.Plugin.ID).Scan(&markers); err != nil || markers != 0 {
		t.Fatalf("marker=%d %v", markers, err)
	}
}

func TestConnectorAudienceUsesCurrentConfigRevision(t *testing.T) {
	db := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(db).WithVersionManagement(true)
	seed(t, db, seedPlugin{id: "connector-audience", typ: "connector", visibility: "private", listingState: "published"})
	plan, err := repo.PreviewVisibility(ctx, tenantScope(), "connector-audience", model.PluginVisibilitySpace)
	if err != nil {
		t.Fatal(err)
	}
	p := pluginrepo.VisibilityParams{PluginID: "connector-audience", Visibility: model.PluginVisibilitySpace, ExpectedRevision: "stale", OperatorID: managedOwner().UID}
	if _, err = repo.ChangeVisibility(ctx, tenantScope(), p); !errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatalf("stale config: %v", err)
	}
	if _, err = repo.UpsertReviewPolicy(ctx, tenantScope(), false, "admin", "Admin"); err != nil {
		t.Fatal(err)
	}
	p.ExpectedRevision = plan.Resource.Revision
	out, err := repo.ChangeVisibility(ctx, tenantScope(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Pending {
		t.Fatal("expected manual review")
	}
	if _, err = pluginsvc.New(repo).WithVersionManagement(true).ApproveReview(ctx, managedReviewer(), out.ReviewID); err != nil {
		t.Fatal(err)
	}
	var versions int
	if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=?`, p.PluginID).Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("connector history invented: %d %v", versions, err)
	}
}

func TestAudienceLegacyDelistSuspendsLiveGraphWithinSpace(t *testing.T) {
	db := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(db).WithVersionManagement(true)
	for _, p := range []seedPlugin{
		{id: "legacy-skill", typ: "skill", visibility: "space"},
		{id: "legacy-expert", typ: "expert", visibility: "space"},
		{id: "legacy-team", typ: "expert_team", visibility: "space"},
		{id: "other-space", typ: "expert", visibility: "space", space: "space-b"},
	} {
		seed(t, db, p)
	}
	seedRelation(t, db, "legacy-skill-ref", "legacy-expert", "legacy-skill", "expert_skill")
	seedRelation(t, db, "legacy-member-ref", "legacy-team", "legacy-expert", "expert_team_expert")
	// Even corrupt foreign references must never make one Space's moderation
	// write into another Space's resources.
	seedRelation(t, db, "foreign-ref", "other-space", "legacy-skill", "expert_skill")
	if _, err := repo.DelistPlugin(ctx, reviewerScope(), pluginrepo.DelistParams{PluginID: "legacy-skill", OperatorID: "admin-1", OperatorName: "Admin"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"legacy-expert", "legacy-team"} {
		var listing string
		var markers int
		if err := db.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, id).Scan(&listing); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM plugin_dependency_suspensions WHERE plugin_id=?`, id).Scan(&markers); err != nil {
			t.Fatal(err)
		}
		if listing != "delisted" || markers != 1 {
			t.Fatalf("%s: state=%s suspensions=%d", id, listing, markers)
		}
	}
	var foreign string
	if err := db.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id='other-space'`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if foreign != "published" {
		t.Fatal("cross-Space write")
	}
}

func TestAudienceLegacyEmbeddedWithoutIndependentRelease(t *testing.T) {
	f := legacyAudienceGraph(t, model.PluginTypeExpertTeam, true, model.PluginVisibilityPrivate, true)
	for _, id := range f.embedded {
		if _, err := f.db.Exec("UPDATE plugins SET published_version_id=NULL WHERE plugin_id=?", id); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := f.svc.PreviewVisibility(context.Background(), managedOwner(), f.root.ID, model.PluginVisibilitySpace)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Dependencies) != 0 {
		t.Fatal("embedded nodes must follow their container")
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
				d, e := svc.CurrentDetail(ctx, owner, id)
				if e != nil {
					t.Fatal(e)
				}
				if d.Plugin.ListingState != model.PluginListingStateDelisted || d.Plugin.DependencyStatus != want {
					t.Fatalf("listing=%s dependency=%s", d.Plugin.ListingState, d.Plugin.DependencyStatus)
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
			d, err := svc.CurrentDetail(ctx, owner, id)
			if err != nil || d.Plugin.ListingState != model.PluginListingStatePublished || d.Plugin.DependencyStatus != "" {
				t.Fatalf("republish did not clear suspension: %v", err)
			}
		})
	}
}
