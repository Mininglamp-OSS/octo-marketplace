package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/router"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/gin-gonic/gin"
)

// Exercise the production HTTP adapter: Service-only tests cannot catch a pin
// being lost while translating the review request into service parameters.
func TestReviewHTTPRecoversUnreleasedExpertWithExactPins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, auto := range []bool{false, true} {
		t.Run(fmt.Sprintf("auto=%t", auto), func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			owner := managedOwner()
			repo := pluginrepo.New(database)
			modern := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), true, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			skill, err := modern.Create(ctx, owner, managedSkillWrite("1.0.0", "Review pin fixture", model.PluginVisibilitySpace))
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, modern, owner, skill.Plugin.ID)
			skillState, err := repo.GetVersionState(ctx, tenantScope(), skill.Plugin.ID)
			if err != nil || skillState.PublishedVersionID == nil {
				t.Fatalf("skill release: %+v %v", skillState, err)
			}
			pin := *skillState.PublishedVersionID
			// Keep an unreleased draft to prove an existing snapshot is insufficient.
			draft, err := modern.Update(ctx, owner, skill.Plugin.ID, managedSkillWrite("1.0.1", "Review pin fixture", model.PluginVisibilitySpace))
			if err != nil {
				t.Fatal(err)
			}
			legacy := pluginsvc.New(pluginrepo.New(database))
			req := managedWrite("1.0.0", "Historical unpinned expert", model.PluginVisibilitySpace)
			req.Relations = []pluginsvc.RelationRequest{{Type: "expert_skill", TargetPluginID: skill.Plugin.ID}}
			expert, err := legacy.Create(ctx, owner, req)
			if err != nil {
				t.Fatal(err)
			}
			publishManaged(t, legacy, owner, expert.Plugin.ID)
			before, err := repo.GetVersionState(ctx, tenantScope(), expert.Plugin.ID)
			if err != nil || before.PublishedVersionID != nil {
				t.Fatalf("historical baseline: %+v %v", before, err)
			}
			if _, err = repo.UpsertReviewPolicy(ctx, tenantScope(), auto, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			newRouter := func(space string) http.Handler {
				identity := model.Identity{UID: owner.UID, Name: owner.Name, Spaces: []string{space}, SpaceRoles: map[string]int{space: model.SpaceRoleAdmin}}
				return router.Public(database, middleware.NewAuthenticator(false, nil, identity, space), middleware.NewAdminAuthenticator(false, nil, identity),
					router.StorageConfig{Driver: "local", LocalDir: dir, BaseURL: "http://localhost", MaxMB: 2, PluginVersionManagementEnabled: true, PluginReferencesEnabled: true, PluginPinnedInstallationEnabled: true, InstallationHMACKeyID: "test", InstallationHMACKeys: map[string]string{"test": "synthetic-review-http-key-32-bytes"}},
					nil, nil, router.ParseConfig{WorkerPoolSize: 1}, nil, router.ReviewConfig{})
			}
			send := func(h http.Handler, path string, body any, status int) map[string]json.RawMessage {
				t.Helper()
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest("POST", path, bytes.NewReader(raw))
				request.Header.Set("Content-Type", "application/json")
				request.RemoteAddr = "127.0.0.1:12345"
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, request)
				if rec.Code != status {
					t.Fatalf("POST %s status=%d want=%d body=%s", path, rec.Code, status, rec.Body)
				}
				var result map[string]json.RawMessage
				if err = json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			h := newRouter(owner.SpaceID)
			edge := map[string]any{"relation_type": "expert_skill", "target_plugin_id": skill.Plugin.ID, "target_version_id": pin}
			body := map[string]any{"plugin_id": expert.Plugin.ID, "version": "1.0.1", "manifest_json": expert.Plugin.Manifest, "plugin_json": expert.Plugin.Package, "relations": []any{edge}}
			send(newRouter("space-b"), "/api/v1/plugins/review_requests", body, 404)
			for _, invalid := range []any{nil, *draft.Plugin.CurrentVersionID} {
				edge["target_version_id"] = invalid
				result := send(h, "/api/v1/plugins/review_requests", body, 400)
				var failure struct {
					Code    string `json:"code"`
					Details struct {
						Reason string `json:"reason"`
					} `json:"details"`
				}
				if err = json.Unmarshal(result["error"], &failure); err != nil || failure.Code != "VALIDATION_ERROR" || failure.Details.Reason != "released_version_required" {
					t.Fatalf("invalid pin error=%s err=%v", result["error"], err)
				}
			}
			edge["target_version_id"] = pin
			result := send(h, "/api/v1/plugins/review_requests", body, 200)
			var review struct {
				ID     string `json:"review_id"`
				Status string `json:"status"`
			}
			if err = json.Unmarshal(result["data"], &review); err != nil || review.ID == "" {
				t.Fatalf("review=%s err=%v", result["data"], err)
			}
			detail := httptest.NewRecorder()
			h.ServeHTTP(detail, httptest.NewRequest("GET", "/api/v1/plugins/review_requests/"+review.ID, nil))
			var frozen struct {
				Data struct {
					Relations []struct {
						Pin *string `json:"target_version_id"`
					} `json:"frozen_relations"`
				} `json:"data"`
			}
			if err = json.Unmarshal(detail.Body.Bytes(), &frozen); detail.Code != 200 || err != nil || len(frozen.Data.Relations) != 1 || frozen.Data.Relations[0].Pin == nil || *frozen.Data.Relations[0].Pin != pin {
				t.Fatalf("frozen review detail lost pin: status=%d body=%s err=%v", detail.Code, detail.Body, err)
			}
			if !auto {
				send(h, "/api/v1/plugins/review_requests/"+review.ID+"/approve", nil, 200)
			} else if review.Status != "approved" {
				t.Fatalf("auto status=%s", review.Status)
			}
			after, err := repo.GetVersionState(ctx, tenantScope(), expert.Plugin.ID)
			if err != nil || after.PublishedVersionID == nil {
				t.Fatalf("release=%+v err=%v", after, err)
			}
			released, err := modern.ManagedVersion(ctx, owner, expert.Plugin.ID, *after.PublishedVersionID, false)
			if err != nil || released.Release == nil || released.Release.VersionLabel != "1.0.1" {
				t.Fatalf("released snapshot=%+v err=%v", released, err)
			}
			edges, err := model.DecodeSnapshotRelations(released.Relations)
			if err != nil || len(edges) != 1 || edges[0].TargetVersionID == nil || *edges[0].TargetVersionID != pin {
				t.Fatalf("frozen pins=%+v err=%v", edges, err)
			}
			var indexed string
			if err = database.QueryRow(`SELECT target_version_id FROM plugin_version_relation_refs WHERE source_plugin_id=? AND source_version_id=?`, expert.Plugin.ID, *after.PublishedVersionID).Scan(&indexed); err != nil || indexed != pin {
				t.Fatalf("index pin=%s err=%v", indexed, err)
			}
			var oldPins int
			if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=? AND version_id=?`, expert.Plugin.ID, *before.CurrentVersionID).Scan(&oldPins); err != nil || oldPins != 0 {
				t.Fatalf("historical snapshot was released: %d %v", oldPins, err)
			}
		})
	}
}
