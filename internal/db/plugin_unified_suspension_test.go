package db

import (
	"context"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"testing"
)

func TestSuspendedExpertRequiresAvailableUnpinnedConnector(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{true: "manual", false: "automatic"}[manual], func(t *testing.T) {
			ctx, owner := context.Background(), managedOwner()
			db := reviewDB(t)
			repo := pluginrepo.New(db)
			svc := pluginsvc.New(repo).WithVersionManagement(true)
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), !manual, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			const connector = "unversioned-connector"
			seed(t, db, seedPlugin{id: connector, typ: "connector", visibility: "space", listingState: "published"})
			req := managedWrite("1.0.0", "Expert using connector", model.PluginVisibilitySpace)
			req.Relations = []pluginsvc.RelationRequest{{Type: "expert_connector", TargetPluginID: connector}}
			expert, err := svc.Create(ctx, owner, req)
			if err != nil {
				t.Fatal(err)
			}
			id := expert.Plugin.ID
			publishManaged(t, svc, owner, id)
			original, err := repo.GetVersionState(ctx, tenantScope(), id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.Delist(ctx, managedReviewer(), pluginsvc.DelistParams{PluginID: connector}); err != nil {
				t.Fatal(err)
			}
			publish := func() (string, error) {
				p, e := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id, Version: "1.0.1"})
				if e != nil {
					return "", e
				}
				if p.Review == nil {
					return "", nil
				}
				_, e = svc.ApproveReview(ctx, managedReviewer(), p.Review.ID)
				return p.Review.ID, e
			}
			review, err := publish()
			if err == nil {
				t.Fatal("published while the unpinned connector was delisted")
			}
			if review != "" {
				if err := svc.CancelReview(ctx, owner, review); err != nil {
					t.Fatal(err)
				}
			}
			check := func(want string) {
				t.Helper()
				d, e := svc.Detail(ctx, owner, id, true)
				if e != nil {
					t.Fatal(e)
				}
				if d.Plugin.ListingState != model.PluginListingStateDelisted || (len(d.DependencyIssues) > 0) != (want == "unavailable") {
					t.Fatalf("listing=%s dependency=%+v", d.Plugin.ListingState, d.DependencyIssues)
				}
				v, e := repo.GetVersionState(ctx, tenantScope(), id)
				if e != nil || *v.PublishedVersionID != *original.PublishedVersionID {
					t.Fatal("published pointer changed")
				}
			}
			check("unavailable")
			if _, err = db.Exec("UPDATE plugins SET listing_state='published' WHERE plugin_id=?", connector); err != nil {
				t.Fatal(err)
			}
			check("restored")
			if _, err = publish(); err != nil {
				t.Fatal(err)
			}
			d, err := svc.Detail(ctx, owner, id, true)
			if err != nil || d.Plugin.ListingState != model.PluginListingStatePublished || len(d.DependencyIssues) != 0 {
				t.Fatalf("republish did not clear suspension: %v", err)
			}
		})
	}
}

func TestDelistIgnoresDraftOnlyMemberEdge(t *testing.T) {
	db := reviewDB(t)
	dependencyFixture(t, db)
	seed(t, db, seedPlugin{id: "draft-only", typ: "skill", visibility: "space", listingState: "published"})
	seedRelation(t, db, "draft-edge", "expert", "draft-only", "expert_skill")
	seedRelation(t, db, "live-team", "team", "expert", "expert_team_expert")
	if _, err := db.Exec("UPDATE plugin_relations SET target_version_id='expert-v1' WHERE relation_id='live-team'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE plugins SET published_version_id=NULL WHERE plugin_id='team'"); err != nil {
		t.Fatal(err)
	}
	r := pluginrepo.New(db).WithVersionManagement(true)
	if _, err := r.DelistPlugin(context.Background(), reviewerScope(), pluginrepo.DelistParams{PluginID: "draft-only", OperatorID: "admin-1"}); err != nil {
		t.Fatal(err)
	}
	var listing string
	var suspended bool
	if err := db.QueryRow("SELECT listing_state, EXISTS(SELECT 1 FROM plugin_dependency_suspensions WHERE plugin_id='team') FROM plugins WHERE plugin_id='team'").Scan(&listing, &suspended); err != nil || listing != "published" || suspended {
		t.Fatalf("legacy team: %s %t %v", listing, suspended, err)
	}
}

// A container suspended by a delisted dependency takes its embedded children
// down with it (demoteEmbeddedChildren). An explicit republish after the
// dependency recovers must promote those children again in the same
// transaction: the recovery check reads the candidate release, where they are
// still delisted, so without the promotion every retry fails with
// target_not_published and rolls back — the container can never be relisted,
// and the embedded child cannot be published on its own.
func TestSuspendedContainerRepublishRestoresEmbeddedChildren(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{true: "manual", false: "automatic"}[manual], func(t *testing.T) {
			ctx, owner := context.Background(), managedOwner()
			db := reviewDB(t)
			repo := pluginrepo.New(db)
			svc := pluginsvc.New(repo).WithVersionManagement(true)
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), !manual, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			const connector, child = "suspension-connector", "bundled-child"
			seed(t, db, seedPlugin{id: connector, typ: "connector", visibility: "space", listingState: "published"})
			// The bundled skill is already a formal release. The suspension cascade
			// only flips its listing state, so the candidate snapshot keeps naming a
			// released version and the availability check depends on the promotion.
			seed(t, db, seedPlugin{id: child, typ: "skill", visibility: "space", listingState: "published", embedded: true, owner: owner.UID, currentVersion: "1.0.0"})
			if _, err := db.Exec(`INSERT INTO plugin_versions(version_id,plugin_id,version,declared_version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,created_by,created_at,snapshot_schema_version,artifacts_json) VALUES ('child-v1',?,'1','1.0.0',JSON_OBJECT(),JSON_OBJECT(),REPEAT('a',71),REPEAT('b',71),JSON_ARRAY(),?,NOW(3),1,JSON_ARRAY())`, child, owner.UID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO plugin_version_releases (plugin_id,version_id,version_label,released_by,released_at) VALUES (?,'child-v1','1.0.0',?,NOW(3))`, child, owner.UID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE plugins SET published_version_id='child-v1' WHERE plugin_id=?`, child); err != nil {
				t.Fatal(err)
			}
			expert, err := svc.Create(ctx, owner, managedWrite("1.0.0", "container with a bundled skill", model.PluginVisibilitySpace))
			if err != nil {
				t.Fatal(err)
			}
			id := expert.Plugin.ID
			seedRelation(t, db, "rel-connector", id, connector, "expert_connector")
			seedRelation(t, db, "rel-child", id, child, "expert_skill")
			publishManaged(t, svc, owner, id)

			state := func() (root, bundled, released string, marker bool) {
				t.Helper()
				if err := db.QueryRow(`SELECT p.listing_state,c.listing_state,COALESCE(p.published_version_id,''),
 EXISTS(SELECT 1 FROM plugin_dependency_suspensions s WHERE s.plugin_id=p.plugin_id)
 FROM plugins p JOIN plugins c ON c.plugin_id=? WHERE p.plugin_id=?`, child, id).Scan(&root, &bundled, &released, &marker); err != nil {
					t.Fatal(err)
				}
				return root, bundled, released, marker
			}
			publish := func() (string, error) {
				p, e := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id, Version: "1.0.1"})
				if e != nil {
					return "", e
				}
				if p.Review == nil {
					return "", nil
				}
				_, e = svc.ApproveReview(ctx, managedReviewer(), p.Review.ID)
				return p.Review.ID, e
			}

			root, bundled, released, marker := state()
			if root != "published" || bundled != "published" || marker || released == "" {
				t.Fatalf("baseline: root=%s bundled=%s released=%q marker=%t", root, bundled, released, marker)
			}
			if _, err = svc.Delist(ctx, managedReviewer(), pluginsvc.DelistParams{PluginID: connector}); err != nil {
				t.Fatal(err)
			}
			root, bundled, suspendedRelease, marker := state()
			if root != "delisted" || bundled != "delisted" || !marker {
				t.Fatalf("cascade: root=%s bundled=%s marker=%t", root, bundled, marker)
			}
			// While the dependency is still unavailable the republish must fail and
			// leave the demoted child, the marker and the release pointer untouched:
			// promotion may not leak out of the aborted publication.
			review, err := publish()
			if err == nil {
				t.Fatal("published while the dependency was still delisted")
			}
			if review != "" {
				if err = svc.CancelReview(ctx, owner, review); err != nil {
					t.Fatal(err)
				}
			}
			root, bundled, released, marker = state()
			if root != "delisted" || bundled != "delisted" || !marker || released != suspendedRelease {
				t.Fatalf("rollback: root=%s bundled=%s released=%q want %q marker=%t", root, bundled, released, suspendedRelease, marker)
			}
			// Recovery restores the container and its bundled child atomically.
			if _, err = db.Exec(`UPDATE plugins SET listing_state='published' WHERE plugin_id=?`, connector); err != nil {
				t.Fatal(err)
			}
			if _, err = publish(); err != nil {
				t.Fatalf("republish after recovery: %v", err)
			}
			root, bundled, recovered, marker := state()
			if root != "published" || bundled != "published" || marker {
				t.Fatalf("recovery: root=%s bundled=%s marker=%t", root, bundled, marker)
			}
			if recovered == released {
				t.Fatalf("published pointer did not advance: %q", recovered)
			}
		})
	}
}
