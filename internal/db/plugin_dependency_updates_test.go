package db

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	repo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	svc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"testing"
)

func TestDirectDependencyUpdateLifecycle(t *testing.T) {
	for _, visibility := range []model.PluginVisibility{model.PluginVisibilityPrivate, model.PluginVisibilitySpace} {
		t.Run(string(visibility), func(t *testing.T) {
			db := reviewDB(t)
			r := repo.New(db)
			s := svc.New(r).WithVersionManagement(true).WithReferences(true)
			ctx := context.Background()
			o := managedOwner()
			_, err := r.UpsertReviewPolicy(ctx, tenantScope(), false, o.UID, o.Name)
			if err != nil {
				t.Fatal(err)
			}
			skill, err := s.Create(ctx, o, managedSkillWrite("1.0.0", "Reminder Skill", model.PluginVisibilitySpace))
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, s, o, skill.Plugin.ID)
			state, err := r.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
			if err != nil {
				t.Fatal(err)
			}
			seed(t, db, seedPlugin{id: "connector-reminder", typ: "connector", visibility: "space", listingState: "published"})
			draft := managedWrite("1.0.0", "expert", visibility)
			draft.Relations = []svc.RelationRequest{{Type: "expert_skill", TargetPluginID: skill.Plugin.ID, TargetVersionID: state.PublishedVersionID}, {Type: "expert_connector", TargetPluginID: "connector-reminder"}}
			expert, err := s.Create(ctx, o, draft)
			if err != nil {
				t.Fatal(err)
			}
			id := expert.Plugin.ID
			publishManaged(t, s, o, id)
			expertVersion, e := r.GetVersionState(ctx, tenantScope(), id)
			if e != nil {
				t.Fatal(e)
			}
			teamWrite := managedWrite("1.0.0", "team", visibility)
			teamWrite.Type = model.PluginTypeExpertTeam
			teamWrite.Name = "Reminder Team"
			teamWrite.Manifest = json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Reminder Team","plugin_type":"expert_team","name":"reminder-team","description":"fixture","labels":[],"examples":[]}`)
			teamWrite.Relations = []svc.RelationRequest{{Type: "expert_team_expert", TargetPluginID: id, TargetVersionID: expertVersion.PublishedVersionID, Data: json.RawMessage(`{"member_key":"leader","is_leader":true,"role":"leader"}`)}}
			team, e := s.Create(ctx, o, teamWrite)
			if e != nil {
				t.Fatal(e)
			}
			publishManaged(t, s, o, team.Plugin.ID)
			preview := func() *repo.DependencyPreview {
				t.Helper()
				out, e := s.DependencyUpdates(ctx, o, id, nil)
				if e != nil {
					t.Fatal(e)
				}
				return out
			}
			if len(preview().Updates) != 0 {
				t.Fatal("unexpected initial updates")
			}
			if _, err = s.Update(ctx, o, skill.Plugin.ID, managedSkillWrite("1.1.0", "Reminder Skill", model.PluginVisibilitySpace)); err != nil {
				t.Fatal(err)
			}
			if len(preview().Updates) != 0 {
				t.Fatal("draft triggered reminder")
			}
			publishManaged(t, s, o, skill.Plugin.ID)
			if _, err = db.Exec("UPDATE plugins SET plugin_hash='new-config' WHERE plugin_id='connector-reminder'"); err != nil {
				t.Fatal(err)
			}
			plan := preview()
			if len(plan.Updates) != 2 {
				t.Fatalf("updates %+v", plan)
			}
			if _, err = s.DependencyUpdates(ctx, svc.Caller{UID: o.UID, SpaceID: "space-b"}, id, nil); !errors.Is(err, svc.ErrNotFound) {
				t.Fatalf("foreign scope: %v", err)
			}
			mine, _, e := s.List(ctx, o, svc.ListParams{Mine: true})
			if e != nil {
				t.Fatal(e)
			}
			red := false
			for _, item := range mine {
				if item.ID == id {
					red = item.HasDependencyUpdates
				}
			}
			if !red {
				t.Fatal("missing owner list reminder")
			}
			teamPlan, e := s.DependencyUpdates(ctx, o, team.Plugin.ID, nil)
			if e != nil || len(teamPlan.Updates) != 0 {
				t.Fatalf("team included indirect updates: %+v %v", teamPlan, e)
			}
			var skillChoice, connector repo.DependencyChoice
			for _, u := range plan.Updates {
				if u.Type == model.PluginTypeSkill {
					skillChoice = u.DependencyChoice
				} else {
					connector = u.DependencyChoice
				}
			}
			if _, err = s.DependencyUpdates(ctx, o, id, []repo.DependencyChoice{skillChoice}); err != nil {
				t.Fatal(err)
			}
			for _, u := range preview().Updates {
				if u.Dismissed != (u.Type == model.PluginTypeSkill) {
					t.Fatal("incorrect dismissal")
				}
			}
			selection := &repo.DependencySelection{DraftRevision: plan.DraftRevision, PublishedID: plan.PublishedID, Choices: []repo.DependencyChoice{connector}}
			pub, err := s.Publish(ctx, o, svc.PublishParams{PluginID: id, Version: "1.1.0", Selection: selection, Changelog: "Accept connector config"})
			if err != nil {
				t.Fatal(err)
			}
			if visibility == model.PluginVisibilitySpace {
				if pub.Review == nil || len(preview().Updates) != 2 {
					t.Fatal("premature acknowledgement")
				}
				if err = s.RejectReview(ctx, managedReviewer(), pub.Review.ID, "retry"); err != nil {
					t.Fatal(err)
				}
				if len(preview().Updates) != 2 {
					t.Fatal("reject changed baseline")
				}
				pub, err = s.Publish(ctx, o, svc.PublishParams{PluginID: id, Version: "1.1.0", Selection: selection})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.ApproveReview(ctx, managedReviewer(), pub.Review.ID); err != nil {
					t.Fatal(err)
				}
			}
			left := preview()
			if len(left.Updates) != 1 || !left.Updates[0].Dismissed || left.Updates[0].TargetID != skill.Plugin.ID {
				t.Fatalf("partial selection: %+v", left)
			}
			selection.Choices = []repo.DependencyChoice{skillChoice}
			if _, err = s.Publish(ctx, o, svc.PublishParams{PluginID: id, Version: "1.2.0", Selection: selection}); !errors.Is(err, svc.ErrConflict) {
				t.Fatalf("stale request: %v", err)
			}
			selection.PublishedID = left.PublishedID
			selection.DraftRevision = left.DraftRevision
			pub, err = s.Publish(ctx, o, svc.PublishParams{PluginID: id, Version: "1.2.0", Selection: selection})
			if err != nil {
				t.Fatal(err)
			}
			if visibility == model.PluginVisibilitySpace {
				if _, err = s.ApproveReview(ctx, managedReviewer(), pub.Review.ID); err != nil {
					t.Fatal(err)
				}
			}
			if len(preview().Updates) != 0 {
				t.Fatal("selected pin not released")
			}
			teamPlan, e = s.DependencyUpdates(ctx, o, team.Plugin.ID, nil)
			if e != nil || len(teamPlan.Updates) != 1 || teamPlan.Updates[0].TargetID != id {
				t.Fatalf("expected direct member update: %+v %v", teamPlan, e)
			}
			memberBefore, e := r.GetVersionState(ctx, tenantScope(), id)
			if e != nil {
				t.Fatal(e)
			}
			teamPub, e := s.Publish(ctx, o, svc.PublishParams{PluginID: team.Plugin.ID, Version: "1.1.0", Selection: &repo.DependencySelection{DraftRevision: teamPlan.DraftRevision, PublishedID: teamPlan.PublishedID, Choices: []repo.DependencyChoice{teamPlan.Updates[0].DependencyChoice}}})
			if e != nil {
				t.Fatal(e)
			}
			if visibility == model.PluginVisibilitySpace {
				if _, e = s.ApproveReview(ctx, managedReviewer(), teamPub.Review.ID); e != nil {
					t.Fatal(e)
				}
			}
			memberAfter, e := r.GetVersionState(ctx, tenantScope(), id)
			if e != nil || *memberBefore.PublishedVersionID != *memberAfter.PublishedVersionID {
				t.Fatal("team update changed member release")
			}
			if _, err = s.Update(ctx, o, skill.Plugin.ID, managedSkillWrite("1.2.0", "Reminder Skill", model.PluginVisibilitySpace)); err != nil {
				t.Fatal(err)
			}
			publishManaged(t, s, o, skill.Plugin.ID)
			newer := preview()
			if len(newer.Updates) != 1 || newer.Updates[0].Dismissed || newer.Updates[0].TargetLabel != "1.2.0" {
				t.Fatalf("new release must remind again: %+v", newer)
			}

			if _, e = db.Exec("UPDATE plugins SET listing_state='delisted' WHERE plugin_id=?", id); e != nil {
				t.Fatal(e)
			}
			if _, e = s.DependencyUpdates(ctx, o, id, nil); !errors.Is(e, svc.ErrConflict) {
				t.Fatalf("delisted preview: %v", e)
			}
			mine, _, e = s.List(ctx, o, svc.ListParams{Mine: true})
			if e != nil {
				t.Fatal(e)
			}
			for _, item := range mine {
				if item.ID == id && item.HasDependencyUpdates {
					t.Fatal("delisted source has update indicator")
				}
			}

		})
	}
}
