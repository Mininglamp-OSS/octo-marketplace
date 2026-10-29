package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// Include table counts as well as row values: a rejected rebuild must roll back
// newly inserted children, placements, snapshots and audit events too.
func nameWriteEvidence(t *testing.T, database *sql.DB) string {
	t.Helper()
	var evidence string
	err := database.QueryRow(`SELECT CONCAT_WS('|',
 (SELECT GROUP_CONCAT(CONCAT_WS(':',plugin_id,plugin_name,COALESCE(name_key,'NULL'),COALESCE(published_name_key,'NULL'),COALESCE(current_version_id,'NULL')) ORDER BY plugin_id) FROM plugins),
 (SELECT COUNT(*) FROM plugins), (SELECT COUNT(*) FROM plugin_relations),
 (SELECT COUNT(*) FROM plugin_versions), (SELECT COUNT(*) FROM plugin_placements),
 (SELECT COUNT(*) FROM plugin_audit_logs), (SELECT COUNT(*) FROM plugin_name_reservations))`).Scan(&evidence)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func legacyNameFixture(t *testing.T, database *sql.DB, repo *pluginrepo.Repo, kind, publishedName string) {
	t.Helper()
	ctx := context.Background()
	sc := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
	if _, err := repo.Create(ctx, sc, uploadNameMutation("legacy", "Alpha", kind, sc.SpaceID)); err != nil {
		t.Fatal(err)
	}
	if publishedName != "" {
		seedVersionRow(t, database, "legacy-release", "legacy", "1")
		if _, err := database.Exec(`UPDATE plugin_versions SET manifest_json=JSON_OBJECT('plugin_name',?) WHERE version_id='legacy-release'`, publishedName); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO plugin_version_releases (plugin_id,version_id,version_label,released_by,released_at) VALUES ('legacy','legacy-release','1.0.0','owner',NOW(3))`); err != nil {
			t.Fatal(err)
		}
		// The old release keeps Alpha while the editable draft has a new name.
		if _, err := database.Exec(`UPDATE plugins SET plugin_name='Legacy Draft',published_version_id='legacy-release',listing_state='published' WHERE plugin_id='legacy'`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`UPDATE plugins SET name_key=NULL,published_name_key=NULL WHERE plugin_id='legacy'`); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyNameCollisionOnUpdateAndRebuild(t *testing.T) {
	for _, kind := range []string{"skill", "expert", "expert_team"} {
		for _, path := range []string{"update", "rebuild"} {
			if kind == "skill" && path == "rebuild" {
				continue // Skill reuploads use Update; graph rebuilds are containers.
			}
			for _, published := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/published=%t", kind, path, published), func(t *testing.T) {
					database := reviewDB(t)
					repo := pluginrepo.New(database)
					ctx := context.Background()
					sc := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
					pub := ""
					if published {
						pub = "Alpha"
					}
					legacyNameFixture(t, database, repo, kind, pub)
					if _, err := repo.Create(ctx, sc, uploadNameMutation("candidate", "Beta", kind, sc.SpaceID)); err != nil {
						t.Fatal(err)
					}
					p, err := repo.Get(ctx, sc, "candidate")
					if err != nil {
						t.Fatal(err)
					}
					write := func(name string) error {
						m := pluginrepo.Mutation{Plugin: *p, OperatorID: "owner", SnapshotVersion: true}
						m.Plugin.Name = name
						if path == "update" {
							_, err := repo.Update(ctx, sc, m)
							return err
						}
						childKind, relType := "skill", "expert_skill"
						if kind == "expert_team" {
							childKind, relType = "expert", "expert_team_expert"
						}
						child := uploadNameMutation("new-child", "Embedded", childKind, sc.SpaceID)
						child.Plugin.IsEmbedded = true
						m.Relations = []model.PluginRelation{{TargetPluginID: "new-child", Type: relType, Status: 1}}
						_, err := repo.RebuildGraph(ctx, sc, m, []pluginrepo.Mutation{child})
						return err
					}
					before := nameWriteEvidence(t, database)
					for _, name := range []string{"Alpha", " alpha ", " ＡＬＰＨＡ "} {
						if err := write(name); !errors.Is(err, pluginrepo.ErrNameTaken) {
							t.Errorf("rename to legacy %q: %v, want ErrNameTaken", name, err)
						}
						if got := nameWriteEvidence(t, database); got != before {
							t.Fatal("rejected rename changed persisted state")
						}
					}
					// Excluding the edited row is essential: self-name saves remain legal.
					if err := write("Beta"); err != nil {
						t.Fatalf("self-name save: %v", err)
					}
				})
			}
		}
	}
}

func TestLegacyNameUpdateServiceReturnsNameTaken(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	legacyNameFixture(t, database, repo, "skill", "Alpha")
	svc := pluginsvc.New(repo).WithVersionManagement(true)
	owner, ctx := managedOwner(), context.Background()
	created, err := svc.Create(ctx, owner, managedSkillWrite("1.0.0", "Beta", model.PluginVisibilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	before := nameWriteEvidence(t, database)
	_, err = svc.Update(ctx, owner, created.Plugin.ID, managedSkillWrite("1.0.0", "Alpha", model.PluginVisibilityPrivate))
	if !errors.Is(err, pluginsvc.ErrNameTaken) {
		t.Fatalf("service rename: %v, want ErrNameTaken", err)
	}
	if got := nameWriteEvidence(t, database); got != before {
		t.Fatal("service rename changed state on conflict")
	}
}

func TestNameUpdateDoesNotRevealForeignPrivateOrGlobalDraftName(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	for _, kind := range []string{"skill", "expert", "expert_team"} {
		t.Run(kind, func(t *testing.T) {
			sc := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
			foreign := pluginrepo.Scope{CallerUID: "other", SpaceID: "space-b"}
			if _, err := repo.Create(ctx, foreign, uploadNameMutation(kind+"-foreign", "Secret", kind, foreign.SpaceID)); err != nil {
				t.Fatal(err)
			}
			global := uploadNameMutation(kind+"-global", "Official", kind, "")
			global.Plugin.SpaceID, global.Plugin.Visibility, global.Plugin.ListingState = nil, "system", "published"
			if _, err := repo.Create(ctx, pluginrepo.Scope{Admin: true}, global); err != nil {
				t.Fatal(err)
			}
			seedVersionRow(t, database, kind+"-release", global.Plugin.ID, "1")
			if _, err := database.Exec(`UPDATE plugin_versions SET manifest_json=JSON_OBJECT('plugin_name','Official') WHERE version_id=?`, kind+"-release"); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO plugin_version_releases (plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,'1.0.0','owner',NOW(3))`, global.Plugin.ID, kind+"-release"); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE plugins SET published_version_id=?,plugin_name='Unreleased Secret',name_key=NULL,published_name_key=NULL WHERE plugin_id=?`, kind+"-release", global.Plugin.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.Create(ctx, sc, uploadNameMutation(kind+"-local", "Beta", kind, sc.SpaceID)); err != nil {
				t.Fatal(err)
			}
			p, err := repo.Get(ctx, sc, kind+"-local")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"Secret", "Unreleased Secret"} {
				p.Name = name
				if _, err := repo.Update(ctx, sc, pluginrepo.Mutation{Plugin: *p, OperatorID: "owner"}); err != nil {
					t.Fatalf("private name %q leaked: %v", name, err)
				}
			}
			p.Name = " ＯＦＦＩＣＩＡＬ "
			if _, err := repo.Update(ctx, sc, pluginrepo.Mutation{Plugin: *p, OperatorID: "owner"}); !errors.Is(err, pluginrepo.ErrNameTaken) {
				t.Fatalf("global published name: %v", err)
			}
		})
	}
}

func TestConcurrentRenamesKeepUniqueNames(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	sc := pluginrepo.Scope{CallerUID: "owner", SpaceID: "space-a"}
	for _, kind := range []string{"skill", "expert", "expert_team"} {
		t.Run(kind, func(t *testing.T) {
			mutations := make([]pluginrepo.Mutation, 2)
			for i, name := range []string{"Beta", "Gamma"} {
				m := uploadNameMutation(fmt.Sprintf("%s-%d", kind, i), name, kind, sc.SpaceID)
				if _, err := repo.Create(ctx, sc, m); err != nil {
					t.Fatal(err)
				}
				m.Plugin.Name = "Alpha"
				mutations[i] = m
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, m := range mutations {
				wg.Add(1)
				go func(m pluginrepo.Mutation) {
					defer wg.Done()
					<-start
					_, err := repo.Update(ctx, sc, m)
					results <- err
				}(m)
			}
			close(start)
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				} else if !errors.Is(err, pluginrepo.ErrNameTaken) && !errors.Is(err, pluginrepo.ErrDeadlock) {
					t.Fatalf("unexpected concurrent rename error: %v", err)
				}
			}
			if success != 1 {
				t.Fatalf("committed %d renames, want one", success)
			}
			for _, m := range mutations {
				p, err := repo.Get(ctx, sc, m.Plugin.ID)
				if err != nil {
					t.Fatal(err)
				}
				if p.Name == "Alpha" {
					continue
				}
				if _, err := repo.Update(ctx, sc, m); !errors.Is(err, pluginrepo.ErrNameTaken) {
					t.Fatalf("loser retry: %v, want ErrNameTaken", err)
				}
			}
		})
	}
}
