package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func uploadNameMutation(id, name, kind, space string) pluginrepo.Mutation {
	return pluginrepo.Mutation{Plugin: model.Plugin{ID: id, Name: name, Type: model.PluginType(kind), SpaceID: &space, OwnerUID: "owner", Visibility: "private", ListingState: "draft", Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), Status: 1, CreatedByType: "human"}, OperatorID: "owner"}
}

func TestUploadCreateNamesAcrossAllResourceTypes(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	for _, kind := range []string{"skill", "expert", "expert_team", "connector"} {
		t.Run(kind, func(t *testing.T) {
			sc := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
			create := func(id, name string) error {
				_, err := repo.Create(ctx, sc, uploadNameMutation(kind+id, name, kind, sc.SpaceID))
				return err
			}
			if err := create("-one", "Alpha"); err != nil {
				t.Fatal(err)
			}
			for i, name := range []string{"Alpha", " alpha ", "ＡＬＰＨＡ"} {
				if err := create(fmt.Sprint("-dup", i), name); !errors.Is(err, pluginrepo.ErrNameTaken) {
					t.Fatalf("%q: %v", name, err)
				}
			}
			// Legacy names cannot bypass the create gate merely because keys are NULL.
			if _, err := database.Exec(`UPDATE plugins SET name_key=NULL,published_name_key=NULL WHERE plugin_id=?`, kind+"-one"); err != nil {
				t.Fatal(err)
			}
			if err := create("-legacy-dupe", "alpha"); !errors.Is(err, pluginrepo.ErrNameTaken) {
				t.Fatalf("legacy collision: %v", err)
			}
			// Other Spaces stay independent; no discovery of their private resources.
			other := pluginrepo.Scope{CallerUID: "other", SpaceID: "space-b"}
			if _, err := repo.Create(ctx, other, uploadNameMutation(kind+"-other", "Alpha", kind, other.SpaceID)); err != nil {
				t.Fatalf("other Space: %v", err)
			}
			// A deleted resource releases the display name, unlike a delisted resource.
			if _, err := database.Exec(`UPDATE plugins SET deleted_at=NOW(3) WHERE plugin_id=?`, kind+"-one"); err != nil {
				t.Fatal(err)
			}
			if err := create("-replacement", "alpha"); err != nil {
				t.Fatalf("deleted name reusable: %v", err)
			}
		})
	}
}

func TestUploadCreateRejectsGlobalMarketNames(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	for _, kind := range []string{"skill", "expert", "expert_team", "connector"} {
		global := uploadNameMutation(kind+"-global", "Official", kind, "")
		global.Plugin.SpaceID = nil
		global.Plugin.Visibility = "system"
		global.Plugin.ListingState = "published"
		if _, err := repo.Create(ctx, pluginrepo.Scope{Admin: true}, global); err != nil {
			t.Fatal(err)
		}
		// Also checks pre-backfill global rows, without disclosing their IDs.
		if _, err := database.Exec(`UPDATE plugins SET name_key=NULL,published_name_key=NULL WHERE plugin_id=?`, global.Plugin.ID); err != nil {
			t.Fatal(err)
		}
		_, err := repo.Create(ctx, pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}, uploadNameMutation(kind+"-clash", " ＯＦＦＩＣＩＡＬ ", kind, "space-a"))
		if !errors.Is(err, pluginrepo.ErrNameTaken) {
			t.Fatalf("%s global collision: %v", kind, err)
		}
	}
}

func TestTeamDraftAndPublishedNamesRemainReserved(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	sc := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
	one := uploadNameMutation("team-one", "Alpha", "expert_team", sc.SpaceID)
	if _, err := repo.Create(ctx, sc, one); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE plugins SET plugin_name='Beta',name_key='beta',published_name_key='alpha' WHERE plugin_id='team-one'`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Alpha", "Beta"} {
		_, err := repo.Create(ctx, sc, uploadNameMutation("team-"+name, name, "expert_team", sc.SpaceID))
		if !errors.Is(err, pluginrepo.ErrNameTaken) {
			t.Fatalf("%s reservation: %v", name, err)
		}
	}
	// The database itself must protect both keys even for concurrent/direct writes.
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_name_reservations WHERE plugin_id='team-one'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("team reservations=%d err=%v", count, err)
	}
	if _, err := database.Exec(`UPDATE plugins SET published_name_key='alpha' WHERE plugin_id='team-one'`); err != nil {
		t.Fatalf("self update: %v", err)
	}
}

func TestCreateNameCheckDoesNotRevealGlobalDraftName(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	m := uploadNameMutation("official-global", "Official", "expert_team", "")
	m.Plugin.SpaceID = nil
	m.Plugin.Visibility = "system"
	m.Plugin.ListingState = "published"
	if _, err := repo.Create(ctx, pluginrepo.Scope{Admin: true}, m); err != nil {
		t.Fatal(err)
	}
	seedVersionRow(t, database, "official-v1", m.Plugin.ID, "1")
	if _, err := database.Exec(`UPDATE plugin_versions SET manifest_json=JSON_OBJECT('plugin_name','Official') WHERE version_id='official-v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO plugin_version_releases (plugin_id,version_id,version_label,released_by,released_at) VALUES ('official-global','official-v1','1.0.0','owner',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE plugins SET plugin_name='Unreleased Secret',name_key='unreleased secret',published_name_key='official',published_version_id='official-v1' WHERE plugin_id='official-global'`); err != nil {
		t.Fatal(err)
	}
	sc := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
	if _, err := repo.Create(ctx, sc, uploadNameMutation("local-draft", "Unreleased Secret", "expert_team", sc.SpaceID)); err != nil {
		t.Fatalf("global unpublished name leaked through duplicate guard: %v", err)
	}
	if _, err := repo.Create(ctx, sc, uploadNameMutation("local-official", "Official", "expert_team", sc.SpaceID)); !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("global released name not protected: %v", err)
	}
}

func TestConcurrentUploadsCannotCommitDuplicateNames(t *testing.T) {
	for _, kind := range []string{"expert_team", "connector"} {
		t.Run(kind, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			repo := pluginrepo.New(database)
			sc := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
			var wg sync.WaitGroup
			results := make(chan error, 6)
			for i := 0; i < 6; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					_, err := repo.Create(ctx, sc, uploadNameMutation(fmt.Sprintf("race-team-%d", i), "Concurrent Team", kind, sc.SpaceID))
					results <- err
				}(i)
			}
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
					continue
				}
				if !errors.Is(err, pluginrepo.ErrNameTaken) && !errors.Is(err, pluginrepo.ErrDeadlock) {
					t.Fatalf("unexpected concurrent error: %v", err)
				}
			}
			if success != 1 {
				t.Fatalf("committed %d duplicates, want exactly one", success)
			}
			if _, err := repo.Create(ctx, sc, uploadNameMutation("race-retry", " concurrent team ", kind, sc.SpaceID)); !errors.Is(err, pluginrepo.ErrNameTaken) {
				t.Fatalf("retry must return rename error: %v", err)
			}
		})
	}
}
