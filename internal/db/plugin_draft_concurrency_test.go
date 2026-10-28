package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

func draftCASBody(id, revision string, req pluginsvc.WriteRequest) []byte {
	rels := make([]map[string]any, 0, len(req.Relations))
	for _, rel := range req.Relations {
		rels = append(rels, map[string]any{"relation_id": rel.ID, "target_plugin_id": rel.TargetPluginID, "target_version_id": rel.TargetVersionID, "relation_type": rel.Type, "sort_order": rel.SortOrder, "data": rel.Data})
	}
	body, _ := json.Marshal(map[string]any{
		"expected_draft_revision": revision,
		"plugin":                  map[string]any{"plugin_id": id, "plugin_name": req.Name, "plugin_type": req.Type, "visibility": req.Visibility, "publisher": req.Publisher, "version": req.Version, "manifest_json": req.Manifest, "plugin_json": req.Package, "tags": []string{}},
		"relations":               rels,
	})
	return body
}

func TestDraftCASHTTPRejectsStaleMetadataWithoutWrites(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []string{"skill", "expert"} {
		for _, change := range []string{"content", "metadata", "visibility", "version", "relations"} {
			if kind == "skill" && change == "relations" {
				continue
			}
			t.Run(kind+"/"+change, func(t *testing.T) {
				database := reviewDB(t)
				repo := pluginrepo.New(database)
				svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
				ctx, owner := context.Background(), managedOwner()
				original := managedWrite("1.0.0", "original instructions", model.PluginVisibilityPrivate)
				if kind == "skill" {
					original = managedSkillWrite("1.0.0", "CAS Skill", model.PluginVisibilityPrivate)
				}
				created, err := svc.Create(ctx, owner, original)
				if err != nil {
					t.Fatal(err)
				}
				id := created.Plugin.ID
				engine := func(space string) *gin.Engine {
					e := gin.New()
					auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: owner.UID, Name: owner.Name}, space)
					pluginhandler.New(svc).Register(e.Group("/api/v1", auth.Handler()))
					return e
				}
				own, foreign := engine(owner.SpaceID), engine("foreign-space")
				send := func(e *gin.Engine, method, path string, body []byte) *httptest.ResponseRecorder {
					r := httptest.NewRecorder()
					req := httptest.NewRequest(method, path, bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					e.ServeHTTP(r, req)
					return r
				}
				revision := func() string {
					t.Helper()
					r := send(own, http.MethodGet, "/api/v1/plugins/"+id+"/draft", nil)
					var body struct {
						Data struct {
							Revision string `json:"draft_revision"`
						} `json:"data"`
					}
					if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &body) != nil || len(body.Data.Revision) != 64 {
						t.Fatalf("draft response %d %s", r.Code, r.Body.String())
					}
					return body.Data.Revision
				}
				oldToken := revision()
				newer := original
				switch change {
				case "content":
					if kind == "expert" {
						newer = managedWrite("1.0.0", "newer instructions", model.PluginVisibilityPrivate)
					} else {
						newer.Package = json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"newer skill body"}]}`)
					}
				case "relations":
					dependency, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "Pinned CAS Skill", model.PluginVisibilityPrivate))
					if err != nil {
						t.Fatal(err)
					}
					publishManaged(t, svc, owner, dependency.Plugin.ID)
					state, err := repo.GetVersionState(ctx, tenantScope(), dependency.Plugin.ID)
					if err != nil {
						t.Fatal(err)
					}
					newer.Relations = []pluginsvc.RelationRequest{{TargetPluginID: dependency.Plugin.ID, TargetVersionID: state.PublishedVersionID, Type: "expert_skill"}}
				case "version":
					newer.Version = "1.0.1"
				case "metadata":
					newer.Publisher = "new publisher"
				case "visibility":
					newer.Visibility = model.PluginVisibilitySpace
				}
				updated, err := svc.Update(ctx, owner, id, newer)
				if err != nil {
					t.Fatal(err)
				}
				if change == "metadata" && *updated.Plugin.CurrentVersionID != *created.Plugin.CurrentVersionID {
					t.Fatal("metadata fixture unexpectedly created a content version")
				}
				if revision() == oldToken {
					t.Fatal("revision failed to cover changed state")
				}
				evidence := func() string {
					t.Helper()
					var state string
					err := database.QueryRow(`SELECT CONCAT_WS('|',plugin_name,visibility,publisher,manifest_hash,plugin_hash,current_version_id,(SELECT COUNT(*) FROM plugin_versions v WHERE v.plugin_id=p.plugin_id),(SELECT COUNT(*) FROM plugin_audit_logs a WHERE a.plugin_id=p.plugin_id)) FROM plugins p WHERE plugin_id=?`, id).Scan(&state)
					if err != nil {
						t.Fatal(err)
					}
					return state
				}
				before := evidence()
				for _, tc := range []struct {
					e      *gin.Engine
					status int
				}{{own, 409}, {foreign, 404}} {
					r := send(tc.e, http.MethodPost, "/api/v1/plugins/upsert", draftCASBody(id, oldToken, original))
					if r.Code != tc.status {
						t.Fatalf("stale response %d want %d: %s", r.Code, tc.status, r.Body.String())
					}
					if evidence() != before {
						t.Fatal("stale/foreign write changed content or evidence")
					}
				}
				if change == "relations" {
					current, err := svc.CurrentDetail(ctx, owner, id)
					if err != nil {
						t.Fatal(err)
					}
					if len(current.Relations) != 1 || current.Relations[0].TargetVersionID == nil || *current.Relations[0].TargetVersionID != *newer.Relations[0].TargetVersionID {
						t.Fatal("stale save replaced the pinned relation")
					}
					newer.Relations[0].ID = current.Relations[0].ID
				}
				if r := send(foreign, http.MethodGet, "/api/v1/plugins/"+id+"/draft", nil); r.Code != 404 {
					t.Fatalf("foreign draft read: %d", r.Code)
				}
				if r := send(own, http.MethodPost, "/api/v1/plugins/upsert", draftCASBody(id, "malformed", newer)); r.Code != 400 {
					t.Fatalf("malformed CAS: %d", r.Code)
				}
				if evidence() != before {
					t.Fatal("invalid CAS changed state")
				}
				fresh := revision()
				newer.Publisher = "fresh editor"
				r := send(own, http.MethodPost, "/api/v1/plugins/upsert", draftCASBody(id, fresh, newer))
				if r.Code != 200 {
					t.Fatalf("fresh CAS %d %s", r.Code, r.Body.String())
				}
				// Competing metadata-only writes keep the same content version; the row
				// lock must nevertheless admit exactly one reader of this draft revision.
				token := revision()
				var wg sync.WaitGroup
				codes := make(chan int, 2)
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						req := newer
						req.Publisher = fmt.Sprintf("writer-%d", i)
						codes <- send(own, http.MethodPost, "/api/v1/plugins/upsert", draftCASBody(id, token, req)).Code
					}(i)
				}
				wg.Wait()
				close(codes)
				counts := map[int]int{}
				for code := range codes {
					counts[code]++
				}
				if counts[200] != 1 || counts[409] != 1 {
					t.Fatalf("competing writes: %v", counts)
				}
			})
		}
	}
}
