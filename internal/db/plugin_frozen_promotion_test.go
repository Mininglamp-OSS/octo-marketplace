package db

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

func promotionArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

// Exercise the service path that can legitimately create a standalone expert
// with private bundled skills. A visibility-only fixture mutation models an old
// invalid Team; neither manual nor automatic review may expose its private member.
func TestManagedTeamApprovalRespectsStandalonePrivateExpertBoundary(t *testing.T) {
	for _, auto := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "automatic"}[auto], func(t *testing.T) {
			database := reviewDB(t)
			repo := pluginrepo.New(database)
			svc := pluginsvc.New(repo, storage.NewLocal(t.TempDir(), "http://localhost")).WithVersionManagement(true)
			ctx := context.Background()
			owner := managedOwner()
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), auto, "reviewer", "Reviewer"); err != nil {
				t.Fatal(err)
			}
			expert, err := svc.Create(ctx, owner, managedWrite("1.0.0", "private expert", model.PluginVisibilityPrivate))
			if err != nil {
				t.Fatal(err)
			}
			secret := "PRIVATE_SKILL_CONTENT_NOT_FOR_THE_TEAM"
			skillZip := promotionArchive(t, map[string][]byte{"SKILL.md": []byte("---\nname: private-skill\ndescription: private fixture\n---\n" + secret)})
			manifest, _ := json.Marshal(map[string]any{"name": "Private expert", "summary": "Private fixture", "instruction": "Private instructions", "skills": []map[string]string{{"name": "Private skill", "file": "skills/private.zip"}}})
			archive := promotionArchive(t, map[string][]byte{"expert.json": manifest, "skills/private.zip": skillZip})
			admin := owner
			admin.IsSystemAdmin = true
			expert, err = svc.ReuploadContainer(ctx, admin, expert.Plugin.ID, pluginsvc.ContainerImportParams{Archive: archive})
			if err != nil {
				t.Fatal(err)
			}
			var skillID string
			for _, r := range expert.Relations {
				if r.Type == "expert_skill" {
					skillID = r.TargetPluginID
				}
			}
			if skillID == "" {
				t.Fatal("private bundled skill was not created")
			}
			other := pluginsvc.Caller{UID: "teammate", SpaceID: owner.SpaceID}
			for _, id := range []string{expert.Plugin.ID, skillID} {
				if _, err := svc.Detail(ctx, other, id, false); !errors.Is(err, pluginsvc.ErrNotFound) {
					t.Fatalf("private resource readable before approval: %s %v", id, err)
				}
			}
			teamReq := managedWrite("1.0.0", "team instructions", model.PluginVisibilityPrivate)
			teamReq.Type = model.PluginTypeExpertTeam
			var teamManifest map[string]any
			if err := json.Unmarshal(teamReq.Manifest, &teamManifest); err != nil {
				t.Fatal(err)
			}
			teamManifest["plugin_type"] = "expert_team"
			teamReq.Manifest, _ = json.Marshal(teamManifest)
			teamReq.Relations = []pluginsvc.RelationRequest{{TargetPluginID: expert.Plugin.ID, Type: "expert_team_expert", Data: json.RawMessage(`{"member_key":"lead","role":"leader","is_leader":true}`)}}
			team, err := svc.Create(ctx, owner, teamReq)
			if err != nil {
				t.Fatal(err)
			}
			// New space→private writes are rejected. Preserve the historical graph
			// fixture so publication itself must also enforce the private boundary.
			if _, err = database.Exec(`UPDATE plugins SET visibility='space' WHERE plugin_id=?`, team.Plugin.ID); err != nil {
				t.Fatal(err)
			}
			pub, err := svc.Publish(ctx, owner, explicitTestPublication(t, database, team.Plugin.ID))
			if err == nil {
				if pub.Review == nil {
					t.Fatal("private member graph published without review")
				}
				_, err = svc.ApproveReview(ctx, managedReviewer(), pub.Review.ID)
			}
			if !errors.Is(err, pluginsvc.ErrNotFound) {
				t.Fatalf("publication did not reject standalone private member: %v", err)
			}
			var listing string
			if err := database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, team.Plugin.ID).Scan(&listing); err != nil || listing == "published" {
				t.Fatalf("rejected publication changed Team state: %s %v", listing, err)
			}
			for _, id := range []string{expert.Plugin.ID, skillID} {
				if _, err := svc.Detail(ctx, other, id, false); !errors.Is(err, pluginsvc.ErrNotFound) {
					t.Fatalf("team approval exposed private resource %s: %v", id, err)
				}
				var visibility, listing string
				if err := database.QueryRow(`SELECT visibility,listing_state FROM plugins WHERE plugin_id=?`, id).Scan(&visibility, &listing); err != nil {
					t.Fatal(err)
				}
				if visibility != "private" || listing != "draft" {
					t.Fatalf("private resource %s changed to %s/%s", id, visibility, listing)
				}
			}
		})
	}
}

func TestManagedTeamApprovalPromotesOwnEmbeddedMembers(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo, storage.NewLocal(t.TempDir(), "http://localhost")).WithVersionManagement(true)
	ctx := context.Background()
	owner := managedOwner()
	req := managedWrite("1.0.0", "team draft", model.PluginVisibilityPrivate)
	req.Type = model.PluginTypeExpertTeam
	var m map[string]any
	_ = json.Unmarshal(req.Manifest, &m)
	m["plugin_type"] = "expert_team"
	req.Manifest, _ = json.Marshal(m)
	team, err := svc.Create(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	skillZip := promotionArchive(t, map[string][]byte{"SKILL.md": []byte("---\nname: team-skill\ndescription: team fixture\n---\nTeam skill body")})
	manifest, _ := json.Marshal(map[string]any{"name": "Embedded team", "summary": "Team fixture", "leader": "Lead", "members": []map[string]any{{"member_key": "lead", "name": "Lead", "role": "leader", "is_leader": true, "instruction": "Team member instructions", "skills": []map[string]string{{"name": "Team skill", "file": "skills/team.zip"}}}}})
	archive := promotionArchive(t, map[string][]byte{"squad.json": manifest, "skills/team.zip": skillZip})
	admin := owner
	admin.IsSystemAdmin = true
	team, err = svc.ReuploadContainer(ctx, admin, team.Plugin.ID, pluginsvc.ContainerImportParams{Archive: archive})
	if err != nil {
		t.Fatal(err)
	}
	var children []string
	for _, rel := range team.Relations {
		children = append(children, rel.TargetPluginID)
		d, err := svc.Detail(ctx, owner, rel.TargetPluginID, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, skill := range d.Relations {
			children = append(children, skill.TargetPluginID)
		}
	}
	if len(children) != 2 {
		t.Fatalf("expected member and skill, got %v", children)
	}
	var edited pluginsvc.WriteRequest
	edited.Name = team.Plugin.Name
	edited.Type = team.Plugin.Type
	edited.Visibility = model.PluginVisibilitySpace
	edited.Version = "1.0.0"
	edited.Manifest = team.Plugin.Manifest
	edited.Package = team.Plugin.Package
	edited.Tags = team.Plugin.Tags
	for _, rel := range team.Relations {
		edited.Relations = append(edited.Relations, pluginsvc.RelationRequest{TargetPluginID: rel.TargetPluginID, Type: rel.Type, SortOrder: rel.SortOrder, Data: rel.Data})
	}
	if _, err = svc.Update(ctx, owner, team.Plugin.ID, edited); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpsertReviewPolicy(ctx, tenantScope(), false, "reviewer", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	pub, err := svc.Publish(ctx, owner, explicitTestPublication(t, database, team.Plugin.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApproveReview(ctx, managedReviewer(), pub.Review.ID); err != nil {
		t.Fatal(err)
	}
	other := pluginsvc.Caller{UID: "teammate", SpaceID: owner.SpaceID}
	for _, id := range children {
		d, err := svc.Detail(ctx, other, id, false)
		if err != nil {
			t.Fatalf("own embedded child not visible: %s %v", id, err)
		}
		if d.Plugin.Visibility != model.PluginVisibilitySpace || d.Plugin.ListingState != model.PluginListingStatePublished {
			t.Fatalf("own embedded child not promoted: %+v", d.Plugin)
		}
	}
}
