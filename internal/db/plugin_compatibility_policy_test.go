package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestCompatibilityPublishedReferenceSurvivesDraftRemoval(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	owner := managedOwner()
	s := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true)
	skill, err := s.Create(ctx, owner, managedSkillWrite("1.0.0", "Target Skill", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(ctx, owner, explicitTestPublication(t, database, skill.Plugin.ID)); err != nil {
		t.Fatal(err)
	}
	req := managedWrite("1.0.0", "released expert", model.PluginVisibilityPrivate)
	req.Relations = []pluginsvc.RelationRequest{{Type: "expert_skill", TargetPluginID: skill.Plugin.ID}}
	expert, err := s.Create(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(ctx, owner, explicitTestPublication(t, database, expert.Plugin.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(ctx, owner, expert.Plugin.ID, managedWrite("2.0.0", "unpublished draft without skill", model.PluginVisibilityPrivate)); err != nil {
		t.Fatal(err)
	}
	released, err := s.Detail(ctx, owner, expert.Plugin.ID, true)
	if err != nil || len(released.Relations) != 1 {
		t.Fatalf("release setup failed: %+v %v", released, err)
	}
	if err = s.Delete(ctx, owner, skill.Plugin.ID); !errors.Is(err, pluginsvc.ErrRelationInUse) {
		t.Fatalf("referenced published Skill deletion was not blocked: %v", err)
	}
	// Publishing the draft actually removes the protected edge; the old release
	// remains history and must not turn into a permanent deletion lock.
	if _, err = s.Publish(ctx, owner, explicitTestPublication(t, database, expert.Plugin.ID)); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, owner, skill.Plugin.ID); err != nil {
		t.Fatalf("historical-only reference blocked deletion: %v", err)
	}
	var releases int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=?`, skill.Plugin.ID).Scan(&releases); err != nil || releases != 1 {
		t.Fatalf("deleted Skill release history lost: count=%d err=%v", releases, err)
	}
}

// These fixture mutations represent pre-fix records, not an alternate write API.
// New writes are covered separately; both publish paths must recheck old graphs.
func TestCompatibilityPrivateAudienceRecheckedAtPublication(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, targetKind := range []string{"skill", "expert"} {
			t.Run(fmt.Sprintf("managed=%t/%s", managed, targetKind), func(t *testing.T) {
				database := reviewDB(t)
				ctx := context.Background()
				repo := pluginrepo.New(database)
				svc := pluginsvc.New(repo).WithVersionManagement(managed)
				owner := managedOwner()
				targetReq := managedSkillWrite("1.0.0", "Private Target", model.PluginVisibilityPrivate)
				if targetKind == "expert" {
					targetReq = managedWrite("1.0.0", "private expert", model.PluginVisibilityPrivate)
				}
				target, err := svc.Create(ctx, owner, targetReq)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = svc.Publish(ctx, owner, explicitTestPublication(t, database, target.Plugin.ID)); err != nil {
					t.Fatal(err)
				}
				req := compatibilitySourceWrite(targetKind, "1.0.0")
				req.Relations = []pluginsvc.RelationRequest{{Type: map[string]string{"skill": "expert_skill", "expert": "expert_team_expert"}[targetKind], TargetPluginID: target.Plugin.ID, Data: json.RawMessage(`{"member_key":"leader","is_leader":true}`)}}
				source, err := svc.Create(ctx, owner, req)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = database.Exec(`UPDATE plugins SET visibility='space' WHERE plugin_id=?`, source.Plugin.ID); err != nil {
					t.Fatal(err)
				}
				if _, err = repo.UpsertReviewPolicy(ctx, tenantScope(), false, "reviewer", "Reviewer"); err != nil {
					t.Fatal(err)
				}
				publication, err := svc.Publish(ctx, owner, explicitTestPublication(t, database, source.Plugin.ID))
				if err == nil {
					if publication.Review == nil {
						t.Fatal("invalid private graph published without review")
					}
					_, err = svc.ApproveReview(ctx, managedReviewer(), publication.Review.ID)
				}
				if !errors.Is(err, pluginsvc.ErrNotFound) {
					t.Fatalf("private audience bypass at publication: %v", err)
				}
				var state string
				var published sql.NullString
				if err = database.QueryRow(`SELECT listing_state,published_version_id FROM plugins WHERE plugin_id=?`, source.Plugin.ID).Scan(&state, &published); err != nil {
					t.Fatal(err)
				}
				if state == "published" || published.Valid {
					t.Fatal("failed publication changed release state")
				}
			})
		}
	}
}

func compatibilitySourceWrite(targetKind, version string) pluginsvc.WriteRequest {
	req := managedWrite(version, "source instructions", model.PluginVisibilityPrivate)
	req.Name = "Reference Source"
	req.Manifest = json.RawMessage(strings.ReplaceAll(string(req.Manifest), "Versioned Expert", req.Name))
	if targetKind == "expert" {
		req.Type = model.PluginTypeExpertTeam
		req.Manifest = json.RawMessage(strings.Replace(string(req.Manifest), `"plugin_type":"expert"`, `"plugin_type":"expert_team"`, 1))
	}
	return req
}

func TestCompatibilityPrivateReferenceCannotBeWidenedByUpdate(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true)
	owner := managedOwner()
	skill, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "Private Skill", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	req := compatibilitySourceWrite("skill", "1.0.0")
	req.Relations = []pluginsvc.RelationRequest{{Type: "expert_skill", TargetPluginID: skill.Plugin.ID}}
	source, err := svc.Create(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Visibility = model.PluginVisibilitySpace
	req.Version = "2.0.0"
	if _, err = svc.Update(ctx, owner, source.Plugin.ID, req); !errors.Is(err, pluginsvc.ErrNotFound) {
		t.Fatalf("update widened private reference: %v", err)
	}
	var visibility string
	var versions int
	if err = database.QueryRow(`SELECT visibility,(SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?) FROM plugins WHERE plugin_id=?`, source.Plugin.ID, source.Plugin.ID).Scan(&visibility, &versions); err != nil {
		t.Fatal(err)
	}
	if visibility != "private" || versions != 1 {
		t.Fatal("rejected update changed source or history")
	}
}
