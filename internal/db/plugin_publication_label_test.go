package db

import (
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestPublicationSelectsLabelIndependentOfDraft(t *testing.T) {
	for _, typ := range []model.PluginType{model.PluginTypeSkill, model.PluginTypeExpert, model.PluginTypeExpertTeam} {
		for _, visibility := range []model.PluginVisibility{model.PluginVisibilityPrivate, model.PluginVisibilitySpace} {
			t.Run(string(typ)+"/"+string(visibility), func(t *testing.T) {
				database := reviewDB(t)
				ctx, owner := context.Background(), managedOwner()
				repo := pluginrepo.New(database)
				svc := pluginsvc.New(repo).WithVersionManagement(true)
				if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), false, owner.UID, owner.Name); err != nil {
					t.Fatal(err)
				}
				req := managedWrite("1.0.0", "saved instructions", visibility)
				if typ == model.PluginTypeSkill {
					req = managedSkillWrite("1.0.0", "Publication Skill", visibility)
				}
				if typ == model.PluginTypeExpertTeam {
					req = compatibilitySourceWrite("expert", "1.0.0")
					req.Visibility = visibility
				}
				created, err := svc.Create(ctx, owner, req)
				if err != nil {
					t.Fatal(err)
				}
				id := created.Plugin.ID
				publishManaged(t, svc, owner, id)
				// A provisional draft label must not determine the formal release.
				req.Version = "9.9.9"
				draft, err := svc.Update(ctx, owner, id, req)
				if err != nil {
					t.Fatal(err)
				}
				draftID := *draft.Plugin.CurrentVersionID
				original, err := svc.ManagedVersion(ctx, owner, id, draftID, false)
				if err != nil {
					t.Fatal(err)
				}
				result, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id, Version: "1.3.0"})
				if err != nil {
					t.Fatal(err)
				}
				if visibility == model.PluginVisibilitySpace {
					if result.Review == nil || result.Review.Version != "1.3.0" {
						t.Fatal("manual review lost the requested label")
					}
					if _, err = svc.ApproveReview(ctx, managedReviewer(), result.Review.ID); err != nil {
						t.Fatal(err)
					}
				} else if result.Review != nil || result.Plugin.Plugin.CurrentVersion == nil || *result.Plugin.Plugin.CurrentVersion != "1.3.0" {
					t.Fatal("private publication response lost the requested label")
				}
				state, err := repo.GetVersionState(ctx, tenantScope(), id)
				if err != nil || state.PublishedVersionID == nil || *state.PublishedVersionID == draftID {
					t.Fatalf("relabel did not append a release snapshot: %+v %v", state, err)
				}
				release, err := svc.ManagedVersion(ctx, owner, id, *state.PublishedVersionID, false)
				if err != nil {
					t.Fatal(err)
				}
				if release.Release == nil || release.Release.VersionLabel != "1.3.0" || release.DeclaredVersion == nil || *release.DeclaredVersion != "1.3.0" {
					t.Fatal("formal release label differs from requested label")
				}
				if string(release.Manifest) != string(original.Manifest) || string(release.Package) != string(original.Package) || string(release.AttachmentKeys) != string(original.AttachmentKeys) || string(release.Artifacts) != string(original.Artifacts) || string(release.Relations) != string(original.Relations) {
					t.Fatal("relabel changed selected content/files/pins")
				}
				unchanged, err := svc.ManagedVersion(ctx, owner, id, draftID, false)
				if err != nil || unchanged.DeclaredVersion == nil || *unchanged.DeclaredVersion != "9.9.9" || unchanged.Release != nil {
					t.Fatalf("old draft snapshot was mutated: %+v %v", unchanged, err)
				}
				// Rejected duplicates/regressions must leave both pointers and history intact.
				var before int
				if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, id).Scan(&before); err != nil {
					t.Fatal(err)
				}
				for _, label := range []string{"1.0.0", "1.2.0"} {
					_, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id, Version: label})
					if !errors.Is(err, pluginsvc.ErrLabelTaken) && !errors.Is(err, pluginsvc.ErrVersionRegressed) {
						t.Fatalf("label %s: %v", label, err)
					}
				}
				after, err := repo.GetVersionState(ctx, tenantScope(), id)
				if err != nil || *after.CurrentVersionID != *state.CurrentVersionID || *after.PublishedVersionID != *state.PublishedVersionID {
					t.Fatal("refused publication changed version pointers")
				}
				var count int
				if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id=?`, id).Scan(&count); err != nil || count != before {
					t.Fatal("refused publication left a new history row")
				}
				if visibility == model.PluginVisibilityPrivate {
					_, err = repo.PublishManaged(ctx, tenantScope(), pluginrepo.PublishParams{PluginID: id, ExpectedVersionID: draftID, Version: "1.4.0", OperatorID: owner.UID})
					if !errors.Is(err, pluginrepo.ErrConflict) {
						t.Fatalf("stale draft publication: %v", err)
					}
				}
			})
		}
	}
}

func TestPrivateReleaseRelabelRetainsPinnedSkillVersion(t *testing.T) {
	database := reviewDB(t)
	ctx, owner := context.Background(), managedOwner()
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	skillReq := managedSkillWrite("1.0.0", "Pinned Skill", model.PluginVisibilityPrivate)
	skill, err := svc.Create(ctx, owner, skillReq)
	if err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, skill.Plugin.ID)
	pinned := *skill.Plugin.CurrentVersionID
	req := managedWrite("9.9.9", "Expert draft", model.PluginVisibilityPrivate)
	req.Relations = []pluginsvc.RelationRequest{{Type: "expert_skill", TargetPluginID: skill.Plugin.ID, TargetVersionID: &pinned}}
	expert, err := svc.Create(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	skillReq.Version = "2.0.0"
	if _, err = svc.Update(ctx, owner, skill.Plugin.ID, skillReq); err != nil {
		t.Fatal(err)
	}
	publishManaged(t, svc, owner, skill.Plugin.ID)
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: expert.Plugin.ID, Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	state, err := repo.GetVersionState(ctx, tenantScope(), expert.Plugin.ID)
	if err != nil || state.PublishedVersionID == nil {
		t.Fatalf("release: %+v %v", state, err)
	}
	v, err := svc.ManagedVersion(ctx, owner, expert.Plugin.ID, *state.PublishedVersionID, false)
	if err != nil {
		t.Fatal(err)
	}
	rels, err := model.DecodeSnapshotRelations(v.Relations)
	if err != nil || len(rels) != 1 || rels[0].TargetVersionID == nil || *rels[0].TargetVersionID != pinned {
		t.Fatalf("pin changed: %+v %v", rels, err)
	}
	var indexed string
	if err := database.QueryRow(`SELECT target_version_id FROM plugin_version_relation_refs WHERE source_version_id=? AND target_plugin_id=?`, v.ID, skill.Plugin.ID).Scan(&indexed); err != nil || indexed != pinned {
		t.Fatalf("reference index changed: %s %v", indexed, err)
	}
}

func TestBaselineAdoptionDoesNotIgnoreRequestedReleaseLabel(t *testing.T) {
	for _, visibility := range []model.PluginVisibility{model.PluginVisibilityPrivate, model.PluginVisibilitySpace} {
		t.Run(string(visibility), func(t *testing.T) {
			database := reviewDB(t)
			ctx, owner := context.Background(), managedOwner()
			repo := pluginrepo.New(database)
			legacy := pluginsvc.New(repo)
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), true, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			d, err := legacy.Create(ctx, owner, managedWrite("1.0.0", "Legacy approved content", visibility))
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, legacy, owner, d.Plugin.ID)
			svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true)
			if _, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: d.Plugin.ID, Version: "2.0.0"}); err != nil {
				t.Fatal(err)
			}
			state, err := repo.GetVersionState(ctx, tenantScope(), d.Plugin.ID)
			if err != nil || state.PublishedVersionID == nil {
				t.Fatalf("state: %+v %v", state, err)
			}
			v, err := svc.ManagedVersion(ctx, owner, d.Plugin.ID, *state.PublishedVersionID, false)
			if err != nil || v.Release == nil || v.Release.VersionLabel != "2.0.0" {
				t.Fatalf("requested label ignored by baseline adoption: %+v %v", v, err)
			}
		})
	}
}
