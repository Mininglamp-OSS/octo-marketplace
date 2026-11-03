package db

import (
	"context"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestDependencyAdoptionAtomicityAndDurableSkip(t *testing.T) {
	for _, tc := range []struct {
		name       string
		visibility model.PluginVisibility
		auto       bool
	}{
		{"private", model.PluginVisibilityPrivate, true}, {"organization-auto", model.PluginVisibilitySpace, true}, {"organization-manual", model.PluginVisibilitySpace, false},
	} {
		t.Run(tc.name, func(t *testing.T) { testDependencyAdoption(t, tc.visibility, tc.auto) })
	}
}
func testDependencyAdoption(t *testing.T, visibility model.PluginVisibility, auto bool) {
	database := reviewDB(t)
	ctx := context.Background()
	owner := managedOwner()
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), auto, owner.UID, owner.Name); err != nil {
		t.Fatal(err)
	}
	skill, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "adoption-skill", visibility))
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, skill.Plugin.ID)
	first, err := repo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft := managedWrite("1.0.0", "original", visibility)
	draft.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.Plugin.ID, TargetVersionID: first.PublishedVersionID, Type: "expert_skill"}}
	expert, err := svc.Create(ctx, owner, draft)
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, expert.Plugin.ID)
	if _, err = svc.Update(ctx, owner, skill.Plugin.ID, managedSkillWrite("1.1.0", "adoption-skill", visibility)); err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, skill.Plugin.ID)
	updates, err := svc.DependencyUpdates(ctx, owner, expert.Plugin.ID)
	if err != nil || len(updates.Updates) != 1 {
		t.Fatalf("updates=%+v err=%v", updates, err)
	}
	target := updates.Updates[0]
	targets := []model.DependencyUpdateTarget{{PluginID: target.PluginID, TargetVersionID: target.TargetVersionID}}
	if err = svc.SkipDependencyUpdates(ctx, owner, expert.Plugin.ID, targets); err != nil {
		t.Fatal(err)
	}
	// A fresh repository/service observes the persisted skip without losing manual adoption.
	fresh := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true).WithReferences(true)
	reread, err := fresh.DependencyUpdates(ctx, owner, expert.Plugin.ID)
	if err != nil || len(reread.Updates) != 1 || !reread.Updates[0].IsSkipped {
		t.Fatalf("skip=%+v err=%v", reread, err)
	}
	draft = managedWrite("1.1.0", "edited and adopted", visibility)
	draft.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.Plugin.ID, TargetVersionID: &target.TargetVersionID, Type: "expert_skill"}}
	params := pluginsvc.DependencyUpdateSubmitParams{ExpectedPublishedVersionID: updates.PublishedVersionID, ExpectedDraftRevision: "stale", Targets: targets, Draft: draft, Version: "1.1.0", Changelog: "adopt skill"}
	var before int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, expert.Plugin.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.SubmitDependencyUpdates(ctx, owner, expert.Plugin.ID, params); err == nil {
		t.Fatal("stale draft accepted")
	}
	var after int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, expert.Plugin.ID).Scan(&after); err != nil || after != before {
		t.Fatalf("failed adoption wrote snapshot before=%d after=%d err=%v", before, after, err)
	}
	params.ExpectedDraftRevision = updates.DraftRevision
	if _, err = fresh.SubmitDependencyUpdates(ctx, pluginsvc.Caller{UID: owner.UID, SpaceID: "space-b"}, expert.Plugin.ID, params); err == nil {
		t.Fatal("cross-space adoption accepted")
	}
	out, err := fresh.SubmitDependencyUpdates(ctx, owner, expert.Plugin.ID, params)
	if err != nil {
		t.Fatal(err)
	}
	if visibility == model.PluginVisibilitySpace && !auto {
		if out.Review == nil || out.Review.Status != model.ReviewStatusPending {
			t.Fatalf("missing pending review: %+v", out)
		}
		old, err := fresh.Detail(ctx, owner, expert.Plugin.ID, true)
		if err != nil || len(old.Relations) != 1 || old.Relations[0].TargetVersionID == nil || *old.Relations[0].TargetVersionID != *first.PublishedVersionID {
			t.Fatalf("pending review changed live reference: %+v %v", old, err)
		}
		if _, err = fresh.ApproveReview(ctx, managedReviewer(), out.Review.ID); err != nil {
			t.Fatal(err)
		}
	}
	published, err := fresh.Detail(ctx, owner, expert.Plugin.ID, true)
	if err != nil || len(published.Relations) != 1 || published.Relations[0].TargetVersionID == nil || *published.Relations[0].TargetVersionID != target.TargetVersionID {
		t.Fatalf("published=%+v err=%v", published, err)
	}
	if _, err = fresh.SubmitDependencyUpdates(ctx, owner, expert.Plugin.ID, params); err == nil {
		t.Fatal("duplicate stale submission accepted")
	}
	remaining, err := fresh.DependencyUpdates(ctx, owner, expert.Plugin.ID)
	if err != nil || len(remaining.Updates) != 0 {
		t.Fatalf("remaining=%+v err=%v", remaining, err)
	}
}

func TestConnectorConfigAdoption(t *testing.T) {
	for _, mode := range []string{"private", "manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			db := reviewDB(t)
			ctx := context.Background()
			owner := managedOwner()
			repo := pluginrepo.New(db)
			svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
			vis := model.PluginVisibilitySpace
			if mode == "private" {
				vis = model.PluginVisibilityPrivate
			}
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), mode != "manual", owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			seed(t, db, seedPlugin{id: "cfg", typ: "connector", visibility: "space", listingState: "published"})
			draft := managedWrite("1.0.0", "original", vis)
			draft.Relations = []pluginsvc.RelationRequest{{TargetPluginID: "cfg", Type: "expert_connector"}}
			expert, err := svc.Create(ctx, owner, draft)
			if err != nil {
				t.Fatal(err)
			}
			id := expert.Plugin.ID
			publishManaged(t, svc, owner, id)
			updates := func() *model.DependencyUpdates {
				t.Helper()
				u, e := svc.DependencyUpdates(ctx, owner, id)
				if e != nil {
					t.Fatal(e)
				}
				return u
			}
			if len(updates().Updates) != 0 {
				t.Fatal("initial config must be baseline")
			}
			change := func(hash string) {
				if _, e := db.Exec("UPDATE plugins SET plugin_hash=? WHERE plugin_id='cfg'", hash); e != nil {
					t.Fatal(e)
				}
			}
			change("first")
			plan := updates()
			if len(plan.Updates) != 1 || plan.Updates[0].TargetVersionID != "" || plan.Updates[0].TargetConfigRevision == "" {
				t.Fatalf("wrong connector update: %+v", plan)
			}
			selected := []model.DependencyUpdateTarget{{PluginID: "cfg", TargetConfigRevision: plan.Updates[0].TargetConfigRevision}}
			if err = svc.SkipDependencyUpdates(ctx, owner, id, selected); err != nil {
				t.Fatal(err)
			}
			if !updates().Updates[0].IsSkipped {
				t.Fatal("skip not persisted")
			}
			submit := func() (*pluginrepo.DependencyUpdatePublishResult, error) {
				p := updates()
				return svc.SubmitDependencyUpdates(ctx, owner, id, pluginsvc.DependencyUpdateSubmitParams{ExpectedPublishedVersionID: p.PublishedVersionID, ExpectedDraftRevision: p.DraftRevision, Targets: selected, Draft: draft, Version: "1.1.0", Changelog: "Apply config"})
			}
			out, err := submit()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "manual" {
				if len(updates().Updates) != 1 {
					t.Fatal("acknowledged before approval")
				}
				change("second")
				if _, err = svc.ApproveReview(ctx, managedReviewer(), out.Review.ID); err == nil {
					t.Fatal("accepted stale config")
				}
				if err = svc.CancelReview(ctx, owner, out.Review.ID); err != nil {
					t.Fatal(err)
				}
				selected[0].TargetConfigRevision = updates().Updates[0].TargetConfigRevision
				out, err = submit()
				if err != nil {
					t.Fatal(err)
				}
				if _, err = svc.ApproveReview(ctx, managedReviewer(), out.Review.ID); err != nil {
					t.Fatal(err)
				}
			}
			if len(updates().Updates) != 0 {
				t.Fatal("selected revision was not acknowledged")
			}
			change("later")
			p := updates()
			if len(p.Updates) != 1 || p.Updates[0].IsSkipped {
				t.Fatal("new config should remind again")
			}
		})
	}
}
