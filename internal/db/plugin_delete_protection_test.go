package db

import (
	"context"
	"errors"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	"testing"
)

func TestDeletePreviouslyPublishedOnlyProtectsCurrentReferences(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database).WithReferences(true)
	for _, id := range []string{"skill", "expert"} {
		seed(t, database, seedPlugin{id: id, typ: id, visibility: "private", listingState: "published"})
		seedVersionRow(t, database, id+"-v1", id, "1")
		if _, err := database.Exec(`INSERT INTO plugin_version_releases VALUES (?,?,'1.0.0','user-1',NOW(3))`, id, id+"-v1"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, id+"-v1", id); err != nil {
			t.Fatal(err)
		}
	}
	seedVersionRow(t, database, "expert-v2", "expert", "2")
	if _, err := database.Exec(`INSERT INTO plugin_version_releases VALUES ('expert','expert-v2','2.0.0','user-1',NOW(3))`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO plugin_version_relation_refs VALUES ('expert','expert-v1','ref','expert_skill','skill','skill-v1')`); err != nil {
		t.Fatal(err)
	}
	markReferenceIndexFixtures(t, database)
	if err := repo.Delete(ctx, tenantScope(), "skill", "user-1", "", "", nil); !errors.Is(err, pluginrepo.ErrRelationInUse) {
		t.Fatalf("current published reference bypass: %v", err)
	}
	// A new published source version no longer refers to the target. The old
	// immutable release/index must remain available without blocking deletion.
	if _, err := database.Exec(`UPDATE plugins SET published_version_id='expert-v2' WHERE plugin_id='expert'`); err != nil {
		t.Fatal(err)
	}
	seedRelation(t, database, "draft-ref", "expert", "skill", "expert_skill")
	if err := repo.Delete(ctx, tenantScope(), "skill", "user-1", "", "", nil); !errors.Is(err, pluginrepo.ErrRelationInUse) {
		t.Fatalf("live editing reference bypass: %v", err)
	}
	if _, err := database.Exec(`UPDATE plugin_relations SET deleted_at=NOW(3) WHERE relation_id='draft-ref'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, pluginrepo.Scope{CallerUID: "user-1", SpaceID: "other"}, "skill", "user-1", "", "", nil); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-space delete: %v", err)
	}
	if err := repo.Delete(ctx, tenantScope(), "skill", "user-1", "", "", nil); err != nil {
		t.Fatalf("unreferenced published resource cannot be deleted: %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id='skill'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("immutable history removed: %d %v", count, err)
	}
}
