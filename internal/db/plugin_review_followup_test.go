package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	pluginhandler "github.com/Mininglamp-OSS/octo-marketplace/internal/api/handler/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/middleware"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
	"github.com/gin-gonic/gin"
)

func TestLegacyContainerReuploadPreservesOnlyEditableNote(t *testing.T) {
	for _, kind := range []model.PluginType{model.PluginTypeExpert, model.PluginTypeExpertTeam} {
		for _, released := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/released=%v", kind, released), func(t *testing.T) {
				database := reviewDB(t)
				repo := pluginrepo.New(database)
				svc := pluginsvc.New(repo, storage.NewLocal(t.TempDir(), "http://localhost")).WithVersionManagement(true)
				ctx, owner := context.Background(), managedOwner()
				note := "saved container draft note"
				req := managedWrite("1.0.0", "original", model.PluginVisibilityPrivate)
				if released {
					if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), true, owner.UID, owner.Name); err != nil {
						t.Fatal(err)
					}
				}
				req.Type, req.Changelog = kind, &note
				var manifest map[string]any
				if err := json.Unmarshal(req.Manifest, &manifest); err != nil {
					t.Fatal(err)
				}
				manifest["plugin_type"] = kind
				req.Manifest, _ = json.Marshal(manifest)
				d, err := svc.Create(ctx, owner, req)
				if err != nil {
					t.Fatal(err)
				}
				if released {
					if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: d.Plugin.ID, Changelog: note}); err != nil {
						t.Fatal(err)
					}
					// Private publication releases the current snapshot. Widen
					// that same release before organization moderation, without
					// a content review that would preserve a separate draft.
					if _, err = svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: d.Plugin.ID, ExpectedPublishedVersionID: *d.Plugin.CurrentVersionID, Visibility: model.PluginVisibilitySpace}); err != nil {
						t.Fatal(err)
					}
					d, err = svc.CurrentDetail(ctx, owner, d.Plugin.ID)
					if err != nil {
						t.Fatal(err)
					}
					if d.DraftChangelog == nil || *d.DraftChangelog != "" {
						t.Fatal("fixture must start from a formal-release note, not a retained editable draft")
					}
				}
				oldVersion := *d.Plugin.CurrentVersionID
				files := map[string][]byte{"expert.json": []byte(`{"name":"Reuploaded expert","summary":"fixture","instruction":"replacement instructions"}`)}
				if kind == model.PluginTypeExpertTeam {
					files = map[string][]byte{"squad.json": []byte(`{"name":"Reuploaded team","summary":"fixture","leader":"Lead","members":[{"member_key":"lead","name":"Lead","role":"leader","is_leader":true,"instruction":"replacement instructions"}]}`)}
				}
				admin := owner
				admin.IsSystemAdmin = true
				if released {
					// Published legacy containers retain their existing rebuild
					// prohibition. Delisting permits the supported release-baseline case.
					if _, err = svc.ReuploadContainer(ctx, admin, d.Plugin.ID, pluginsvc.ContainerImportParams{Archive: promotionArchive(t, files)}); !errors.Is(err, pluginsvc.ErrConflict) {
						t.Fatalf("published rebuild must remain blocked: %v", err)
					}
					moderator := owner
					moderator.SpaceRole = pluginsvc.SpaceRoleAdmin
					if _, err = svc.Delist(ctx, moderator, pluginsvc.DelistParams{PluginID: d.Plugin.ID}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = svc.ReuploadContainer(ctx, admin, d.Plugin.ID, pluginsvc.ContainerImportParams{Archive: promotionArchive(t, files)}); err != nil {
					t.Fatal(err)
				}
				draft, err := svc.CurrentDetail(ctx, owner, d.Plugin.ID)
				if err != nil {
					t.Fatal(err)
				}
				want := note
				if released {
					want = ""
				}
				if draft.DraftChangelog == nil || *draft.DraftChangelog != want {
					t.Fatalf("note=%v want=%q", draft.DraftChangelog, want)
				}
				if draft.Plugin.CurrentVersionID == nil || *draft.Plugin.CurrentVersionID == oldVersion {
					t.Fatal("reupload did not advance draft")
				}
				var historical string
				if err = database.QueryRow(`SELECT changelog FROM plugin_versions WHERE version_id=?`, oldVersion).Scan(&historical); err != nil || historical != note {
					t.Fatalf("historical note=%q err=%v", historical, err)
				}
			})
		}
	}
}

func TestOmittedDraftNotePreservedOnlyFromUnreleasedSnapshot(t *testing.T) {
	for _, admin := range []bool{false, true} {
		for _, released := range []bool{false, true} {
			t.Run(fmt.Sprintf("admin=%v/released=%v", admin, released), func(t *testing.T) {
				database := reviewDB(t)
				svc := pluginsvc.New(pluginrepo.New(database)).WithVersionManagement(true)
				ctx, owner := context.Background(), managedOwner()
				note := "keep editable note"
				req := managedWrite("1.0.0", "original", model.PluginVisibilityPrivate)
				req.Changelog = &note
				d, err := svc.Create(ctx, owner, req)
				if err != nil {
					t.Fatal(err)
				}
				if released {
					if _, err = svc.Publish(ctx, owner, explicitTestPublication(t, database, d.Plugin.ID)); err != nil {
						t.Fatal(err)
					}
				}
				draft, err := svc.CurrentDetail(ctx, owner, d.Plugin.ID)
				if err != nil {
					t.Fatal(err)
				}
				revision := model.PluginDraftRevision(draft.Plugin)
				next := managedWrite("1.0.1", "changed instructions", model.PluginVisibilityPrivate)
				next.ExpectedDraftRevision = &revision
				save := func(req pluginsvc.WriteRequest) error {
					var err error
					if admin {
						_, err = svc.AdminUpdate(ctx, pluginsvc.Caller{UID: "admin", IsSystemAdmin: true}, d.Plugin.ID, req)
					} else {
						_, err = svc.Update(ctx, owner, d.Plugin.ID, req)
					}
					return err
				}
				if err = save(next); err != nil {
					t.Fatal(err)
				}
				draft, err = svc.CurrentDetail(ctx, owner, d.Plugin.ID)
				if err != nil {
					t.Fatal(err)
				}
				want := note
				if released {
					want = ""
				}
				if draft.DraftChangelog == nil || *draft.DraftChangelog != want {
					t.Fatalf("note=%v want %q", draft.DraftChangelog, want)
				}
				if !strings.Contains(string(draft.Plugin.Package), "changed instructions") {
					t.Fatal("content not saved")
				}
				// Explicit empty still clears, including through AdminUpdate.
				empty := ""
				next.Changelog = &empty
				revision = model.PluginDraftRevision(draft.Plugin)
				if err = save(next); err != nil {
					t.Fatal(err)
				}
				draft, err = svc.CurrentDetail(ctx, owner, d.Plugin.ID)
				if err != nil || draft.DraftChangelog == nil || *draft.DraftChangelog != "" {
					t.Fatalf("clear: %+v %v", draft, err)
				}
			})
		}
	}
}

func TestSameAudienceHTTPDoesNotHidePendingReview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []string{"visibility", "content"} {
		t.Run(kind, func(t *testing.T) {
			database := reviewDB(t)
			repo := pluginrepo.New(database)
			svc := pluginsvc.New(repo).WithVersionManagement(true)
			ctx, owner := context.Background(), managedOwner()
			visibility := model.PluginVisibilityPrivate
			if kind == "content" {
				visibility = model.PluginVisibilitySpace
			}
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), true, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			d, err := svc.Create(ctx, owner, managedWrite("1.0.0", "released", visibility))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.Publish(ctx, owner, explicitTestPublication(t, database, d.Plugin.ID)); err != nil {
				t.Fatal(err)
			}
			state, err := repo.GetVersionState(ctx, tenantScope(), d.Plugin.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.UpsertReviewPolicy(ctx, tenantScope(), false, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			engine := gin.New()
			auth := middleware.NewAuthenticator(false, nil, model.Identity{UID: owner.UID, Name: owner.Name}, owner.SpaceID)
			pluginhandler.New(svc).Register(engine.Group("/api/v1", auth.Handler()))
			send := func(target model.PluginVisibility) *httptest.ResponseRecorder {
				raw, _ := json.Marshal(map[string]string{"visibility": string(target), "expected_published_version_id": *state.PublishedVersionID})
				req := httptest.NewRequest("PATCH", "/api/v1/plugins/"+d.Plugin.ID+"/visibility", strings.NewReader(string(raw)))
				req.Header.Set("Content-Type", "application/json")
				out := httptest.NewRecorder()
				engine.ServeHTTP(out, req)
				return out
			}
			if kind == "visibility" {
				first := send(model.PluginVisibilitySpace)
				if first.Code != 200 || !strings.Contains(first.Body.String(), "pending_review") {
					t.Fatalf("initial: %d %s", first.Code, first.Body.String())
				}
			} else {
				if _, err = svc.Update(ctx, owner, d.Plugin.ID, managedWrite("1.0.1", "upgrade", visibility)); err != nil {
					t.Fatal(err)
				}
				if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: d.Plugin.ID, Version: "1.0.1", Changelog: "upgrade"}); err != nil {
					t.Fatal(err)
				}
			}
			var reviewID string
			if err = database.QueryRow(`SELECT review_id FROM plugin_review_requests WHERE plugin_id=? AND status='pending'`, d.Plugin.ID).Scan(&reviewID); err != nil {
				t.Fatal(err)
			}
			response := send(visibility)
			if response.Code != 409 || !strings.Contains(response.Body.String(), "review_pending") {
				t.Fatalf("same audience: %d %s", response.Code, response.Body.String())
			}
			var pending int
			if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_review_requests WHERE review_id=? AND status='pending' AND deleted_at IS NULL`, reviewID).Scan(&pending); err != nil || pending != 1 {
				t.Fatalf("pending changed: %d %v", pending, err)
			}
			// The request did not cancel or invalidate the existing approval.
			if _, err = svc.ApproveReview(ctx, managedReviewer(), reviewID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
