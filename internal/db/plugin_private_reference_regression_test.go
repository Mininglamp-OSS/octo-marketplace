package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	"testing"
)

// Reuse the existing audience rule. Both reference kinds, including Admin
// writes, must reject a broader audience rather than disclosing private bytes.
func TestPrivateReferenceAudienceForExpertsAndTeams(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithReferences(true)
	for _, kind := range []string{"skill", "expert"} {
		seed(t, database, seedPlugin{id: kind, typ: kind, visibility: "private", listingState: "published"})
		seedVersionRow(t, database, kind+"-v1", kind, "1")
		if _, err := database.Exec(`INSERT INTO plugin_version_releases VALUES (?,?,'1.0.0','user-1',NOW(3))`, kind, kind+"-v1"); err != nil {
			t.Fatal(err)
		}
	}
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
			t.Run(fmt.Sprintf("%s/%d", kind, i), func(t *testing.T) {
				typ, relation := model.PluginTypeExpert, "expert_skill"
				data := json.RawMessage(`{}`)
				if kind == "expert" {
					typ, relation = model.PluginTypeExpertTeam, "expert_team_expert"
					data = json.RawMessage(`{"member_key":"leader","is_leader":true}`)
				}
				p := model.Plugin{ID: fmt.Sprintf("source-%s-%d", kind, i), Name: fmt.Sprintf("Source-%s-%d", kind, i), Type: typ, OwnerUID: tc.owner, SpaceID: &tc.space, Visibility: model.PluginVisibility(tc.visibility), ListingState: "draft", Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), Status: 1, CreatedByType: "human"}
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
