package db

import (
	"context"
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"testing"
)

func TestVisibilityGroupTransactions(t *testing.T) {
	for _, decision := range []string{"auto", "approve", "reject", "cancel"} {
		t.Run(decision, func(t *testing.T) {
			db := reviewDB(t)
			ctx := context.Background()
			owner := managedOwner()
			repo := pluginrepo.New(db)
			svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), decision == "auto", owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			skill, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "group-skill", model.PluginVisibilityPrivate))
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, svc, owner, skill.Plugin.ID)
			state, err := repo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
			if err != nil {
				t.Fatal(err)
			}
			req := managedWrite("1.0.0", "group expert", model.PluginVisibilityPrivate)
			req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.Plugin.ID, TargetVersionID: state.PublishedVersionID, Type: "expert_skill"}}
			expert, err := svc.Create(ctx, owner, req)
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, svc, owner, expert.Plugin.ID)
			plan, err := svc.VisibilityPlan(ctx, owner, expert.Plugin.ID, model.PluginVisibilitySpace)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Changes) != 2 || len(plan.Blockers) != 0 {
				t.Fatalf("plan %+v", plan)
			}
			result, err := svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: expert.Plugin.ID, Visibility: model.PluginVisibilitySpace, ExpectedPlanRevision: plan.Revision})
			if err != nil {
				t.Fatal(err)
			}
			assertScopes := func(want string) {
				t.Helper()
				for _, id := range []string{skill.Plugin.ID, expert.Plugin.ID} {
					var scope string
					if err := db.QueryRow(`SELECT visibility FROM plugins WHERE plugin_id=?`, id).Scan(&scope); err != nil || scope != want {
						t.Fatalf("id=%s scope=%s err=%v", id, scope, err)
					}
				}
			}
			if decision != "auto" {
				assertScopes("private")
				switch decision {
				case "approve":
					_, err = svc.ApproveReview(ctx, managedReviewer(), result.ReviewID)
				case "reject":
					err = svc.RejectReview(ctx, managedReviewer(), result.ReviewID, "no")
				case "cancel":
					err = svc.CancelReview(ctx, owner, result.ReviewID)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if decision == "auto" || decision == "approve" {
				assertScopes("space")
			} else {
				assertScopes("private")
			}
			var pending int
			if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_review_requests WHERE status='pending'`).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("pending=%d err=%v", pending, err)
			}
			var releases int
			if err = db.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases`).Scan(&releases); err != nil || releases != 2 {
				t.Fatalf("audience change minted a release: %d %v", releases, err)
			}
		})
	}
}

func TestVisibilityHideSuspendsConsumersUntilExplicitPublication(t *testing.T) {
	db := reviewDB(t)
	ctx := context.Background()
	owner := managedOwner()
	repo := pluginrepo.New(db)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	skill, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "hide-skill", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, skill.Plugin.ID)
	state, _ := repo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
	req := managedWrite("1.0.0", "consumer", model.PluginVisibilitySpace)
	req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.Plugin.ID, TargetVersionID: state.PublishedVersionID, Type: "expert_skill"}}
	expert, err := svc.Create(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, expert.Plugin.ID)
	plan, err := svc.VisibilityPlan(ctx, owner, skill.Plugin.ID, model.PluginVisibilityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Affected) != 1 {
		t.Fatalf("impact %+v", plan)
	}
	if _, err = svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: skill.Plugin.ID, Visibility: model.PluginVisibilityPrivate, ExpectedPlanRevision: plan.Revision}); err != nil {
		t.Fatal(err)
	}
	var stateValue string
	if err = db.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, expert.Plugin.ID).Scan(&stateValue); err != nil || stateValue != "delisted" {
		t.Fatalf("consumer state=%s %v", stateValue, err)
	}
	plan, err = svc.VisibilityPlan(ctx, owner, skill.Plugin.ID, model.PluginVisibilitySpace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: skill.Plugin.ID, Visibility: model.PluginVisibilitySpace, ExpectedPlanRevision: plan.Revision}); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, expert.Plugin.ID).Scan(&stateValue); err != nil || stateValue != "delisted" {
		t.Fatal("dependency recovery auto relisted consumer")
	}
}

func TestVisibilityHideIncludesOtherOwnersPrivateConsumerAndRetainsDraftRepair(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	owner := managedOwner()
	other := pluginsvc.Caller{UID: "user-2", Name: "Other", SpaceID: owner.SpaceID}
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	skill, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "shared skill", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, skill.Plugin.ID)
	state, err := repo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := managedWrite("1.0.0", "private consumer", model.PluginVisibilityPrivate)
	req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.Plugin.ID, TargetVersionID: state.PublishedVersionID, Type: "expert_skill"}}
	consumer, err := svc.Create(ctx, other, req)
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, other, consumer.Plugin.ID)
	plan, err := svc.VisibilityPlan(ctx, owner, skill.Plugin.ID, model.PluginVisibilityPrivate)
	if err != nil || len(plan.Affected) != 1 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if plan.Affected[0].Name != "" {
		t.Fatal("private consumer name leaked")
	}
	if _, err = svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: skill.Plugin.ID, Visibility: model.PluginVisibilityPrivate, ExpectedPlanRevision: plan.Revision}); err != nil {
		t.Fatal(err)
	}
	var listing string
	if err = database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, consumer.Plugin.ID).Scan(&listing); err != nil || listing != "delisted" {
		t.Fatalf("listing=%s err=%v", listing, err)
	}
	// Exact opaque pins can be retained while repairing this owner's draft. A
	// different inaccessible pin is still rejected, and publication stays strict.
	draft, err := svc.CurrentDetail(ctx, other, consumer.Plugin.ID)
	if err != nil || len(draft.Relations) != 1 || draft.Relations[0].TargetPluginID != skill.Plugin.ID {
		t.Fatalf("opaque draft ref lost: %+v err=%v", draft, err)
	}
	req.Version = "1.1.0"
	if _, err = svc.Update(ctx, other, consumer.Plugin.ID, req); err != nil {
		t.Fatalf("draft repair blocked: %v", err)
	}
	if _, err = svc.Publish(ctx, other, explicitTestPublication(t, database, consumer.Plugin.ID)); err == nil {
		t.Fatal("unavailable graph republished")
	}
	plan, err = svc.VisibilityPlan(ctx, owner, skill.Plugin.ID, model.PluginVisibilitySpace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: skill.Plugin.ID, Visibility: model.PluginVisibilitySpace, ExpectedPlanRevision: plan.Revision}); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, consumer.Plugin.ID).Scan(&listing); err != nil || listing != "delisted" {
		t.Fatal("recovery auto relisted private consumer")
	}
}

func TestVisibilityGroupConcurrentConfirmationAndRollback(t *testing.T) {
	for _, mode := range []string{"concurrent", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			owner := managedOwner()
			repo := pluginrepo.New(database)
			svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), mode == "rollback", owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			skill, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "atomic-skill", model.PluginVisibilityPrivate))
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, svc, owner, skill.Plugin.ID)
			state, err := repo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
			if err != nil {
				t.Fatal(err)
			}
			req := managedWrite("1.0.0", "atomic expert", model.PluginVisibilityPrivate)
			req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.Plugin.ID, TargetVersionID: state.PublishedVersionID, Type: "expert_skill"}}
			expert, err := svc.Create(ctx, owner, req)
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, svc, owner, expert.Plugin.ID)
			plan, err := svc.VisibilityPlan(ctx, owner, expert.Plugin.ID, model.PluginVisibilitySpace)
			if err != nil {
				t.Fatal(err)
			}
			params := pluginsvc.VisibilityParams{PluginID: expert.Plugin.ID, Visibility: model.PluginVisibilitySpace, ExpectedPlanRevision: plan.Revision}
			if mode == "rollback" {
				// Force a storage failure after the transaction has begun applying scope
				// changes. Both scope writes and all participant review rows must roll back.
				if _, err = database.Exec(`CREATE TRIGGER reject_group_scope BEFORE UPDATE ON plugins FOR EACH ROW BEGIN IF NEW.plugin_id='` + skill.Plugin.ID + `' AND NEW.visibility='space' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='rehearsal failure'; END IF; END`); err != nil {
					t.Fatal(err)
				}
				if _, err = svc.ChangeVisibility(ctx, owner, params); err == nil {
					t.Fatal("forced storage failure accepted")
				}
				var widened, reviews int
				if err = database.QueryRow(`SELECT COUNT(*) FROM plugins WHERE visibility='space'`).Scan(&widened); err != nil {
					t.Fatal(err)
				}
				if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_review_requests WHERE kind='visibility'`).Scan(&reviews); err != nil {
					t.Fatal(err)
				}
				if widened != 0 || reviews != 0 {
					t.Fatalf("partial scope/review commit: widened=%d reviews=%d", widened, reviews)
				}
				return
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func() { <-start; _, err := svc.ChangeVisibility(ctx, owner, params); results <- err }()
			}
			close(start)
			success := 0
			for i := 0; i < 2; i++ {
				if <-results == nil {
					success++
				}
			}
			if success != 1 {
				t.Fatalf("confirmation success count=%d", success)
			}
			var pending int
			if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_review_requests WHERE kind='visibility' AND status='pending'`).Scan(&pending); err != nil || pending != 2 {
				t.Fatalf("pending=%d err=%v", pending, err)
			}
			// Every participant, not just the root, is occupied by the review group.
			edit := managedSkillWrite("1.1.0", "atomic-skill", model.PluginVisibilityPrivate)
			if _, err = svc.Update(ctx, owner, skill.Plugin.ID, edit); err == nil {
				t.Fatal("group participant edit accepted")
			}
		})
	}
}

func TestHideCascadesThroughPausedMemberEvenWhenTeamPinsOlderContent(t *testing.T) {
	for _, v := range []model.PluginVisibility{model.PluginVisibilitySpace, model.PluginVisibilityPrivate} {
		t.Run(string(v), func(t *testing.T) { testHistoricalMemberCascade(t, v, false) })
	}
}
func TestHideCascadesThroughLegacyPublishedConsumers(t *testing.T) {
	testHistoricalMemberCascade(t, model.PluginVisibilitySpace, true)
}
func testHistoricalMemberCascade(t *testing.T, teamVisibility model.PluginVisibility, legacy bool) {
	database := reviewDB(t)
	ctx := context.Background()
	owner := managedOwner()
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	skillIDs := []string{}
	pins := []*string{}
	for _, name := range []string{"cascade-a", "cascade-b"} {
		skill, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", name, model.PluginVisibilitySpace))
		if err != nil {
			t.Fatal(err)
		}
		publishManaged(t, svc, owner, skill.Plugin.ID)
		state, err := repo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
		if err != nil {
			t.Fatal(err)
		}
		skillIDs = append(skillIDs, skill.Plugin.ID)
		pins = append(pins, state.PublishedVersionID)
	}
	memberReq := managedWrite("1.0.0", "member uses A", model.PluginVisibilitySpace)
	memberReq.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skillIDs[0], TargetVersionID: pins[0], Type: "expert_skill"}}
	member, err := svc.Create(ctx, owner, memberReq)
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, member.Plugin.ID)
	memberState, err := repo.GetVersionState(ctx, tenantScope(), member.Plugin.ID)
	if err != nil {
		t.Fatal(err)
	}
	teamReq := managedWrite("1.0.0", "team pins member A", teamVisibility)
	teamReq.Type = model.PluginTypeExpertTeam
	var manifest map[string]any
	if err = json.Unmarshal(teamReq.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["plugin_type"] = "expert_team"
	teamReq.Manifest, _ = json.Marshal(manifest)
	teamReq.Relations = []pluginsvc.RelationRequest{{TargetPluginID: member.Plugin.ID, TargetVersionID: memberState.PublishedVersionID, Type: "expert_team_expert", Data: json.RawMessage(`{"member_key":"leader","role":"leader","is_leader":true}`)}}
	team, err := svc.Create(ctx, owner, teamReq)
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, team.Plugin.ID)
	memberReq.Version = "1.1.0"
	memberReq.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skillIDs[1], TargetVersionID: pins[1], Type: "expert_skill"}}
	if _, err = svc.Update(ctx, owner, member.Plugin.ID, memberReq); err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, member.Plugin.ID)
	directReq := managedWrite("1.0.0", "private direct consumer", model.PluginVisibilityPrivate)
	directReq.Name = "Private Direct Consumer"
	var directManifest map[string]any
	if err = json.Unmarshal(directReq.Manifest, &directManifest); err != nil {
		t.Fatal(err)
	}
	directManifest["plugin_name"] = directReq.Name
	directManifest["name"] = "private-direct-consumer"
	directReq.Manifest, _ = json.Marshal(directManifest)
	directReq.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skillIDs[1], TargetVersionID: pins[1], Type: "expert_skill"}}
	direct, err := svc.Create(ctx, owner, directReq)
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, direct.Plugin.ID)
	if legacy {
		// Published pre-version-management resources remain supported without a
		// formal pointer or release registration; unpinned legacy members use live relations.
		for _, id := range []string{member.Plugin.ID, team.Plugin.ID} {
			if _, err = database.Exec(`UPDATE plugins SET published_version_id=NULL WHERE plugin_id=?`, id); err != nil {
				t.Fatal(err)
			}
			if _, err = database.Exec(`DELETE FROM plugin_version_relation_refs WHERE target_plugin_id=? OR source_plugin_id=?`, id, id); err != nil {
				t.Fatal(err)
			}
			if _, err = database.Exec(`DELETE FROM plugin_version_releases WHERE plugin_id=?`, id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = database.Exec(`UPDATE plugin_relations SET target_version_id=NULL WHERE source_plugin_id=?`, team.Plugin.ID); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := svc.VisibilityPlan(ctx, owner, skillIDs[1], model.PluginVisibilityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Affected) != 2 {
		t.Fatalf("member suspension must also pause its older-pinned team: %+v", plan.Affected)
	}
	if _, err = svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: skillIDs[1], Visibility: model.PluginVisibilityPrivate, ExpectedPlanRevision: plan.Revision}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{member.Plugin.ID, team.Plugin.ID} {
		var listing string
		if err = database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, id).Scan(&listing); err != nil || listing != "delisted" {
			t.Fatalf("listing=%s err=%v", listing, err)
		}
	}
	var directListing string
	if err = database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, direct.Plugin.ID).Scan(&directListing); err != nil || directListing != "published" {
		t.Fatalf("own private direct consumer unnecessarily paused: %s %v", directListing, err)
	}
	plan, err = svc.VisibilityPlan(ctx, owner, skillIDs[1], model.PluginVisibilitySpace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: skillIDs[1], Visibility: model.PluginVisibilitySpace, ExpectedPlanRevision: plan.Revision}); err != nil {
		t.Fatal(err)
	}
	memberReq.Version = "1.2.0"
	if _, err = svc.Update(ctx, owner, member.Plugin.ID, memberReq); err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, member.Plugin.ID)
	var listing string
	if err = database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, team.Plugin.ID).Scan(&listing); err != nil || listing != "delisted" {
		t.Fatal("team auto relisted when member recovered")
	}
}
