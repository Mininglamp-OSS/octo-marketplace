package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

type legacyAudienceFixture struct {
	db         *sql.DB
	repo       *pluginrepo.Repo
	svc        *pluginsvc.Service
	root       *model.Plugin
	write      pluginsvc.WriteRequest
	embedded   []string
	standalone string
}

func legacyAudienceGraph(t *testing.T, kind model.PluginType, embedded bool, visibility model.PluginVisibility, auto bool) legacyAudienceFixture {
	t.Helper()
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithCapabilityInstaller(&installStubProvisioner{})
	ctx, owner := context.Background(), managedOwner()
	if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), auto, owner.UID, owner.Name); err != nil {
		t.Fatal(err)
	}
	create := func(req pluginsvc.WriteRequest) *model.Plugin {
		d, err := svc.Create(ctx, owner, req)
		if err != nil {
			t.Fatal(err)
		}
		return d.Plugin
	}
	expertWrite := func(name string, typ model.PluginType, vis model.PluginVisibility) pluginsvc.WriteRequest {
		r := managedWrite("1.0.0", "legacy instructions", vis)
		r.Name, r.Type = name, typ
		r.Manifest, _ = json.Marshal(map[string]any{"$schema": "cowork-plugin-manifest-2.0.json", "plugin_name": name, "plugin_type": typ, "name": name, "description": "fixture", "labels": []string{}, "examples": []string{}})
		return r
	}
	childVisibility := model.PluginVisibilitySpace
	if embedded {
		childVisibility = visibility
	}
	skill := create(managedSkillWrite("1.0.0", "legacy-skill", childVisibility))
	publishManaged(t, svc, owner, skill.ID)
	f := legacyAudienceFixture{db: database, repo: repo, svc: svc}
	req := expertWrite("legacy-root", kind, visibility)
	if kind == model.PluginTypeExpert {
		req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.ID, Type: "expert_skill"}}
		f.embedded = []string{skill.ID}
	} else {
		memberReq := expertWrite("legacy-member", model.PluginTypeExpert, childVisibility)
		memberReq.Relations = []pluginsvc.RelationRequest{{TargetPluginID: skill.ID, Type: "expert_skill"}}
		member := create(memberReq)
		req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: member.ID, Type: "expert_team_expert", Data: json.RawMessage(`{"member_key":"leader","is_leader":true,"role":"leader"}`)}}
		f.embedded = []string{member.ID, skill.ID}
		publishManaged(t, svc, owner, member.ID)
	}

	// A standalone catalog resource referenced by this container stays public.
	if kind == model.PluginTypeExpert {
		extra := create(managedSkillWrite("1.0.0", "standalone-skill", model.PluginVisibilitySpace))
		publishManaged(t, svc, owner, extra.ID)
		f.standalone = extra.ID
		req.Relations = append(req.Relations, pluginsvc.RelationRequest{TargetPluginID: extra.ID, Type: "expert_skill"})
	} else {
		extra := create(expertWrite("standalone-expert", model.PluginTypeExpert, model.PluginVisibilitySpace))
		publishManaged(t, svc, owner, extra.ID)
		f.standalone = extra.ID
		req.Relations = append(req.Relations, pluginsvc.RelationRequest{TargetPluginID: extra.ID, Type: "expert_team_expert", Data: json.RawMessage(`{"member_key":"extra","is_leader":false,"role":"member"}`)})
	}
	f.root, f.write = create(req), req
	if embedded {
		for _, id := range f.embedded {
			if _, err := database.Exec(`UPDATE plugins SET is_embedded=1 WHERE plugin_id=?`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	publishManaged(t, svc, owner, f.root.ID)
	return f
}

func TestVisibilityNarrowingRevokesOwnedEmbeddedGraph(t *testing.T) {
	for _, kind := range []model.PluginType{model.PluginTypeExpert, model.PluginTypeExpertTeam} {
		for _, newerDraft := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/newer-draft=%t", kind, newerDraft), func(t *testing.T) {
				f := legacyAudienceGraph(t, kind, true, model.PluginVisibilitySpace, true)
				ctx, owner := context.Background(), managedOwner()
				member := pluginsvc.Caller{UID: "member", SpaceID: owner.SpaceID}
				state, err := f.repo.GetVersionState(ctx, tenantScope(), f.root.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range f.embedded {
					if _, err := f.svc.Detail(ctx, member, id, true); err != nil {
						t.Fatalf("fixture child not readable: %v", err)
					}
				}
				if newerDraft {
					req := f.write
					req.Version, req.Relations = "2.0.0", nil
					if _, err := f.svc.Update(ctx, owner, f.root.ID, req); err != nil {
						t.Fatal(err)
					}
				}
				// Even malformed live legacy edges must not mutate another
				// owner's or Space's embedded resources during narrowing.
				foreignKind, edgeType := "skill", "expert_skill"
				if kind == model.PluginTypeExpertTeam {
					foreignKind, edgeType = "expert", "expert_team_expert"
				}
				for _, foreign := range []seedPlugin{
					{id: "foreign-child", typ: foreignKind, visibility: "space", listingState: "published", embedded: true, owner: "other", space: "other-space"},
					{id: "other-owner-child", typ: foreignKind, visibility: "space", listingState: "published", embedded: true, owner: "other", space: owner.SpaceID},
				} {
					seed(t, f.db, foreign)
					seedRelation(t, f.db, "edge-"+foreign.id, f.root.ID, foreign.id, edgeType)
				}
				var versionsBefore, releasesBefore int
				if err := f.db.QueryRow(`SELECT (SELECT COUNT(*) FROM plugin_versions),(SELECT COUNT(*) FROM plugin_version_releases)`).Scan(&versionsBefore, &releasesBefore); err != nil {
					t.Fatal(err)
				}
				_, err = f.svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: f.root.ID, ExpectedPublishedVersionID: *state.PublishedVersionID, Visibility: model.PluginVisibilityPrivate})
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range append([]string{f.root.ID}, f.embedded...) {
					if _, err := f.svc.Detail(ctx, member, id, true); !errors.Is(err, pluginsvc.ErrNotFound) {
						t.Errorf("private detail %s: %v", id, err)
					}
					if _, err := f.svc.DetailGraph(ctx, member, id); !errors.Is(err, pluginsvc.ErrNotFound) {
						t.Errorf("private graph %s: %v", id, err)
					}
					if _, err := f.svc.Install(ctx, member, id, pluginsvc.InstallParams{WorkspaceID: "workspace", RuntimeID: "runtime"}); !errors.Is(err, pluginsvc.ErrNotFound) {
						t.Errorf("private install %s: %v", id, err)
					}
					if _, err := f.svc.Detail(ctx, owner, id, true); err != nil {
						t.Errorf("owner lost access: %v", err)
					}
				}
				for _, id := range f.embedded {
					p, err := f.repo.Get(ctx, tenantScope(), id)
					if err != nil {
						t.Fatal(err)
					}
					if p.Type == model.PluginTypeSkill {
						if _, err := f.svc.SkillMarkdown(ctx, member, id); !errors.Is(err, pluginsvc.ErrNotFound) {
							t.Errorf("private download: %v", err)
						}
					}
				}
				for _, id := range []string{"foreign-child", "other-owner-child"} {
					var vis string
					if err := f.db.QueryRow(`SELECT visibility FROM plugins WHERE plugin_id=?`, id).Scan(&vis); err != nil || vis != "space" {
						t.Fatalf("foreign audience changed: %s %v", vis, err)
					}
				}
				if _, err := f.svc.Detail(ctx, member, f.standalone, true); err != nil {
					t.Fatalf("standalone reference was changed: %v", err)
				}
				var versionsAfter, releasesAfter int
				if err := f.db.QueryRow(`SELECT (SELECT COUNT(*) FROM plugin_versions),(SELECT COUNT(*) FROM plugin_version_releases)`).Scan(&versionsAfter, &releasesAfter); err != nil {
					t.Fatal(err)
				}
				if versionsBefore != versionsAfter || releasesBefore != releasesAfter {
					t.Fatal("audience change created a content version/release")
				}
				// A newer editable topology must not invalidate re-widening the
				// unchanged released embedded graph.
				if _, err := f.svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: f.root.ID, ExpectedPublishedVersionID: *state.PublishedVersionID, Visibility: model.PluginVisibilitySpace}); err != nil {
					t.Fatalf("restore released graph: %v", err)
				}

			})
		}
	}
}

func TestLegacyVisibilityWideningUsesPublicationRollout(t *testing.T) {
	for _, kind := range []model.PluginType{model.PluginTypeExpert, model.PluginTypeExpertTeam} {
		for _, embedded := range []bool{false, true} {
			for _, auto := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/embedded=%t/auto=%t", kind, embedded, auto), func(t *testing.T) {
					f := legacyAudienceGraph(t, kind, embedded, model.PluginVisibilityPrivate, auto)
					ctx, owner := context.Background(), managedOwner()
					state, err := f.repo.GetVersionState(ctx, tenantScope(), f.root.ID)
					if err != nil {
						t.Fatal(err)
					}
					out, err := f.svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: f.root.ID, ExpectedPublishedVersionID: *state.PublishedVersionID, Visibility: model.PluginVisibilitySpace})
					if err != nil {
						t.Fatal(err)
					}
					member := pluginsvc.Caller{UID: "member", SpaceID: owner.SpaceID}
					if !auto {
						if _, err := f.svc.Detail(ctx, member, f.root.ID, true); !errors.Is(err, pluginsvc.ErrNotFound) {
							t.Fatal("pending review exposed root")
						}
						if _, err := f.svc.ApproveReview(ctx, managedReviewer(), out.ReviewID); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := f.svc.DetailGraph(ctx, member, f.root.ID); err != nil {
						t.Fatal(err)
					}
					for _, id := range f.embedded {
						if _, err := f.svc.Detail(ctx, member, id, true); err != nil {
							t.Fatalf("released child not readable: %v", err)
						}
					}
					stateAfter, err := f.repo.GetVersionState(ctx, tenantScope(), f.root.ID)
					if err != nil || *state.PublishedVersionID != *stateAfter.PublishedVersionID {
						t.Fatal("widening changed release")
					}
				})
			}
		}
	}
}
