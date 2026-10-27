package db

import (
	"encoding/json"
	"net/http"
	"testing"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

func TestPublishedGraphHTTPMixedPinnedVersions(t *testing.T) {
	database := reviewDB(t)
	dependencyFixture(t, database)
	seed(t, database, seedPlugin{id: "expert2", typ: "expert", visibility: "space", listingState: "published"})
	seedVersionRow(t, database, "expert2-v1", "expert2", "1")
	seedVersionRow(t, database, "skill-v2", "skill", "2")
	if _, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES ('expert2','expert2-v1','1.0.0','user-1',NOW(3)),('skill','skill-v2','2.0.0','user-1',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE plugins SET published_version_id='expert2-v1' WHERE plugin_id='expert2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE plugin_versions SET manifest_json=JSON_OBJECT('plugin_name','Released Skill Two','description','version two'),manifest_hash='version-two-manifest',plugin_hash='version-two-package' WHERE version_id='skill-v2'`); err != nil {
		t.Fatal(err)
	}
	v1, v2 := "skill-v1", "skill-v2"
	e1, e2 := "expert-v1", "expert2-v1"
	setDependencySnapshotRelations(t, database, "team", "team-v1", []model.PluginRelation{
		{ID: "te", SourcePluginID: "team", TargetPluginID: "expert", Type: "expert_team_expert", TargetVersionID: &e1, Status: 1},
		{ID: "te2", SourcePluginID: "team", TargetPluginID: "expert2", Type: "expert_team_expert", TargetVersionID: &e2, Status: 1},
	})
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "member", Name: "Member"}, "space-a")
	svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true)
	pluginhandler.New(svc).Register(engine.Group("/api/v1", auth.Handler()))
	for _, tc := range []struct {
		name          string
		first, second *string
		versions      int
	}{
		{"mixed", &v1, &v2, 2}, {"mixed_reverse", &v2, &v1, 2}, {"shared_version", &v1, &v1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i, source := range []string{"expert", "expert2"} {
				pin := []*string{tc.first, tc.second}[i]
				setDependencySnapshotRelations(t, database, source, []string{e1, e2}[i], []model.PluginRelation{{ID: source + "-skill", SourcePluginID: source, TargetPluginID: "skill", Type: "expert_skill", TargetVersionID: pin, Status: 1}})
			}
			body := graphReviewGET(t, engine, "/api/v1/plugins/detail_graph?plugin_id=team", http.StatusOK)
			var nodes []struct {
				ID           string          `json:"plugin_id"`
				Version      string          `json:"current_version_id"`
				Name         string          `json:"plugin_name"`
				ManifestHash string          `json:"manifest_hash"`
				PluginHash   string          `json:"plugin_hash"`
				Manifest     json.RawMessage `json:"manifest_json"`
				Package      json.RawMessage `json:"plugin_json"`
			}
			if err := json.Unmarshal(body["related_plugins"], &nodes); err != nil {
				t.Fatal(err)
			}
			byVersion := map[string]bool{}
			all := map[string]bool{}
			for _, node := range nodes {
				key := node.ID + "/" + node.Version
				if all[key] {
					t.Fatalf("duplicate snapshot %s", key)
				}
				all[key] = true
				if len(node.Package) != 0 {
					t.Fatal("related snapshot exposes full package")
				}
				if node.ID != "skill" {
					continue
				}
				byVersion[node.Version] = true
				if node.Version == v1 && node.Name != "Released Skill" {
					t.Fatalf("v1 name=%s", node.Name)
				}
				if node.Version == v2 && (node.Name != "Released Skill Two" || node.ManifestHash != "version-two-manifest" || node.PluginHash != "version-two-package") {
					t.Fatalf("v2 reused another snapshot: %+v", node)
				}
			}
			if len(byVersion) != tc.versions {
				t.Fatalf("versions=%v want=%d", byVersion, tc.versions)
			}
			var rels []struct {
				Target  string `json:"target_plugin_id"`
				Version string `json:"target_version_id"`
			}
			if err := json.Unmarshal(body["relations"], &rels); err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, rel := range rels {
				if !all[rel.Target+"/"+rel.Version] {
					t.Fatalf("edge has no version-addressable node: %+v", rel)
				}
				if rel.Target == "skill" {
					count++
				}
			}
			if count != 2 {
				t.Fatalf("skill relations=%d", count)
			}
		})
	}
	// Current Space visibility remains authoritative despite immutable versions.
	foreign := gin.New()
	foreignAuth := middleware.NewAuthenticator(false, nil, model.Identity{UID: "member", Name: "Member"}, "space-b")
	pluginhandler.New(svc).Register(foreign.Group("/api/v1", foreignAuth.Handler()))
	graphReviewGET(t, foreign, "/api/v1/plugins/detail_graph?plugin_id=team", http.StatusNotFound)
}
