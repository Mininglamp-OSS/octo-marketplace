package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

// Reuse the existing audience rule. Both reference kinds, including Admin
// writes, must reject a broader audience rather than disclosing private bytes.
func TestPrivateReferenceAudienceForExpertsAndTeams(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithVersionManagement(true)
	for _, kind := range []string{"skill", "expert"} {
		seed(t, database, seedPlugin{id: kind, typ: kind, visibility: "private", listingState: "published"})
		seedVersionRow(t, database, kind+"-v1", kind, "1")
		if _, err := database.Exec(`INSERT INTO plugin_version_releases VALUES (?,?,'1.0.0','user-1',NOW(3))`, kind, kind+"-v1"); err != nil {
			t.Fatal(err)
		}
	}
	for _, references := range []bool{false, true} {
		repo.WithReferences(references)
		for _, kind := range []string{"skill", "expert"} {
			for i, tc := range []struct {
				visibility, owner, space string
				admin, allowed           bool
			}{
				{"private", "user-1", "space-a", false, true},
				{"space", "user-1", "space-a", false, false},
				{"system", "user-1", "space-a", true, false},
				{"private", "other-owner", "space-a", false, false},
				{"private", "user-1", "other-space", false, false},
				{"space", "user-1", "space-a", true, false},
			} {
				t.Run(fmt.Sprintf("references=%t/%s/%d", references, kind, i), func(t *testing.T) {
					typ, relation := model.PluginTypeExpert, "expert_skill"
					data := json.RawMessage(`{}`)
					if kind == "expert" {
						typ, relation = model.PluginTypeExpertTeam, "expert_team_expert"
						data = json.RawMessage(`{"member_key":"leader","is_leader":true}`)
					}
					p := model.Plugin{ID: fmt.Sprintf("source-%t-%s-%d", references, kind, i), Name: fmt.Sprintf("Source-%t-%s-%d", references, kind, i), Type: typ, OwnerUID: tc.owner, SpaceID: &tc.space, Visibility: model.PluginVisibility(tc.visibility), ListingState: "draft", Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), Status: 1, CreatedByType: "human"}
					version := kind + "-v1"
					edge := model.PluginRelation{SourcePluginID: p.ID, TargetPluginID: kind, TargetPluginType: model.PluginType(kind), TargetVersionID: &version, Type: relation, Data: data, Status: 1}
					_, err := repo.Create(context.Background(), pluginrepo.Scope{CallerUID: tc.owner, SpaceID: tc.space, Admin: tc.admin}, pluginrepo.Mutation{Plugin: p, Relations: []model.PluginRelation{edge}, OperatorID: tc.owner})
					if tc.allowed && err != nil {
						t.Fatal(err)
					}
					if !tc.allowed && !errors.Is(err, pluginrepo.ErrNotFound) {
						t.Fatalf("audience broadened: %v", err)
					}
				})
			}
		}
	}
}

func TestAdminIndependentReferenceAudience(t *testing.T) {
	database := reviewDB(t)
	for _, kind := range []string{"skill", "expert"} {
		for _, visibility := range []string{"space", "system"} {
			id := kind + "-" + visibility
			seed(t, database, seedPlugin{id: id, typ: kind, visibility: visibility, listingState: "published"})
			seedVersionRow(t, database, id+"-v1", id, "1")
			if _, err := database.Exec(`INSERT INTO plugin_version_releases VALUES (?,?,'1.0.0','user-1',NOW(3))`, id, id+"-v1"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, references := range []bool{false, true} {
		repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(references)
		for _, kind := range []string{"skill", "expert"} {
			for i, tc := range []struct {
				source, target, space string
				allowed               bool
			}{
				{"system", "space", "space-a", false}, {"public", "space", "space-a", false},
				{"space", "space", "other-space", false}, {"private", "space", "other-space", false},
				{"space", "space", "space-a", true}, {"private", "space", "space-a", true},
				{"system", "system", "space-a", true}, {"space", "system", "other-space", true},
			} {
				t.Run(fmt.Sprintf("refs=%t/%s/%d", references, kind, i), func(t *testing.T) {
					typ, relation := model.PluginTypeExpert, "expert_skill"
					data := json.RawMessage(`{}`)
					if kind == "expert" {
						typ, relation = model.PluginTypeExpertTeam, "expert_team_expert"
						data = json.RawMessage(`{"member_key":"leader","is_leader":true}`)
					}
					id := fmt.Sprintf("audience-%t-%s-%d", references, kind, i)
					p := model.Plugin{ID: id, Name: id, Type: typ, OwnerUID: "user-1", SpaceID: &tc.space, Visibility: model.PluginVisibility(tc.source), ListingState: "draft", Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), Status: 1, CreatedByType: "human"}
					target := kind + "-" + tc.target
					version := target + "-v1"
					edge := model.PluginRelation{SourcePluginID: id, TargetPluginID: target, TargetPluginType: model.PluginType(kind), TargetVersionID: &version, Type: relation, Data: data, Status: 1}
					_, err := repo.Create(context.Background(), pluginrepo.Scope{CallerUID: "user-1", SpaceID: tc.space, Admin: true}, pluginrepo.Mutation{Plugin: p, Relations: []model.PluginRelation{edge}, OperatorID: "user-1"})
					if tc.allowed && err != nil {
						t.Fatal(err)
					}
					if !tc.allowed && !errors.Is(err, pluginrepo.ErrNotFound) {
						t.Fatalf("audience broadened: %v", err)
					}
				})
			}
		}
	}
}

// Simulate a historical invalid draft: publication revalidates immutable edges,
// rather than trusting a draft written before the audience guard was tightened.
func TestAdminPublicationRejectsSpaceDependencyForPublicRoot(t *testing.T) {
	database := reviewDB(t)
	seed(t, database, seedPlugin{id: "space-skill", typ: "skill", visibility: "space", listingState: "published"})
	for _, visibility := range []string{"system", "public"} {
		id := "root-" + visibility
		vid := id + "-v1"
		seed(t, database, seedPlugin{id: id, typ: "expert", visibility: visibility, listingState: "draft"})
		seedVersionRow(t, database, vid, id, "1")
		rels, err := model.EncodeSnapshotRelations([]model.PluginRelation{{SourcePluginID: id, TargetPluginID: "space-skill", Type: "expert_skill", Status: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(`UPDATE plugin_versions SET relations_json=? WHERE version_id=?`, string(rels), vid); err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(`UPDATE plugins SET current_version_id=?,current_version='1.0.0',space_id=NULL WHERE plugin_id=?`, vid, id); err != nil {
			t.Fatal(err)
		}
		repo := pluginrepo.New(database).WithVersionManagement(true)
		_, err = repo.PublishManaged(context.Background(), pluginrepo.Scope{CallerUID: "admin", Admin: true}, pluginrepo.PublishParams{PluginID: id, ExpectedVersionID: vid, OperatorID: "admin"})
		if !errors.Is(err, pluginrepo.ErrNotFound) {
			t.Fatalf("%s publish=%v", visibility, err)
		}
		var releases int
		var listing string
		if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=?`, id).Scan(&releases); err != nil {
			t.Fatal(err)
		}
		if err = database.QueryRow(`SELECT listing_state FROM plugins WHERE plugin_id=?`, id).Scan(&listing); err != nil {
			t.Fatal(err)
		}
		if releases != 0 || listing != "draft" {
			t.Fatalf("releases=%d listing=%s", releases, listing)
		}
	}
}
