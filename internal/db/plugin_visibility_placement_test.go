package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestVisibilityWideningRepairsDefaultPlacement(t *testing.T) {
	for _, auto := range []bool{true, false} {
		for _, placement := range []string{"missing", "hidden"} {
			t.Run(fmt.Sprintf("auto=%t/%s", auto, placement), func(t *testing.T) {
				database := reviewDB(t)
				ctx, owner := context.Background(), managedOwner()
				repo := pluginrepo.New(database)
				svc := pluginsvc.New(repo).WithVersionManagement(true)
				if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), auto, owner.UID, owner.Name); err != nil {
					t.Fatal(err)
				}
				created, err := svc.Create(ctx, owner, managedWrite("1.0.0", "released", model.PluginVisibilityPrivate))
				if err != nil {
					t.Fatal(err)
				}
				id := created.Plugin.ID
				if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id}); err != nil {
					t.Fatal(err)
				}
				state, err := repo.GetVersionState(ctx, tenantScope(), id)
				if err != nil {
					t.Fatal(err)
				}
				published := *state.PublishedVersionID
				if placement == "missing" {
					_, err = database.Exec(`DELETE FROM plugin_placements WHERE plugin_id=? AND placement_code='default'`, id)
				} else {
					seedCategory(t, database, "keep-category", `["expert"]`)
					_, err = database.Exec(`UPDATE plugin_placements SET visible=0,category_id='keep-category',sort_order=7 WHERE plugin_id=? AND placement_code='default'`, id)
				}
				if err != nil {
					t.Fatal(err)
				}
				member := pluginsvc.Caller{UID: "member", SpaceID: owner.SpaceID}
				list := func(actor pluginsvc.Caller) []model.Plugin {
					t.Helper()
					items, _, err := svc.List(ctx, actor, pluginsvc.ListParams{PlacementCode: "default", Type: model.PluginTypeExpert, Limit: 20})
					if err != nil {
						t.Fatal(err)
					}
					return items
				}
				params := pluginsvc.VisibilityParams{PluginID: id, ExpectedPublishedVersionID: published, Visibility: model.PluginVisibilitySpace}
				result, err := svc.ChangeVisibility(ctx, owner, params)
				if err != nil {
					t.Fatal(err)
				}
				if !auto {
					var visible int
					if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_placements WHERE plugin_id=? AND placement_code='default' AND visible=1`, id).Scan(&visible); err != nil {
						t.Fatal(err)
					}
					if !result.Pending || visible != 0 || len(list(member)) != 0 {
						t.Fatal("pending visibility review changed effective listing")
					}
					if _, err = svc.ApproveReview(ctx, managedReviewer(), result.ReviewID); err != nil {
						t.Fatal(err)
					}
				}
				items := list(member)
				if len(items) != 1 || items[0].ID != id {
					t.Fatalf("space-visible release missing from catalog: %+v", items)
				}
				var count int
				if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_placements WHERE plugin_id=? AND placement_code='default' AND visible=1`, id).Scan(&count); err != nil || count != 1 {
					t.Fatalf("visible placements=%d err=%v", count, err)
				}
				if placement == "hidden" {
					var category string
					var order int
					if err = database.QueryRow(`SELECT category_id,sort_order FROM plugin_placements WHERE plugin_id=? AND placement_code='default'`, id).Scan(&category, &order); err != nil || category != "keep-category" || order != 7 {
						t.Fatalf("placement metadata changed: category=%q order=%d err=%v", category, order, err)
					}
				}
				if len(list(pluginsvc.Caller{UID: "member", SpaceID: "other-space"})) != 0 {
					t.Fatal("repaired placement leaked across Spaces")
				}
				params.Visibility = model.PluginVisibilityPrivate
				if _, err = svc.ChangeVisibility(ctx, owner, params); err != nil {
					t.Fatal(err)
				}
				if len(list(member)) != 0 {
					t.Fatal("visible placement bypassed private audience")
				}
				state, err = repo.GetVersionState(ctx, tenantScope(), id)
				if err != nil || state.PublishedVersionID == nil || *state.PublishedVersionID != published || state.CurrentVersionID == nil || *state.CurrentVersionID != published {
					t.Fatalf("placement repair changed release: %+v %v", state, err)
				}
			})
		}
	}
}
