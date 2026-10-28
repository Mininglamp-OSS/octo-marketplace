package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
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

func TestDeleteProtectsSkillInTeamsOlderPinnedMember(t *testing.T) {
	for _, references := range []bool{false, true} {
		t.Run(fmt.Sprintf("references=%t", references), func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			for _, p := range []seedPlugin{{id: "skill", typ: "skill", visibility: "private", listingState: "published"}, {id: "expert", typ: "expert", visibility: "private", listingState: "published"}, {id: "team", typ: "expert_team", visibility: "private", listingState: "published"}} {
				seed(t, database, p)
			}
			releasePinnedVersion(t, database, "skill", "s1", "1.0.0", nil)
			releasePinnedVersion(t, database, "expert", "e1", "1.0.0", []model.PluginRelation{pinnedTestEdge("expert", "skill", "expert_skill", "s1")})
			releasePinnedVersion(t, database, "expert", "e2", "2.0.0", nil)
			releasePinnedVersion(t, database, "team", "t1", "1.0.0", []model.PluginRelation{pinnedTestEdge("team", "expert", "expert_team_expert", "e1")})
			if _, err := database.Exec(`INSERT INTO plugin_version_relation_refs VALUES ('expert','e1','e-s','expert_skill','skill','s1'),('team','t1','t-e','expert_team_expert','expert','e1')`); err != nil {
				t.Fatal(err)
			}
			markReferenceIndexFixtures(t, database)
			repo := pluginrepo.New(database).WithReferences(references)
			if err := repo.Delete(ctx, tenantScope(), "skill", "user-1", "", "", nil); !errors.Is(err, pluginrepo.ErrRelationInUse) {
				t.Fatalf("pinned Team did not protect Skill: %v", err)
			}
			releasePinnedVersion(t, database, "team", "t2", "2.0.0", nil)
			if _, err := database.Exec(`INSERT INTO plugin_version_reference_index_state(source_plugin_id,source_version_id,relation_count,snapshot_digest,verified_at) SELECT plugin_id,version_id,0,SHA2(CAST(relations_json AS CHAR CHARACTER SET utf8mb4),256),NOW(3) FROM plugin_versions WHERE version_id='t2'`); err != nil {
				t.Fatal(err)
			}
			if err := repo.Delete(ctx, tenantScope(), "skill", "user-1", "", "", nil); err != nil {
				t.Fatalf("old Team history permanently protected Skill: %v", err)
			}
		})
	}
}

// pinnedTestEdge builds one snapshot edge. Connector edges carry no pin by
// contract; every Skill/Expert edge in a released snapshot must carry one.
func pinnedTestEdge(source, target, kind, pin string) model.PluginRelation {
	r := model.PluginRelation{ID: source + "-" + target, SourcePluginID: source, TargetPluginID: target, Type: kind, Status: 1, Data: json.RawMessage(`{}`)}
	if pin != "" {
		v := pin
		r.TargetVersionID = &v
	}
	if kind == "expert_team_expert" {
		r.Data = json.RawMessage(`{"member_key":"lead","is_leader":true}`)
	}
	return r
}

// releasePinnedVersion stores one immutable snapshot plus its formal release and
// points the source's published head at it.
func releasePinnedVersion(t *testing.T, database *sql.DB, id, versionID, label string, rels []model.PluginRelation) {
	t.Helper()
	seedVersionRow(t, database, versionID, id, label)
	raw, err := model.EncodeSnapshotRelations(rels)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE plugin_versions SET relations_json=?,snapshot_schema_version=1,artifacts_json=JSON_ARRAY() WHERE plugin_id=? AND version_id=?`, string(raw), id, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,?,'user-1',NOW(3))`, id, versionID, label); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, versionID, id); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteUnreferencedPublishedAndDelistedKeepsHistory(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database).WithReferences(true)
	for _, kind := range []string{"skill", "expert"} {
		for _, state := range []string{"published", "delisted"} {
			id := kind + "-" + state
			seed(t, database, seedPlugin{id: id, typ: kind, visibility: "private", listingState: state})
			releasePinnedVersion(t, database, id, id+"-v1", "1.0.0", nil)
		}
	}
	markReferenceIndexFixtures(t, database)
	for _, kind := range []string{"skill", "expert"} {
		for _, state := range []string{"published", "delisted"} {
			id := kind + "-" + state
			var err error
			if kind == "expert" {
				err = repo.DeleteGraph(context.Background(), tenantScope(), id, "user-1", "", "", nil)
			} else {
				err = repo.Delete(context.Background(), tenantScope(), id, "user-1", "", "", nil)
			}
			if err != nil {
				t.Fatalf("delete %s: %v", id, err)
			}
			var count int
			if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_versions v JOIN plugin_version_releases r ON r.plugin_id=v.plugin_id AND r.version_id=v.version_id WHERE v.plugin_id=?`, id).Scan(&count); err != nil || count != 1 {
				t.Fatalf("release history lost for %s: %d %v", id, count, err)
			}
		}
	}
}

func TestDeleteDoesNotRequireConvertingUnrelatedDelistedUnpinnedHistory(t *testing.T) {
	database := reviewDB(t)
	for _, p := range []seedPlugin{{id: "skill", typ: "skill", visibility: "private", listingState: "delisted"}, {id: "expert", typ: "expert", visibility: "private", listingState: "delisted"}, {id: "team", typ: "expert_team", visibility: "private", listingState: "delisted"}} {
		seed(t, database, p)
	}
	releasePinnedVersion(t, database, "skill", "s1", "1.0.0", nil)
	// Pre-version-management delisted rows retain their old snapshots and no
	// formal published pointer. They are not silently promoted or converted.
	seedVersionRow(t, database, "e1", "expert", "1")
	seedVersionRow(t, database, "t1", "team", "1")
	raw, err := model.EncodeSnapshotRelations([]model.PluginRelation{pinnedTestEdge("team", "expert", "expert_team_expert", "")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE plugin_versions SET relations_json=? WHERE version_id='t1'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	seedRelation(t, database, "old-live-member", "team", "expert", "expert_team_expert")
	if _, err = database.Exec(`INSERT INTO plugin_version_reference_index_state(source_plugin_id,source_version_id,relation_count,snapshot_digest,verified_at) SELECT plugin_id,version_id,0,SHA2(CAST(relations_json AS CHAR CHARACTER SET utf8mb4),256),NOW(3) FROM plugin_versions WHERE version_id='s1'`); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := database.QueryRow(`SELECT relations_json FROM plugin_versions WHERE version_id='t1'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := pluginrepo.New(database).WithReferences(true).Delete(context.Background(), tenantScope(), "skill", "user-1", "", "", nil); err != nil {
		t.Fatal("unrelated unpinned history became a global deletion ban", err)
	}
	if err := database.QueryRow(`SELECT relations_json FROM plugin_versions WHERE version_id='t1'`).Scan(&after); err != nil || before != after {
		t.Fatal("historical snapshot was converted", err)
	}
}

func TestDeleteRejectsUnpinnedLegacyTeamWithoutGuessingMemberVersion(t *testing.T) {
	for _, references := range []bool{false, true} {
		for _, emptyPin := range []bool{false, true} {
			t.Run(fmt.Sprintf("references_%t/empty_%t", references, emptyPin), func(t *testing.T) {
				database := reviewDB(t)
				for _, p := range []seedPlugin{{id: "skill", typ: "skill", visibility: "private", listingState: "published"}, {id: "expert", typ: "expert", visibility: "private", listingState: "published"}, {id: "team", typ: "expert_team", visibility: "private", listingState: "published"}} {
					seed(t, database, p)
				}
				releasePinnedVersion(t, database, "skill", "s1", "1.0.0", nil)
				releasePinnedVersion(t, database, "expert", "e1", "1.0.0", []model.PluginRelation{pinnedTestEdge("expert", "skill", "expert_skill", "s1")})
				releasePinnedVersion(t, database, "expert", "e2", "2.0.0", nil)
				edge := pinnedTestEdge("team", "expert", "expert_team_expert", "")
				if emptyPin {
					blank := ""
					edge.TargetVersionID = &blank
				}
				releasePinnedVersion(t, database, "team", "t1", "1.0.0", []model.PluginRelation{edge})
				if _, err := database.Exec(`INSERT INTO plugin_version_relation_refs VALUES ('expert','e1','e-s','expert_skill','skill','s1')`); err != nil {
					t.Fatal(err)
				}
				markReferenceIndexFixtures(t, database)
				// Such a legacy snapshot cannot be indexed with a NULL member pin. Do not
				// fabricate verified coverage for its unrepresentable dependency graph.
				if _, err := database.Exec(`DELETE FROM plugin_version_reference_index_state WHERE source_plugin_id='team'`); err != nil {
					t.Fatal(err)
				}
				repo := pluginrepo.New(database).WithReferences(references)
				if err := repo.Delete(context.Background(), tenantScope(), "skill", "user-1", "", "", nil); !errors.Is(err, pluginrepo.ErrReferenceIndexIncomplete) {
					t.Fatalf("unpinned legacy graph allowed delete: %v", err)
				}
				var active int
				if err := database.QueryRow(`SELECT COUNT(*) FROM plugins WHERE plugin_id='skill' AND deleted_at IS NULL`).Scan(&active); err != nil || active != 1 {
					t.Fatalf("target mutated: count=%d err=%v", active, err)
				}
				// Removing the Team edge through a new release resolves uncertainty; unused
				// history must not become a permanent deletion ban.
				releasePinnedVersion(t, database, "team", "t2", "2.0.0", nil)
				if _, err := database.Exec(`INSERT INTO plugin_version_reference_index_state(source_plugin_id,source_version_id,relation_count,snapshot_digest,verified_at) SELECT plugin_id,version_id,0,SHA2(CAST(relations_json AS CHAR CHARACTER SET utf8mb4),256),NOW(3) FROM plugin_versions WHERE version_id='t2'`); err != nil {
					t.Fatal(err)
				}
				if err := repo.Delete(context.Background(), tenantScope(), "skill", "user-1", "", "", nil); err != nil {
					t.Fatalf("resolved current graph still blocked: %v", err)
				}
			})
		}
	}
}
