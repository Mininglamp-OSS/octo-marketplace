package db

import (
	"context"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func TestPluginKeywordSearchesNameAndManifestDescription(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	scope := tenantScope()

	seed(t, database, seedPlugin{
		id:           "name-hit",
		visibility:   "private",
		listingState: "published",
		manifest:     `{"description":"unrelated"}`,
	})
	seed(t, database, seedPlugin{
		id:           "description-hit",
		visibility:   "private",
		listingState: "published",
		manifest:     `{"description":"DescriptionOnlyNeedle and 100%_done!"}`,
	})
	seed(t, database, seedPlugin{
		id:           "wildcard-decoy",
		visibility:   "private",
		listingState: "published",
		manifest:     `{"description":"100-anythingXdone?"}`,
	})
	if _, err := database.Exec(`UPDATE plugins SET plugin_name='NameOnlyNeedle' WHERE plugin_id='name-hit'`); err != nil {
		t.Fatal(err)
	}

	assertPluginSearchIDs(t, repo, scope, "NameOnlyNeedle", "name-hit")
	assertPluginSearchIDs(t, repo, scope, "descriptiononlyneedle", "description-hit")
	assertPluginSearchIDs(t, repo, scope, "100%_done!", "description-hit")
	assertPluginSearchIDs(t, repo, scope, "missing")
}

func assertPluginSearchIDs(t *testing.T, repo *pluginrepo.Repo, scope pluginrepo.Scope, keyword string, want ...string) {
	t.Helper()
	items, total, err := repo.List(context.Background(), scope, pluginrepo.ListFilter{
		PlacementCode: "default",
		Type:          model.PluginTypeSkill,
		Keyword:       keyword,
		Limit:         20,
	})
	if err != nil {
		t.Fatalf("List keyword %q: %v", keyword, err)
	}
	if total != int64(len(want)) || len(items) != len(want) {
		t.Fatalf("List keyword %q returned total=%d items=%d, want %d", keyword, total, len(items), len(want))
	}
	for i := range want {
		if items[i].ID != want[i] {
			t.Fatalf("List keyword %q item[%d]=%q, want %q", keyword, i, items[i].ID, want[i])
		}
	}
}
