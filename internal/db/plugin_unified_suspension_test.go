package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
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

// containerUploadArchive builds the real reupload payload for an expert with one
// bundled skill, so the regression below exercises the production upload path
// instead of a hand-built row set.
func containerUploadArchive(t *testing.T) []byte {
	t.Helper()
	skillZip := promotionArchive(t, map[string][]byte{"SKILL.md": []byte("---\nname: bundled\ndescription: bundled fixture\n---\nbundled body")})
	manifest, err := json.Marshal(map[string]any{
		"name": "Bundled expert", "summary": "summary", "instruction": "instructions",
		"skills": []map[string]string{{"name": "Bundled", "file": "skills/bundled.zip"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return promotionArchive(t, map[string][]byte{"expert.json": manifest, "skills/bundled.zip": skillZip})
}

// A bundled Skill uploaded with its container follows that container: it is
// listed and demoted with it and never receives an independent formal release.
// The suspension cascade demotes it together with the container, so an explicit
// republish after the dependency recovers must restore the child without
// demanding a release the upload path never creates. Before the availability
// predicate honored that topology, the recovery DELETE affected zero rows,
// returned target_not_published, and every retry rolled back — the container
// could never be relisted and its child could not be published on its own.
func TestSuspendedUploadedContainerRepublishRestoresEmbeddedChildren(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{true: "manual", false: "automatic"}[manual], func(t *testing.T) {
			ctx, owner := context.Background(), managedOwner()
			db := reviewDB(t)
			repo := pluginrepo.New(db)
			svc := pluginsvc.New(repo, storage.NewLocal(t.TempDir(), "http://localhost")).WithVersionManagement(true)
			if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), !manual, owner.UID, owner.Name); err != nil {
				t.Fatal(err)
			}
			const connector = "suspension-connector"
			seed(t, db, seedPlugin{id: connector, typ: "connector", visibility: "space", listingState: "published"})
			expert, err := svc.Create(ctx, owner, managedWrite("1.0.0", "container instructions", model.PluginVisibilitySpace))
			if err != nil {
				t.Fatal(err)
			}
			id := expert.Plugin.ID
			admin := owner
			admin.IsSystemAdmin = true
			expert, err = svc.ReuploadContainer(ctx, admin, id, pluginsvc.ContainerImportParams{Archive: containerUploadArchive(t)})
			if err != nil {
				t.Fatalf("reupload: %v", err)
			}
			var child string
			for _, rel := range expert.Relations {
				if rel.Type == "expert_skill" {
					child = rel.TargetPluginID
				}
			}
			if child == "" {
				t.Fatal("uploaded container has no bundled skill")
			}
			// Pin the production topology: the upload path snapshots the top only,
			// so the bundled child has no release and no published pointer.
			var releases int
			var published bool
			if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=?), published_version_id IS NOT NULL FROM plugins WHERE plugin_id=?`, child, child).Scan(&releases, &published); err != nil {
				t.Fatal(err)
			}
			if releases != 0 || published {
				t.Fatalf("bundled child unexpectedly has a release (%d) or published pointer (%t)", releases, published)
			}
			// An unpinned Connector reference saved while the container is editable.
			req := managedWrite("1.0.1", "container instructions v2", model.PluginVisibilitySpace)
			req.Relations = []pluginsvc.RelationRequest{
				{Type: "expert_connector", TargetPluginID: connector},
				{Type: "expert_skill", TargetPluginID: child},
			}
			if _, err = svc.Update(ctx, owner, id, req); err != nil {
				t.Fatalf("update: %v", err)
			}
			publish := func(version string) (string, error) {
				p, e := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id, Version: version})
				if e != nil {
					return "", e
				}
				if p.Review == nil {
					return "", nil
				}
				_, e = svc.ApproveReview(ctx, managedReviewer(), p.Review.ID)
				return p.Review.ID, e
			}
			state := func() (root, bundled, released string, marker bool) {
				t.Helper()
				if err := db.QueryRow(`SELECT p.listing_state,c.listing_state,COALESCE(p.published_version_id,''),
 EXISTS(SELECT 1 FROM plugin_dependency_suspensions s WHERE s.plugin_id=p.plugin_id)
 FROM plugins p JOIN plugins c ON c.plugin_id=? WHERE p.plugin_id=?`, child, id).Scan(&root, &bundled, &released, &marker); err != nil {
					t.Fatal(err)
				}
				return root, bundled, released, marker
			}
			// available asserts the container is a supported catalog entry: not
			// blocked by its bundled child, readable by another Space member and free
			// of owner dependency diagnostics. This is the contract the release
			// requirement used to break even before any suspension.
			available := func(step string) {
				t.Helper()
				blocked, err := repo.HasUnavailableDependencies(ctx, tenantScope(), id)
				if err != nil {
					t.Fatal(err)
				}
				issues, err := repo.DependencyIssues(ctx, tenantScope(), []model.Plugin{{ID: id, Type: model.PluginTypeExpert, ListingState: model.PluginListingStatePublished}})
				if err != nil {
					t.Fatal(err)
				}
				items, _, err := svc.List(ctx, owner, pluginsvc.ListParams{Type: model.PluginTypeExpert})
				if err != nil {
					t.Fatal(err)
				}
				listed := false
				for _, item := range items {
					if item.ID == id {
						listed = true
					}
				}
				teammate := pluginsvc.Caller{UID: "teammate", SpaceID: owner.SpaceID}
				if _, err := svc.Detail(ctx, teammate, id, false); err != nil {
					t.Fatalf("%s: teammate cannot read the published container: %v", step, err)
				}
				if blocked || len(issues[id]) != 0 || !listed {
					t.Fatalf("%s: blocked=%t issues=%+v listed=%t", step, blocked, issues[id], listed)
				}
			}

			if _, err = publish("1.0.1"); err != nil {
				t.Fatalf("first publish: %v", err)
			}
			root, bundled, released, marker := state()
			if root != "published" || bundled != "published" || marker || released == "" {
				t.Fatalf("baseline: root=%s bundled=%s released=%q marker=%t", root, bundled, released, marker)
			}
			available("after publish")
			if _, err = svc.Delist(ctx, managedReviewer(), pluginsvc.DelistParams{PluginID: connector}); err != nil {
				t.Fatal(err)
			}
			root, bundled, suspendedRelease, marker := state()
			if root != "delisted" || bundled != "delisted" || !marker {
				t.Fatalf("cascade: root=%s bundled=%s marker=%t", root, bundled, marker)
			}
			// While the dependency is still unavailable the republish must fail and
			// leave the demoted child, the marker and the release pointer untouched;
			// the restored child may not leak out of the aborted publication.
			review, err := publish("1.0.2")
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
			// Recovery restores the container and its bundled child atomically, and
			// leaves the child without an independent release it never had.
			if _, err = db.Exec(`UPDATE plugins SET listing_state='published' WHERE plugin_id=?`, connector); err != nil {
				t.Fatal(err)
			}
			if _, err = publish("1.0.2"); err != nil {
				t.Fatalf("republish after recovery: %v", err)
			}
			root, bundled, recovered, marker := state()
			if root != "published" || bundled != "published" || marker {
				t.Fatalf("recovery: root=%s bundled=%s marker=%t", root, bundled, marker)
			}
			if recovered == suspendedRelease {
				t.Fatalf("published pointer did not advance: %q", recovered)
			}
			if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=?), published_version_id IS NOT NULL FROM plugins WHERE plugin_id=?`, child, child).Scan(&releases, &published); err != nil {
				t.Fatal(err)
			}
			if releases != 0 || published {
				t.Fatalf("recovery fabricated a child release (%d) or pointer (%t)", releases, published)
			}
			available("after recovery")
		})
	}
}

// A private container must recover its bundled rows at the audience its owner
// chose. The suspension cascade demotes the container and its embedded children
// together; the recovery promotion used to hardcode Space visibility (it was
// written for Space review approval), so republishing a private container made
// its bundled Skill org-readable while the Expert stayed private.
func TestSuspendedPrivateContainerRepublishKeepsEmbeddedAudience(t *testing.T) {
	ctx, owner := context.Background(), managedOwner()
	db := reviewDB(t)
	repo := pluginrepo.New(db)
	svc := pluginsvc.New(repo, storage.NewLocal(t.TempDir(), "http://localhost")).WithVersionManagement(true)
	if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), true, "reviewer", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	const connector = "private-suspension-connector"
	seed(t, db, seedPlugin{id: connector, typ: "connector", visibility: "space", listingState: "published"})
	expert, err := svc.Create(ctx, owner, managedWrite("1.0.0", "container instructions", model.PluginVisibilitySpace))
	if err != nil {
		t.Fatal(err)
	}
	id := expert.Plugin.ID
	admin := owner
	admin.IsSystemAdmin = true
	expert, err = svc.ReuploadContainer(ctx, admin, id, pluginsvc.ContainerImportParams{Archive: containerUploadArchive(t)})
	if err != nil {
		t.Fatalf("reupload: %v", err)
	}
	var child string
	for _, rel := range expert.Relations {
		if rel.Type == "expert_skill" {
			child = rel.TargetPluginID
		}
	}
	if child == "" {
		t.Fatal("uploaded container has no bundled skill")
	}
	req := managedWrite("1.0.1", "container instructions v2", model.PluginVisibilitySpace)
	req.Relations = []pluginsvc.RelationRequest{
		{Type: "expert_connector", TargetPluginID: connector},
		{Type: "expert_skill", TargetPluginID: child},
	}
	if _, err = svc.Update(ctx, owner, id, req); err != nil {
		t.Fatalf("update: %v", err)
	}
	publishSpace := func(version string) error {
		p, e := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id, Version: version})
		if e != nil {
			return e
		}
		if p.Review != nil {
			_, e = svc.ApproveReview(ctx, managedReviewer(), p.Review.ID)
		}
		return e
	}
	if err = publishSpace("1.0.1"); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	// Space publication first, then narrow the whole graph to private.
	plan, err := svc.VisibilityPlan(ctx, owner, id, model.PluginVisibilityPrivate)
	if err != nil {
		t.Fatalf("visibility plan: %v", err)
	}
	if _, err = svc.ChangeVisibility(ctx, owner, pluginsvc.VisibilityParams{PluginID: id, Visibility: model.PluginVisibilityPrivate, ExpectedPlanRevision: plan.Revision}); err != nil {
		t.Fatalf("narrow to private: %v", err)
	}
	audience := func(step string) (rootVis, childVis, rootListing, childListing string, marker bool) {
		t.Helper()
		if err := db.QueryRow(`SELECT p.visibility,c.visibility,p.listing_state,c.listing_state,
 EXISTS(SELECT 1 FROM plugin_dependency_suspensions s WHERE s.plugin_id=p.plugin_id)
 FROM plugins p JOIN plugins c ON c.plugin_id=? WHERE p.plugin_id=?`, child, id).Scan(&rootVis, &childVis, &rootListing, &childListing, &marker); err != nil {
			t.Fatal(err)
		}
		return rootVis, childVis, rootListing, childListing, marker
	}
	rootVis, childVis, rootListing, childListing, marker := audience("narrowed")
	if rootVis != "private" || childVis != "private" || rootListing != "published" || childListing != "published" || marker {
		t.Fatalf("narrowed: root=%s/%s child=%s/%s marker=%t", rootVis, rootListing, childVis, childListing, marker)
	}
	teammate := pluginsvc.Caller{UID: "teammate", SpaceID: owner.SpaceID}
	if _, err = svc.Detail(ctx, teammate, child, false); !errors.Is(err, pluginsvc.ErrNotFound) {
		t.Fatalf("private bundled child readable after narrowing: %v", err)
	}
	if _, err = svc.Delist(ctx, managedReviewer(), pluginsvc.DelistParams{PluginID: connector}); err != nil {
		t.Fatal(err)
	}
	rootVis, childVis, rootListing, childListing, marker = audience("suspended")
	if rootVis != "private" || childVis != "private" || rootListing != "delisted" || childListing != "delisted" || !marker {
		t.Fatalf("suspended: root=%s/%s child=%s/%s marker=%t", rootVis, rootListing, childVis, childListing, marker)
	}
	// The dependency is still unavailable: publication must roll back and leave
	// the private audience and the demoted child untouched.
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id, Version: "1.0.2"}); err == nil {
		t.Fatal("published while the dependency was still delisted")
	}
	rootVis, childVis, rootListing, childListing, marker = audience("rollback")
	if rootVis != "private" || childVis != "private" || rootListing != "delisted" || childListing != "delisted" || !marker {
		t.Fatalf("rollback: root=%s/%s child=%s/%s marker=%t", rootVis, rootListing, childVis, childListing, marker)
	}
	if _, err = db.Exec(`UPDATE plugins SET listing_state='published' WHERE plugin_id=?`, connector); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id, Version: "1.0.2"}); err != nil {
		t.Fatalf("republish after recovery: %v", err)
	}
	rootVis, childVis, rootListing, childListing, marker = audience("recovered")
	if rootVis != "private" || childVis != "private" {
		t.Fatalf("private audience widened on recovery: root=%s child=%s", rootVis, childVis)
	}
	if rootListing != "published" || childListing != "published" || marker {
		t.Fatalf("recovered: root=%s child=%s marker=%t", rootListing, childListing, marker)
	}
	if _, err = svc.Detail(ctx, teammate, child, false); !errors.Is(err, pluginsvc.ErrNotFound) {
		t.Fatalf("private bundled child readable after recovery: %v", err)
	}
	if _, err = svc.Detail(ctx, teammate, id, false); !errors.Is(err, pluginsvc.ErrNotFound) {
		t.Fatalf("private container readable by teammate: %v", err)
	}
	if _, err = svc.Detail(ctx, owner, child, false); err != nil {
		t.Fatalf("owner cannot read own bundled child: %v", err)
	}
}
