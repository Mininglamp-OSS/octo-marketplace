package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func nameFixture(t *testing.T, db *sql.DB, repo *pluginrepo.Repo, id, name, typ, space, listing string) {
	t.Helper()
	p := model.Plugin{
		ID: id, Name: name, Type: model.PluginType(typ),
		Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingState(listing),
		Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`),
		ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1,
		CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner",
	}
	_, err := repo.Create(context.Background(), pluginrepo.Scope{CallerUID: "owner", SpaceID: space}, pluginrepo.Mutation{
		Plugin: p, OperatorID: "owner", OperatorName: "Owner",
	})
	if err != nil {
		t.Fatalf("create %s/%s: %v", id, name, err)
	}
}

func TestPluginNameUniquenessPerSpaceType(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	nameFixture(t, db, repo, "expert-a", "Alpha", "expert", "space-a", "draft")
	// Same display name is allowed across types and Spaces.
	nameFixture(t, db, repo, "skill-a", "Alpha", "skill", "space-a", "draft")
	nameFixture(t, db, repo, "expert-b", "Alpha", "expert", "space-b", "draft")
	// Same type + Space + normalized name is rejected.
	err := func() error {
		_, e := repo.Create(context.Background(), pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}, pluginrepo.Mutation{
			Plugin: model.Plugin{ID: "expert-dup", Name: " alpha ", Type: model.PluginTypeExpert, Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1, CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner"}, OperatorID: "owner", OperatorName: "Owner",
		})
		return e
	}()
	if !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("duplicate name error = %v, want ErrNameTaken", err)
	}
}

func TestPluginNameUniquenessFullWidthAndCase(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	nameFixture(t, db, repo, "skill-beta", "Beta", "skill", "space-a", "draft")
	err := func() error {
		_, e := repo.Create(context.Background(), pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}, pluginrepo.Mutation{
			Plugin: model.Plugin{ID: "skill-beta-fullwidth", Name: "ＢＥＴＡ", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1, CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner"}, OperatorID: "owner", OperatorName: "Owner",
		})
		return e
	}()
	if !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("full-width duplicate error = %v, want ErrNameTaken", err)
	}
}

func TestPluginNameUniquenessSoftDeleteAndDelist(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	nameFixture(t, db, repo, "skill-gamma", "Gamma", "skill", "space-a", "draft")
	// Delisted is not deleted and must keep occupying the name.
	if _, err := db.Exec(`UPDATE plugins SET listing_state='delisted' WHERE plugin_id='skill-gamma'`); err != nil {
		t.Fatal(err)
	}
	err := func() error {
		_, e := repo.Create(context.Background(), pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}, pluginrepo.Mutation{
			Plugin: model.Plugin{ID: "skill-gamma-dupe", Name: "gamma", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1, CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner"}, OperatorID: "owner", OperatorName: "Owner",
		})
		return e
	}()
	if !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("delisted duplicate error = %v, want ErrNameTaken", err)
	}
	if _, err := db.Exec(`UPDATE plugins SET deleted_at=NOW(3) WHERE plugin_id='skill-gamma'`); err != nil {
		t.Fatal(err)
	}
	nameFixture(t, db, repo, "skill-gamma-new", "Gamma", "skill", "space-a", "draft")
}

func TestPluginNameRenameUpdatesKey(t *testing.T) {
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	nameFixture(t, db, repo, "skill-echo", "Echo", "skill", "space-a", "draft")
	ctx := context.Background()
	scope := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
	got, err := repo.Get(ctx, scope, "skill-echo")
	if err != nil {
		t.Fatal(err)
	}
	got.Name = "Zeta"
	if _, err := repo.Update(ctx, scope, pluginrepo.Mutation{Plugin: *got, OperatorID: "owner"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	// The new name is now occupied.
	if _, err := repo.Create(ctx, scope, pluginrepo.Mutation{
		Plugin: model.Plugin{ID: "skill-zeta-dupe", Name: " zeta ", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySpace, ListingState: model.PluginListingStateDraft, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{}`), Package: json.RawMessage(`{}`), ManifestHash: strings.Repeat("a", 71), PluginHash: strings.Repeat("b", 71), Status: 1, CreatedByType: string(model.CreatedByHuman), CreatorName: "Owner"}, OperatorID: "owner", OperatorName: "Owner",
	}); !errors.Is(err, pluginrepo.ErrNameTaken) {
		t.Fatalf("renamed duplicate error = %v, want ErrNameTaken", err)
	}
	// The old name is free after the rename.
	nameFixture(t, db, repo, "skill-echo-2", "Echo", "skill", "space-a", "draft")
}
