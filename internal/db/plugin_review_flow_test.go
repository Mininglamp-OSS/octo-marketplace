package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	migrate "github.com/rubenv/sql-migrate"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	migrationsql "github.com/Mininglamp-OSS/octo-marketplace/migrations/sql"
)

// These tests drive the REAL repository against a real MySQL. The review
// workflow's load-bearing behaviour is transactional (the single-pending lock,
// the status CAS, the frozen-snapshot application) and a fake store cannot
// express any of it.

func reviewDB(t *testing.T) *sql.DB {
	t.Helper()
	database := isolatedTestDB(t)
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrationsql.FS, Root: "."}
	if _, err := migrate.Exec(database, "mysql", source, migrate.Up); err != nil {
		t.Fatalf("migrate Up: %v", err)
	}
	return database
}

type seedPlugin struct {
	id         string
	typ        string
	visibility string
	// listingState defaults to the pre-listing_state equivalent of `visibility`:
	// `private` meant "draft", anything else meant "listed". Existing tests
	// therefore keep their original meaning without restating it. Tests about the
	// new axis — a space-INTENT draft, a delisted row — set it explicitly.
	listingState   string
	currentVersion string
	// currentVersionID mimics the state a real import leaves behind: a private
	// draft that ALREADY carries a version snapshot.
	currentVersionID string
	embedded         bool
	owner            string
	space            string
	manifest         string
	pkg              string
}

func seed(t *testing.T, database *sql.DB, p seedPlugin) {
	t.Helper()
	if p.owner == "" {
		p.owner = "user-1"
	}
	if p.space == "" {
		p.space = "space-a"
	}
	if p.typ == "" {
		p.typ = "skill"
	}
	if p.manifest == "" {
		p.manifest = `{"plugin_name":"Fixture"}`
	}
	if p.pkg == "" {
		p.pkg = `{"attachments":[]}`
	}
	if p.listingState == "" {
		if p.visibility == "private" {
			p.listingState = "draft"
		} else {
			p.listingState = "published"
		}
	}
	var versionID any
	if p.currentVersionID != "" {
		versionID = p.currentVersionID
	}
	var current any
	if p.currentVersion != "" {
		current = p.currentVersion
	}
	if _, err := database.Exec(`INSERT INTO plugins
		(plugin_id, plugin_name, plugin_type, is_embedded, tags_json, owner_uid, space_id, visibility, listing_state,
		 manifest_json, plugin_json, manifest_hash, plugin_hash, current_version_id, current_version,
		 created_at, updated_at)
		VALUES (?, 'Fixture', ?, ?, JSON_ARRAY(), ?, ?, ?, ?, CAST(? AS JSON), CAST(? AS JSON),
		        REPEAT('a',71), REPEAT('b',71), ?, ?, NOW(3), NOW(3))`,
		p.id, p.typ, p.embedded, p.owner, p.space, p.visibility, p.listingState, p.manifest, p.pkg, versionID, current); err != nil {
		t.Fatalf("seed %s: %v", p.id, err)
	}
	// Every create attaches a visible default placement; the review gate never
	// hides one, so the fixture must carry one too.
	if _, err := database.Exec(`INSERT INTO plugin_placements
		(placement_id, placement_code, plugin_id, visible, sort_order, created_at, updated_at)
		VALUES (CONCAT('pl-', ?), 'default', ?, 1, 0, NOW(3), NOW(3))`, p.id, p.id); err != nil {
		t.Fatalf("seed placement %s: %v", p.id, err)
	}
}

func seedRelation(t *testing.T, database *sql.DB, id, source, target, typ string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO plugin_relations
		(relation_id, source_plugin_id, target_plugin_id, relation_type, sort_order, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, 1, 'user-1', NOW(3), NOW(3))`, id, source, target, typ); err != nil {
		t.Fatalf("seed relation %s: %v", id, err)
	}
}

func tenantScope() pluginrepo.Scope {
	return pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a"}
}

// reviewerScope is a DIFFERENT uid in the same Space. Running the decision
// transactions as the applicant hides every owner-vs-reviewer scoping bug: the
// approve path must lock the plugin by Space, not by owner.
func reviewerScope() pluginrepo.Scope {
	return pluginrepo.Scope{CallerUID: "admin-1", SpaceID: "space-a"}
}

func snapshotOf(manifest, pkg string, relations []model.PluginRelation) pluginrepo.FrozenSnapshot {
	return pluginrepo.FrozenSnapshot{
		Manifest:     json.RawMessage(manifest),
		Package:      json.RawMessage(pkg),
		Relations:    relations,
		ManifestHash: "sha256:frozen-manifest",
		PluginHash:   "sha256:frozen-package",
		// These fixtures stand in for a submit that declared its own content, which
		// is what freezeSubmission produces for every shape except the contentless
		// live-row copy. TestSubmitRefusesAContentlessSnapshotAgainstAPublishedRow
		// is the one case that clears this flag on purpose.
		DeclaredContent: true,
	}
}

func newRequest(pluginID, version string) *model.PluginReviewRequest {
	return &model.PluginReviewRequest{
		PluginID:      pluginID,
		SpaceID:       "space-a",
		TargetScope:   "space",
		Status:        model.ReviewStatusPending,
		Version:       version,
		ApplicantUID:  "user-1",
		ApplicantName: "Alice",
	}
}

// TestInsertReviewRequestDerivesKindFromVisibility pins the rule that decides
// whether approval lists a plugin at all.
//
// SubmitReview is gated on visibility=='space' at the service layer and again
// inside InsertReviewRequest under the plugin lock, so the repository never
// sees a private row here. Fixtures below seed `space,listing_state=draft` to
// model the space-intent first listing the real flow produces; `private,draft`
// seeds that survive are published-without-review PublishPlugin cases (e.g.
// TestApproveListsAStrandedPublishedPrivateRow), which this code never
// receives.
//
// The import path snapshots a version as part of the upload, so a space-intent
// draft normally HAS a current_version_id. Gating "first listing" on the
// ABSENCE of one classifies every real upload as an upgrade, and the isFirst
// branch of ApproveReview — the only code that flips listing_state to
// published — then never runs: the request reaches `approved`, a version is
// minted, and the plugin stays invisible forever with no error anywhere. A
// fixture built through the plugin-write API hides the bug, because that path
// also leaves current_version_id NULL and lands on the correct branch by
// accident.
func TestInsertReviewRequestDerivesKindFromVisibility(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)

	// The state a real upload leaves behind: space-intent draft, but already
	// versioned.
	seed(t, database, seedPlugin{id: "plugin-imported", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "1.0.0"})
	// A space-intent draft with no version at all (created through the write API).
	seed(t, database, seedPlugin{id: "plugin-draft", visibility: "space", listingState: "draft"})
	// A plugin already listed to the org.
	seed(t, database, seedPlugin{id: "plugin-listed", visibility: "space", currentVersionID: "ver-2", currentVersion: "1.0.0"})

	tests := []struct {
		name     string
		pluginID string
		version  string
		want     model.ReviewKind
	}{
		{
			name:     "space-intent draft that import already versioned is still a first listing",
			pluginID: "plugin-imported", version: "9.9.9", want: model.ReviewKindFirst,
		},
		{
			name:     "space-intent draft with no version is a first listing",
			pluginID: "plugin-draft", version: "9.9.9", want: model.ReviewKindFirst,
		},
		{
			name:     "already-listed plugin is an upgrade",
			pluginID: "plugin-listed", version: "9.9.9", want: model.ReviewKindUpgrade,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newRequest(tt.pluginID, tt.version)
			if err := repo.InsertReviewRequest(context.Background(), tenantScope(), req, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
				t.Fatalf("InsertReviewRequest: %v", err)
			}
			if req.Kind != tt.want {
				t.Errorf("kind = %q, want %q", req.Kind, tt.want)
			}
			// The persisted row must agree with what the caller was told; approval
			// reads kind back out of the database, not off this struct.
			var stored string
			if err := database.QueryRow(`SELECT kind FROM plugin_review_requests WHERE review_id=?`, req.ID).Scan(&stored); err != nil {
				t.Fatalf("read back kind: %v", err)
			}
			if model.ReviewKind(stored) != tt.want {
				t.Errorf("persisted kind = %q, want %q", stored, tt.want)
			}
		})
	}
}

// A first listing at the import default label must be accepted: the draft's
// current_version is a DRAFT label, not a published one.
func TestSubmitAcceptsTheDraftLabelOnAFirstListing(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "1.0.0"})
	if err := repo.InsertReviewRequest(context.Background(), tenantScope(), newRequest("plugin-1", "1.0.0"), snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatalf("first listing at the draft label was refused: %v", err)
	}
}

// The sentinel is ErrReviewPending, not ErrConflict. Both are 409, so the status
// code does not discriminate — what does is the conflict_reason the handler
// derives: review_pending carries "cancel the pending request first", which is the
// actual next step, while the generic `state` arm ErrConflict lands on says only
// that something raced. This is the most common way a submit fails, so it is the
// one that least deserves the unactionable reason.
func TestSubmitEnforcesSinglePendingPerPlugin(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft"})
	if err := repo.InsertReviewRequest(context.Background(), tenantScope(), newRequest("plugin-1", "1.0.0"), snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatal(err)
	}
	err := repo.InsertReviewRequest(context.Background(), tenantScope(), newRequest("plugin-1", "1.0.1"), snapshotOf(`{"a":1}`, `{"b":2}`, nil))
	if !errors.Is(err, pluginrepo.ErrReviewPending) {
		t.Fatalf("second pending submit = %v, want ErrReviewPending", err)
	}
	if errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatal("ErrReviewPending must not also satisfy ErrConflict, or the handler's generic arm wins the switch")
	}
}

// Version-label rules: an already-published label is refused, a label from a
// rejected/canceled request is free to reuse. Since plugin_versions.version is a
// counter and never records the applicant's label, the review table itself is the
// published-label record.
func TestSubmitVersionLabelReuseRules(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "0.9.0"})

	// Submit 1.0.0 and reject it.
	first := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), first, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.RejectReview(ctx, reviewerScope(), pluginrepo.RejectReviewParams{
		ReviewID: first.ID, ReviewerUID: "admin-1", ReviewerName: "Adam", Reason: "needs docs",
	}); err != nil {
		t.Fatal(err)
	}
	// The SAME label must be reusable after a rejection — the applicant fixed the
	// content, the version they announced did not change.
	second := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), second, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatalf("a rejected label was not reusable: %v", err)
	}
	// Cancel it and reuse again.
	if _, _, err := repo.CancelReview(ctx, tenantScope(), second.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	third := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), third, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatalf("a canceled label was not reusable: %v", err)
	}
	// Approve it. Now the label is published.
	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: third.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertReviewRequest(ctx, tenantScope(), newRequest("plugin-1", "1.0.0"), snapshotOf(`{"a":1}`, `{"b":2}`, nil)); !errors.Is(err, pluginrepo.ErrLabelTaken) {
		t.Fatalf("republishing an approved label = %v, want ErrLabelTaken", err)
	}
	// A different label is fine.
	if err := repo.InsertReviewRequest(ctx, tenantScope(), newRequest("plugin-1", "1.1.0"), snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatalf("a fresh label was refused: %v", err)
	}
}

// A plugin that was already Space-visible before this feature existed has no
// approved request to compare against, so its current label counts as published
// (grandfathering, with no backfill).
func TestSubmitRefusesTheLiveLabelOfAnAlreadyListedPlugin(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", currentVersionID: "ver-1", currentVersion: "2.4.0"})
	if err := repo.InsertReviewRequest(context.Background(), tenantScope(), newRequest("plugin-1", "2.4.0"), snapshotOf(`{"a":1}`, `{"b":2}`, nil)); !errors.Is(err, pluginrepo.ErrLabelTaken) {
		t.Fatalf("resubmitting the live label = %v, want ErrLabelTaken", err)
	}
}

// The forward-only version rule must be enforced under the plugin row lock, not
// only on the service's earlier unlocked read. This reproduces the exact
// submit-overlapping-approve interleaving:
//
//	current=1.5.0, plugin listed
//	T2's unlocked service check reads 1.5.0 and accepts label 1.6.0 (the freeze
//	  window then runs for seconds)
//	T1 submits 2.0.0 and it is approved  -> current_version = 2.0.0
//	T2's locked InsertReviewRequest finally runs with label 1.6.0
//
// The equality check alone lets 1.6.0 through (it is not in the published set,
// which is {2.0.0}); without the locked ordering re-check the later approval
// would stamp 1.6.0 and regress the plugin's public label 1.5.0 -> 2.0.0 ->
// 1.6.0 permanently. InsertReviewRequest re-derives the rule from the LOCKED
// current_version, so T2 is refused with ErrVersionRegressed.
func TestSubmitReRunsForwardOnlyRuleAgainstLockedCurrentVersion(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", currentVersionID: "ver-1", currentVersion: "1.5.0"})

	// T1 submits 2.0.0 and it is approved: current_version moves forward to 2.0.0.
	// This stands in for the concurrent decision that lands during T2's freeze
	// window; T2 already passed its unlocked check against the stale 1.5.0.
	winner := newRequest("plugin-1", "2.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), winner, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: winner.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	}); err != nil {
		t.Fatal(err)
	}

	// T2's delayed locked insert. 1.6.0 is forward of the stale 1.5.0 T2 saw, but
	// backward of the locked 2.0.0 — it must now be refused rather than queued for
	// an approval that would regress the public label.
	err := repo.InsertReviewRequest(ctx, tenantScope(), newRequest("plugin-1", "1.6.0"), snapshotOf(`{"a":1}`, `{"b":2}`, nil))
	if !errors.Is(err, pluginrepo.ErrVersionRegressed) {
		t.Fatalf("submit racing an approve = %v, want ErrVersionRegressed", err)
	}

	// A label forward of the locked current is still accepted.
	if err := repo.InsertReviewRequest(ctx, tenantScope(), newRequest("plugin-1", "2.1.0"), snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatalf("a label forward of the locked current was refused: %v", err)
	}
}

// TestApproveReRunsForwardOnlyRuleAgainstLockedCurrentVersion pins the apply-time
// half of the forward-only invariant. The submit-time check only guarantees the
// request label was forward-only WHEN IT WAS SUBMITTED; any writer that advances
// current_version while the request sits pending (an admin version edit, or the
// author's own draft label edit while the plugin is not yet listed) invalidates
// that guarantee. ApproveReview stamps the applicant's frozen label, so without a
// re-check under the plugin lock the approval would regress the public version.
func TestApproveReRunsForwardOnlyRuleAgainstLockedCurrentVersion(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", currentVersionID: "ver-1", currentVersion: "1.5.0"})

	// The owner submits 1.6.0. It passes the locked insert check against 1.5.0 and
	// sits pending.
	req := newRequest("plugin-1", "1.6.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatal(err)
	}

	// A concurrent writer advances current_version to 3.0.0 while the request is
	// pending — the admin-edit / draft-label-edit route the reviewers reproduced.
	// The pending request's frozen 1.6.0 is now BACKWARD of the live label.
	if _, err := database.Exec(`UPDATE plugins SET current_version='3.0.0' WHERE plugin_id='plugin-1'`); err != nil {
		t.Fatal(err)
	}

	// Approving now would stamp 1.6.0 over the live 3.0.0 — a public regression.
	// The under-lock re-check must refuse it.
	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	}); !errors.Is(err, pluginrepo.ErrVersionRegressed) {
		t.Fatalf("approve after the version advanced = %v, want ErrVersionRegressed", err)
	}

	// The public label is untouched: the refused approval did not commit.
	var current string
	if err := database.QueryRow(`SELECT current_version FROM plugins WHERE plugin_id='plugin-1'`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != "3.0.0" {
		t.Fatalf("current_version = %q after a refused approval, want 3.0.0 (unchanged)", current)
	}
}

// TestApproveFirstListingAcceptsTheDraftLabel pins the critical nuance of the
// label-reuse guard: on a first listing the draft's current_version IS the
// request label, and the approval must SUCCEED. Refusing equality at approve
// would brick every first listing. publishedVersionLabels is what separates
// "author re-sent their own draft label" (excluded from the published set,
// allowed) from "this label is already published over other content" (in the
// published set, refused).
func TestApproveFirstListingAcceptsTheDraftLabel(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	// A space-intent draft carrying the exact label the submitter sent — the
	// normal first-listing state an import leaves behind.
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "1.0.0"})
	req := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Frozen"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatalf("InsertReviewRequest: %v", err)
	}
	out, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	})
	if err != nil {
		t.Fatalf("approving a first listing at the draft label was refused: %v", err)
	}
	if out.Visibility != model.PluginVisibilitySpace || out.ListingState != model.PluginListingStatePublished {
		t.Fatalf("the first listing did not list the plugin: visibility=%q listing_state=%q", out.Visibility, out.ListingState)
	}
	if out.CurrentVersion == nil || *out.CurrentVersion != "1.0.0" {
		t.Fatalf("current_version = %v, want 1.0.0", out.CurrentVersion)
	}
}

// TestApproveRefusesALabelReusedMidFlight pins fix #1: publishedVersionLabels
// must be re-run against the LOCKED plugin row at approve time, not only at
// insert time. AdminUpdate can move a LISTED plugin's current_version to
// exactly the pending request's label (SnapshotVersion=true, no
// RefusePendingReview / EnforceListingGate), so without the re-check
// snapshotVersion minted an already-published label over different content.
//
// The ordering guard alone (VersionNotRegressed) cannot catch this because it
// returns true unconditionally on equality, and it MUST keep doing so — that
// short-circuit is what lets the previous test (first listing at draft label)
// succeed. It is the published-set check, not equality, that distinguishes the
// two cases.
func TestApproveRefusesALabelReusedMidFlight(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	// A plugin ALREADY listed at 1.0.0. A request for 1.0.0 is refused at insert
	// time, so submit the NEXT label (2.0.0), then advance the live label to 2.0.0
	// the way AdminUpdate would — minting a new current_version over different
	// content while the request sits pending.
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "published", currentVersionID: "ver-1", currentVersion: "1.0.0"})
	req := newRequest("plugin-1", "2.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Frozen 2.0"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatalf("InsertReviewRequest: %v", err)
	}
	// The "AdminUpdate-shaped" writer: SnapshotVersion forces a new version row,
	// mirroring admin.go:326. It moves current_version forward to EXACTLY the
	// request's label, but over content no reviewer saw — that is the bug being
	// pinned.
	if _, err := database.Exec(`UPDATE plugins SET current_version='2.0.0', manifest_json=CAST('{"plugin_name":"Admin-dropped"}' AS JSON), plugin_hash=REPEAT('c',71) WHERE plugin_id='plugin-1'`); err != nil {
		t.Fatal(err)
	}

	_, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	})
	if !errors.Is(err, pluginrepo.ErrLabelTaken) {
		t.Fatalf("approving over a mid-flight label reuse = %v, want ErrLabelTaken", err)
	}

	// The request must NOT be flipped to approved, the live label stays on the
	// admin-dropped content, and no new version was minted by the aborted approval.
	var status string
	if err := database.QueryRow(`SELECT status FROM plugin_review_requests WHERE review_id=?`, req.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(model.ReviewStatusPending) {
		t.Fatalf("the refused approval settled the request: status=%q", status)
	}
	var current, phash string
	if err := database.QueryRow(`SELECT current_version, plugin_hash FROM plugins WHERE plugin_id='plugin-1'`).Scan(&current, &phash); err != nil {
		t.Fatal(err)
	}
	if current != "2.0.0" {
		t.Fatalf("current_version = %q, want 2.0.0 (unchanged by the refused approval)", current)
	}
	if phash != strings.Repeat("c", 71) {
		t.Fatalf("the refused approval overwrote the live content: plugin_hash=%q", phash)
	}
	// No new version row was minted by the aborted approval. The seed never
	// inserted plugin_versions rows (current_version/current_version_id are set
	// directly on plugins, matching what some legacy imports look like), so
	// "rows==0" here is the "nothing added" signal regardless of the seed state.
	// plugin_versions.version is the per-plugin auto-increment counter, not the
	// applicant semver label — look for the counter rather than the label so the
	// assertion doesn't depend on snapshotVersion's internal sequencing.
	var versionRows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id='plugin-1'`).Scan(&versionRows); err != nil {
		t.Fatal(err)
	}
	if versionRows != 0 {
		t.Fatalf("version rows after refused approval = %d, want 0 (no new snapshot minted)", versionRows)
	}
}

// TestSubmitRefusesWhenVisibilityWasLoweredToPrivateMidFlight pins fix #2:
// InsertReviewRequest must re-derive visibility == 'space' under the plugin
// lock, not trust the service's earlier unlocked Detail read. A concurrent
// owner upsert can lower a still-draft row to private between the unlocked
// check and the insert transaction; RefusePendingReview finds no request row
// yet so the update wins, and without the in-tx check the insert lands against
// a now-private row that ApproveReview's isFirst branch would flip to space
// against the author's since-changed intent. Mirrors
// TestPublishRefusesAPluginWithAReviewSubmittedMidFlight for the symmetric
// window.
func TestSubmitRefusesWhenVisibilityWasLoweredToPrivateMidFlight(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	// A space-intent draft, the state SubmitReview expects to find.
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "1.0.0"})

	// The interleaving: between the service's unlocked Detail read and
	// InsertReviewRequest, the owner lowers visibility to private. This is legal
	// on a draft; Repo.Update's RefusePendingReview check sees no pending request
	// (the insert hasn't committed yet), so the update wins the plugin row lock.
	// Reproduce by raw UPDATE.
	if _, err := database.Exec(`UPDATE plugins SET visibility='private' WHERE plugin_id='plugin-1'`); err != nil {
		t.Fatal(err)
	}

	err := repo.InsertReviewRequest(ctx, tenantScope(), newRequest("plugin-1", "1.0.1"), snapshotOf(`{"plugin_name":"V2"}`, `{"attachments":[]}`, nil))
	if !errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatalf("submit against a privately-lowered row = %v, want ErrConflict", err)
	}
	var pending int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_review_requests WHERE plugin_id='plugin-1'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("a request row was written against a private plugin: %d rows", pending)
	}
	// The plugin stays private: the refused insert did not widen anything.
	var vis string
	if err := database.QueryRow(`SELECT visibility FROM plugins WHERE plugin_id='plugin-1'`).Scan(&vis); err != nil {
		t.Fatal(err)
	}
	if vis != "private" {
		t.Fatalf("visibility = %q after the refused insert, want private", vis)
	}
}

// A contentless submit is legal on a draft (the draft row IS what the reviewer
// should see) and illegal on a published plugin (freezing the live content makes
// the review theatre and approval would only mint a label for content that has
// already shipped). The service refuses it from an UNLOCKED read taken before the
// freeze window; this pins the under-lock twin, i.e. the interleaving where a
// concurrent approval promoted the row to published after that read. Seeding the
// row as published is exactly the state the locked read finds in that race.
func TestSubmitRefusesAContentlessSnapshotAgainstAPublishedRow(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "published", currentVersionID: "ver-1", currentVersion: "1.0.0"})

	contentless := snapshotOf(`{"plugin_name":"Live row"}`, `{"attachments":[]}`, nil)
	contentless.DeclaredContent = false
	err := repo.InsertReviewRequest(ctx, tenantScope(), newRequest("plugin-1", "1.1.0"), contentless)
	if !errors.Is(err, pluginrepo.ErrReviewContentRequired) {
		t.Fatalf("contentless submit against a published row = %v, want ErrReviewContentRequired", err)
	}
	var pending int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_review_requests WHERE plugin_id='plugin-1'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("a contentless request row was written against a published plugin: %d rows", pending)
	}
	// The same insert with declared content is accepted, so the guard is keyed on
	// the flag and not on something incidental to this fixture.
	if err := repo.InsertReviewRequest(ctx, tenantScope(), newRequest("plugin-1", "1.1.0"),
		snapshotOf(`{"plugin_name":"Declared"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatalf("declared-content submit against the same row was refused: %v", err)
	}
}

// A first listing may still snapshot the live draft row: the content-required
// rule is keyed on the LOCKED listing_state, so clamping it to every submit would
// break the ordinary "submit my draft for review" path.
func TestSubmitAllowsAContentlessSnapshotOnAFirstListing(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "1.0.0"})

	contentless := snapshotOf(`{"plugin_name":"Draft"}`, `{"attachments":[]}`, nil)
	contentless.DeclaredContent = false
	req := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, contentless); err != nil {
		t.Fatalf("contentless first-listing submit was refused: %v", err)
	}
	if req.Kind != model.ReviewKindFirst {
		t.Fatalf("kind = %q, want first", req.Kind)
	}
}

// The whole point of the gate: approve is what makes a plugin org-visible, and it
// applies the FROZEN documents rather than whatever the draft says now.
func TestApproveFlipsVisibilityAndAppliesTheFrozenSnapshot(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "0.9.0"})

	req := newRequest("plugin-1", "1.0.0")
	changelog := "first release"
	req.Changelog = &changelog
	frozenManifest := `{"plugin_name":"Frozen"}`
	frozenPackage := `{"attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"frozen"}]}`
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(frozenManifest, frozenPackage, nil)); err != nil {
		t.Fatal(err)
	}
	// The author keeps editing the draft after submitting. None of it may leak
	// into what the reviewer approves.
	if _, err := database.Exec(`UPDATE plugins SET manifest_json=CAST('{"plugin_name":"Edited after submit"}' AS JSON), plugin_hash=REPEAT('z',71) WHERE plugin_id='plugin-1'`); err != nil {
		t.Fatal(err)
	}

	out, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam", RequestID: "req-1",
	})
	if err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}
	if out.Visibility != model.PluginVisibilitySpace {
		t.Fatalf("visibility = %q, want space", out.Visibility)
	}
	if out.CurrentVersion == nil || *out.CurrentVersion != "1.0.0" {
		t.Fatalf("current_version = %v, want the applicant's label", out.CurrentVersion)
	}
	if out.PluginHash != "sha256:frozen-package" || out.ManifestHash != "sha256:frozen-manifest" {
		t.Fatalf("hashes = %q/%q; the live draft leaked into the approval", out.ManifestHash, out.PluginHash)
	}
	if !containsJSON(string(out.Manifest), "Frozen") {
		t.Fatalf("manifest = %s, want the frozen document", out.Manifest)
	}

	// The placement is untouched: it was visible before and stays visible. Hiding
	// it for private drafts would also hide them from their author's own list,
	// which INNER JOINs the placement on visible=1.
	var visible bool
	if err := database.QueryRow(`SELECT visible FROM plugin_placements WHERE plugin_id='plugin-1' AND placement_code='default'`).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if !visible {
		t.Error("the default placement is hidden after approval")
	}

	// The release history row: plugin_versions.version stays the auto-increment
	// counter. A semver label written there would corrupt the sequence.
	var versionSeq, changelogOut string
	if err := database.QueryRow(`SELECT version, changelog FROM plugin_versions WHERE plugin_id='plugin-1'`).Scan(&versionSeq, &changelogOut); err != nil {
		t.Fatalf("read version row: %v", err)
	}
	if versionSeq != "1" {
		t.Errorf("plugin_versions.version = %q, want the counter %q", versionSeq, "1")
	}
	if changelogOut != changelog {
		t.Errorf("version changelog = %q, want %q", changelogOut, changelog)
	}
	// And the audit trail records who decided it and from where.
	var action, remark string
	if err := database.QueryRow(`SELECT action, remark FROM plugin_audit_logs WHERE plugin_id='plugin-1' ORDER BY created_at DESC LIMIT 1`).Scan(&action, &remark); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if action != "review_approve" || remark != "decision_source=web" {
		t.Fatalf("audit = %q/%q", action, remark)
	}
}

// TestApproveReDerivesDenormalizedNameAndTagsFromFrozenManifest pins P1-3: the
// denormalized plugin_name/tags_json columns must be re-derived from the frozen
// manifest at approval, not left at whatever the author edited the live row to
// after submitting. While a plugin is still an unlisted draft the ordinary write
// path lets the author rename/retag it; that edit is unreviewed, so approval must
// overwrite the row's name/tags with the reviewed manifest's values. Otherwise
// the row is self-inconsistent: reviewed manifest content under an unreviewed
// display name.
func TestApproveReDerivesDenormalizedNameAndTagsFromFrozenManifest(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "0.9.0"})

	req := newRequest("plugin-1", "1.0.0")
	frozenManifest := `{"plugin_name":"Reviewed Name","labels":["reviewed"]}`
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(frozenManifest, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	// After submitting, the author renames and retags the live draft. Neither the
	// name nor the tags were reviewed, so neither may survive approval.
	if _, err := database.Exec(`UPDATE plugins SET plugin_name='Sneaky Rename', tags_json=CAST('["sneaky"]' AS JSON) WHERE plugin_id='plugin-1'`); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam", RequestID: "req-1",
	}); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}

	var name, tags string
	if err := database.QueryRow(`SELECT plugin_name, tags_json FROM plugins WHERE plugin_id='plugin-1'`).Scan(&name, &tags); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if name != "Reviewed Name" {
		t.Errorf("plugin_name = %q, want the frozen manifest's %q (post-submit rename leaked)", name, "Reviewed Name")
	}
	var gotTags []string
	if err := json.Unmarshal([]byte(tags), &gotTags); err != nil {
		t.Fatalf("decode tags_json %q: %v", tags, err)
	}
	if len(gotTags) != 1 || gotTags[0] != "reviewed" {
		t.Errorf("tags_json = %q, want the frozen manifest's labels [\"reviewed\"] (post-submit retag leaked)", tags)
	}
}

// The container case: an expert_team whose membership changed between submit and
// approve must ship the FROZEN members. Freezing only the documents ships the
// reviewed manifest next to the live membership and records zero relations on the
// minted version.
func TestApproveAppliesTheFrozenRelationGraph(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "team-1", typ: "expert_team", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "0.9.0"})
	seed(t, database, seedPlugin{id: "member-a", typ: "expert", visibility: "private", embedded: true})
	seed(t, database, seedPlugin{id: "member-b", typ: "expert", visibility: "private", embedded: true})
	seedRelation(t, database, "rel-a", "team-1", "member-a", "expert_team_expert")

	frozen := []model.PluginRelation{{
		ID: "rel-a", SourcePluginID: "team-1", TargetPluginID: "member-a",
		Type: "expert_team_expert", SortOrder: 0, Status: 1,
	}}
	req := newRequest("team-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Team"}`, `{"attachments":[]}`, frozen)); err != nil {
		t.Fatal(err)
	}
	// After submitting, the author adds a second member. The reviewer never saw it.
	seedRelation(t, database, "rel-b", "team-1", "member-b", "expert_team_expert")

	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	}); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}

	live := map[string]bool{}
	rows, err := database.Query(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id='team-1' AND deleted_at IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			t.Fatal(err)
		}
		live[target] = true
	}
	if !live["member-a"] {
		t.Error("the frozen member was not applied")
	}
	if live["member-b"] {
		t.Error("an unreviewed member was shipped by the approval")
	}

	// The minted version must record the frozen graph, not an empty array.
	var relationsJSON string
	if err := database.QueryRow(`SELECT relations_json FROM plugin_versions WHERE plugin_id='team-1'`).Scan(&relationsJSON); err != nil {
		t.Fatal(err)
	}
	if !containsJSON(relationsJSON, "member-a") {
		t.Fatalf("version relations_json = %s, want the frozen member", relationsJSON)
	}
	if containsJSON(relationsJSON, "member-b") {
		t.Fatalf("version relations_json shipped the live member: %s", relationsJSON)
	}

	// A listed container whose embedded parts stayed private is uninstallable by
	// anyone but its author (resolveInstallDetail refuses a partially visible
	// topology), so the frozen children follow the top's visibility. The member
	// dropped from the frozen graph must NOT be promoted.
	var visA, visB string
	if err := database.QueryRow(`SELECT visibility FROM plugins WHERE plugin_id='member-a'`).Scan(&visA); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT visibility FROM plugins WHERE plugin_id='member-b'`).Scan(&visB); err != nil {
		t.Fatal(err)
	}
	if visA != "space" {
		t.Errorf("member-a visibility = %q, want space", visA)
	}
	if visB != "private" {
		t.Errorf("member-b visibility = %q, want private (it was not in the reviewed graph)", visB)
	}
}

// An upgrade swaps the listed content and does not re-flip visibility.
func TestApproveUpgradeKeepsTheListingAndSwapsContent(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", currentVersionID: "ver-1", currentVersion: "1.0.0"})

	req := newRequest("plugin-1", "2.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"V2"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	if req.Kind != model.ReviewKindUpgrade {
		t.Fatalf("kind = %q, want upgrade", req.Kind)
	}
	// While the request is pending, the listed version is still the old one.
	var liveVersion string
	if err := database.QueryRow(`SELECT current_version FROM plugins WHERE plugin_id='plugin-1'`).Scan(&liveVersion); err != nil {
		t.Fatal(err)
	}
	if liveVersion != "1.0.0" {
		t.Fatalf("pending review already changed the listed version to %q", liveVersion)
	}
	out, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Visibility != model.PluginVisibilitySpace {
		t.Fatalf("visibility = %q", out.Visibility)
	}
	if out.CurrentVersion == nil || *out.CurrentVersion != "2.0.0" {
		t.Fatalf("current_version = %v", out.CurrentVersion)
	}
}

// A reject changes nothing about the plugin: a space-intent draft stays a
// draft, neither visibility nor listing_state move.
func TestRejectLeavesThePluginUntouched(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "0.9.0"})
	req := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Frozen"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.RejectReview(ctx, reviewerScope(), pluginrepo.RejectReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam", Reason: "needs docs",
	}); err != nil {
		t.Fatal(err)
	}
	var visibility, listingState, currentVersion string
	if err := database.QueryRow(`SELECT visibility, listing_state, current_version FROM plugins WHERE plugin_id='plugin-1'`).Scan(&visibility, &listingState, &currentVersion); err != nil {
		t.Fatal(err)
	}
	if visibility != "space" || listingState != "draft" || currentVersion != "0.9.0" {
		t.Fatalf("reject changed the plugin: %q/%q/%q", visibility, listingState, currentVersion)
	}
	var versions int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id='plugin-1'`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 0 {
		t.Errorf("a rejected submission minted %d version rows", versions)
	}
	var status, reason, source string
	if err := database.QueryRow(`SELECT status, reason, decision_source FROM plugin_review_requests WHERE review_id=?`, req.ID).Scan(&status, &reason, &source); err != nil {
		t.Fatal(err)
	}
	if status != "rejected" || reason != "needs docs" || source != "web" {
		t.Fatalf("request = %q/%q/%q", status, reason, source)
	}
}

// Cancel is applicant-only, and an already-decided request is a CONFLICT — not a
// 404 telling the applicant their just-approved request never existed.
func TestCancelIsApplicantOnlyAndConflictsWhenDecided(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft"})
	req := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatal(err)
	}
	// Someone else cannot cancel it, and gets no hint that it exists.
	if _, _, err := repo.CancelReview(ctx, tenantScope(), req.ID, "user-9"); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cancel by a non-applicant = %v, want ErrNotFound", err)
	}
	// Neither can a caller in another Space.
	if _, _, err := repo.CancelReview(ctx, pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-b"}, req.ID, "user-1"); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-Space cancel = %v, want ErrNotFound", err)
	}
	if _, _, err := repo.CancelReview(ctx, tenantScope(), req.ID, "user-1"); err != nil {
		t.Fatalf("applicant cancel: %v", err)
	}
	// Cancelling twice, or cancelling after a decision, is a conflict.
	if _, _, err := repo.CancelReview(ctx, tenantScope(), req.ID, "user-1"); !errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatalf("second cancel = %v, want ErrConflict", err)
	}
}

// A cross-Space decision must be indistinguishable from a missing one, and must
// not mutate anything.
func TestDecisionsAreSpaceScoped(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft"})
	req := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatal(err)
	}
	foreign := pluginrepo.Scope{CallerUID: "admin-2", SpaceID: "space-b"}
	if _, err := repo.ApproveReview(ctx, foreign, pluginrepo.ApproveReviewParams{ReviewID: req.ID, ReviewerUID: "admin-2"}); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-Space approve = %v, want ErrNotFound", err)
	}
	if _, _, err := repo.RejectReview(ctx, foreign, pluginrepo.RejectReviewParams{ReviewID: req.ID, ReviewerUID: "admin-2", Reason: "no"}); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-Space reject = %v, want ErrNotFound", err)
	}
	var status, visibility string
	if err := database.QueryRow(`SELECT rr.status, p.visibility FROM plugin_review_requests rr JOIN plugins p ON p.plugin_id=rr.plugin_id WHERE rr.review_id=?`, req.ID).Scan(&status, &visibility); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || visibility != "space" {
		t.Fatalf("a cross-Space decision mutated state: %q/%q", status, visibility)
	}
	// A reviewer in the right Space still succeeds, so the refusal above was the
	// Space predicate and not a broken fixture.
	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam"}); err != nil {
		t.Fatalf("in-Space approve: %v", err)
	}
}

// Two admins deciding at once: exactly one wins, and the loser gets a typed
// conflict carrying the committed outcome rather than a 404 or a 500.
func TestConcurrentDecisionsProduceOneWinner(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft"})
	req := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = repo.ApproveReview(ctx, pluginrepo.Scope{CallerUID: "admin-1", SpaceID: "space-a"},
			pluginrepo.ApproveReviewParams{ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam"})
	}()
	go func() {
		defer wg.Done()
		_, _, errs[1] = repo.RejectReview(ctx, pluginrepo.Scope{CallerUID: "admin-2", SpaceID: "space-a"},
			pluginrepo.RejectReviewParams{ReviewID: req.ID, ReviewerUID: "admin-2", ReviewerName: "Ada", Reason: "no"})
	}()
	wg.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, pluginrepo.ErrConflict):
			// The expected loser outcome.
		default:
			t.Fatalf("decider %d got a non-conflict error: %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1 (errs=%v)", winners, errs)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM plugin_review_requests WHERE review_id=?`, req.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "approved" && status != "rejected" {
		t.Fatalf("terminal status = %q", status)
	}
	var versions int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id='plugin-1'`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if status == "approved" && versions != 1 {
		t.Errorf("approved once but minted %d versions", versions)
	}
	if status == "rejected" && versions != 0 {
		t.Errorf("rejected but minted %d versions", versions)
	}
}

// Deciding an already-settled request is a conflict; deciding a nonexistent one
// is not found. The IM callback needs the distinction to answer `conflict` with a
// real state instead of retrying the event into the DLQ.
func TestDecidingASettledRequestConflictsAndAMissingOneIsNotFound(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft"})
	req := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam"}); !errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatalf("re-approve = %v, want ErrConflict", err)
	}
	if _, _, err := repo.RejectReview(ctx, reviewerScope(), pluginrepo.RejectReviewParams{ReviewID: req.ID, ReviewerUID: "admin-1", Reason: "no"}); !errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatalf("reject after approve = %v, want ErrConflict", err)
	}
	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{ReviewID: "review-nope", ReviewerUID: "admin-1"}); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("approve of a missing request = %v, want ErrNotFound", err)
	}
}

// Reads are scoped: an applicant sees only their own requests, a reviewer sees
// the whole Space, and neither sees across Spaces.
func TestReviewReadsAreScoped(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	// Review requests only ever live against space-intent drafts (SubmitReview and
	// InsertReviewRequest both refuse visibility!=space). Use owner-distinct
	// space,draft rows to test applicant scoping.
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", owner: "user-1"})
	seed(t, database, seedPlugin{id: "plugin-2", visibility: "space", listingState: "draft", owner: "user-2"})

	mine := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), mine, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatal(err)
	}
	theirs := newRequest("plugin-2", "1.0.0")
	theirs.ApplicantUID = "user-2"
	theirs.ApplicantName = "Bob"
	if err := repo.InsertReviewRequest(ctx, pluginrepo.Scope{CallerUID: "user-2", SpaceID: "space-a"}, theirs, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatal(err)
	}

	// An applicant reading someone else's request is a miss, not a 403 that
	// confirms it exists.
	if _, err := repo.GetReviewRequest(ctx, tenantScope(), theirs.ID, false); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("applicant read of another request = %v, want ErrNotFound", err)
	}
	if _, err := repo.GetReviewRequest(ctx, reviewerScope(), theirs.ID, true); err != nil {
		t.Fatalf("reviewer read = %v", err)
	}
	// Cross-Space, even as a reviewer.
	if _, err := repo.GetReviewRequest(ctx, pluginrepo.Scope{CallerUID: "admin-9", SpaceID: "space-b"}, theirs.ID, true); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("cross-Space reviewer read = %v, want ErrNotFound", err)
	}

	items, total, err := repo.ListReviewRequests(ctx, tenantScope(), pluginrepo.ReviewListFilter{SpaceID: "space-a", ApplicantUID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != mine.ID {
		t.Fatalf("mine list = %d/%d", total, len(items))
	}
	items, total, err = repo.ListReviewRequests(ctx, reviewerScope(), pluginrepo.ReviewListFilter{SpaceID: "space-a"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("space list = %d/%d", total, len(items))
	}
	_, total, err = repo.ListReviewRequests(ctx, pluginrepo.Scope{CallerUID: "admin-9", SpaceID: "space-b"}, pluginrepo.ReviewListFilter{SpaceID: "space-b"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("another Space's queue total = %d, want 0", total)
	}

	// The list must not carry the frozen snapshot columns; the detail read must.
	list, _, err := repo.ListReviewRequests(ctx, reviewerScope(), pluginrepo.ReviewListFilter{SpaceID: "space-a"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if len(item.PluginJSON) != 0 || len(item.ManifestJSON) != 0 || len(item.RelationsJSON) != 0 {
			t.Errorf("list row %s carries snapshot bytes", item.ID)
		}
	}
	detail, err := repo.LoadReviewSnapshot(ctx, reviewerScope(), mine.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.PluginJSON) == 0 || len(detail.ManifestJSON) == 0 || len(detail.RelationsJSON) == 0 {
		t.Fatalf("detail is missing snapshot bytes: %+v", detail)
	}
}

// The IM callback's cross-scope read is the one query with no tenant predicate.
// It must still refuse a nonexistent id, and the receipt table must make a
// replayed event a no-op.
func TestCardActionReceiptRoundTrip(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft"})
	req := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"a":1}`, `{"b":2}`, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetReviewRequestAnySpace(ctx, req.ID); err != nil {
		t.Fatalf("AnySpace read: %v", err)
	}
	if _, err := repo.GetReviewRequestAnySpace(ctx, "review-nope"); !errors.Is(err, pluginrepo.ErrNotFound) {
		t.Fatalf("AnySpace read of a missing id = %v, want ErrNotFound", err)
	}
	if got, err := repo.GetCardActionReceipt(ctx, "9007199254740993"); err != nil || got != nil {
		t.Fatalf("unseen receipt = %v/%v, want nil/nil", got, err)
	}
	rec := &model.CardActionReceipt{EventID: "9007199254740993", ReviewID: req.ID, Decision: "approve", StoredResponse: `{"disposition":"applied","state":"approved"}`}
	if err := repo.InsertCardActionReceipt(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetCardActionReceipt(ctx, "9007199254740993")
	if err != nil || got == nil {
		t.Fatalf("stored receipt = %v/%v", got, err)
	}
	if got.StoredResponse != rec.StoredResponse {
		t.Fatalf("stored response = %q, want it byte-identical for replay", got.StoredResponse)
	}
	if err := repo.InsertCardActionReceipt(ctx, rec); !errors.Is(err, pluginrepo.ErrConflict) {
		t.Fatalf("duplicate receipt = %v, want ErrConflict", err)
	}
}

func containsJSON(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// The end the whole feature exists for: before approval a colleague cannot see
// the plugin in the market list, and after approval they can. Every other test in
// this file asserts the visibility COLUMN; this one asserts the observable market
// behaviour the column is supposed to produce, through the same List query the API
// serves.
//
// The author's own view is asserted on BOTH surfaces, because they now differ: an
// unpublished draft is absent from the market grid even for its owner (that
// absence is what distinguishes "saved a draft" from "published"), but present in
// the mode=mine listing that backs 我的发布. Approval adds it to the grid.
func TestApprovalIsWhatMakesAPluginVisibleToTheOrg(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{id: "plugin-1", visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "0.9.0"})

	author := tenantScope()
	colleague := pluginrepo.Scope{CallerUID: "user-2", SpaceID: "space-a"}
	outsider := pluginrepo.Scope{CallerUID: "user-3", SpaceID: "space-b"}

	visibleTo := func(t *testing.T, sc pluginrepo.Scope) bool {
		t.Helper()
		items, _, err := repo.List(ctx, sc, pluginrepo.ListFilter{PlacementCode: "default"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, item := range items {
			if item.ID == "plugin-1" {
				return true
			}
		}
		return false
	}
	// mineListing is the 我的发布 query: the same predicate plus an owner narrowing,
	// and deliberately WITHOUT the listed-only grid rule.
	inMineListing := func(t *testing.T, sc pluginrepo.Scope) bool {
		t.Helper()
		items, _, err := repo.List(ctx, sc, pluginrepo.ListFilter{PlacementCode: "default", Mine: true})
		if err != nil {
			t.Fatalf("List(mine): %v", err)
		}
		for _, item := range items {
			if item.ID == "plugin-1" {
				return true
			}
		}
		return false
	}

	if visibleTo(t, author) {
		t.Fatal("an unpublished draft is in the market grid; saving a draft and publishing look the same")
	}
	if !inMineListing(t, author) {
		t.Fatal("the author cannot see their own draft in 我的发布; the draft is unreachable")
	}
	if visibleTo(t, colleague) {
		t.Fatal("a space-intent draft is already visible to the org; the review gate is open")
	}
	if inMineListing(t, colleague) {
		t.Fatal("another member's mine listing returned a draft they do not own")
	}

	req := newRequest("plugin-1", "1.0.0")
	if err := repo.InsertReviewRequest(ctx, author, req, snapshotOf(`{"plugin_name":"Frozen"}`, `{"attachments":[]}`, nil)); err != nil {
		t.Fatal(err)
	}
	// A PENDING request must not list it — only an approved one does.
	if visibleTo(t, colleague) {
		t.Fatal("a pending request listed the plugin")
	}

	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	}); err != nil {
		t.Fatal(err)
	}
	if !visibleTo(t, colleague) {
		t.Fatal("an APPROVED plugin is still invisible to the org; approval did nothing")
	}
	if !visibleTo(t, author) {
		t.Fatal("approval hid the plugin from its own author")
	}
	if visibleTo(t, outsider) {
		t.Fatal("approval leaked the plugin into another Space")
	}
}

// The upgrade flow end to end: submitting content leaves the LISTED row
// byte-identical (the org keeps reading the old version), and approving swaps
// documents, hashes, version label and relations together.
func TestUpgradeSubmitLeavesTheListedRowUntouchedAndApproveSwapsIt(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()
	seed(t, database, seedPlugin{
		id: "expert-1", typ: "expert", visibility: "space",
		currentVersionID: "ver-1", currentVersion: "1.0.0",
		manifest: `{"plugin_name":"Live v1"}`, pkg: `{"attachments":[{"path":"AGENTS.md","content_type":"raw","raw_content":"live v1"}]}`,
	})
	seed(t, database, seedPlugin{id: "skill-old", typ: "skill", visibility: "space", embedded: true})
	// skill-new is a STANDALONE catalog skill, not an embedded one. lockRelationTargets
	// refuses to adopt an embedded child that is not already this source's own — a
	// later container reupload would soft-delete it out from under the adopter — so a
	// reviewed graph can add standalone targets and drop existing edges, but cannot
	// mint new embedded children. Those only come from the container import path.
	seed(t, database, seedPlugin{id: "skill-new", typ: "skill", visibility: "space"})
	seedRelation(t, database, "rel-old", "expert-1", "skill-old", "expert_skill")

	type liveRow struct {
		manifest, pkg, mhash, phash, version, visibility string
	}
	readLive := func(t *testing.T) liveRow {
		t.Helper()
		var row liveRow
		if err := database.QueryRow(`SELECT manifest_json, plugin_json, manifest_hash, plugin_hash, current_version, visibility
			FROM plugins WHERE plugin_id='expert-1'`).
			Scan(&row.manifest, &row.pkg, &row.mhash, &row.phash, &row.version, &row.visibility); err != nil {
			t.Fatal(err)
		}
		return row
	}
	before := readLive(t)

	// The submission carries its OWN content and its own membership: skill-old out,
	// skill-new in.
	frozen := []model.PluginRelation{{
		SourcePluginID: "expert-1", TargetPluginID: "skill-new",
		Type: "expert_skill", SortOrder: 0, Status: 1,
	}}
	req := newRequest("expert-1", "2.0.0")
	if err := repo.InsertReviewRequest(ctx, tenantScope(), req,
		snapshotOf(`{"plugin_name":"Reviewed v2"}`, `{"attachments":[{"path":"AGENTS.md","content_type":"raw","raw_content":"reviewed v2"}]}`, frozen)); err != nil {
		t.Fatal(err)
	}
	if req.Kind != model.ReviewKindUpgrade {
		t.Fatalf("kind = %q, want upgrade", req.Kind)
	}
	// Nothing about the listed row may move at submit time.
	if after := readLive(t); after != before {
		t.Fatalf("submitting an upgrade changed the LIVE row:\nbefore %+v\nafter  %+v", before, after)
	}
	var liveTargets int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_relations WHERE source_plugin_id='expert-1' AND target_plugin_id='skill-new' AND deleted_at IS NULL`).Scan(&liveTargets); err != nil {
		t.Fatal(err)
	}
	if liveTargets != 0 {
		t.Fatal("submitting an upgrade already applied the reviewed membership")
	}

	if _, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
		ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
	}); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}
	after := readLive(t)
	if !containsJSON(after.manifest, "Reviewed v2") {
		t.Errorf("manifest = %s, want the reviewed content", after.manifest)
	}
	if !containsJSON(after.pkg, "reviewed v2") {
		t.Errorf("plugin_json = %s, want the reviewed content", after.pkg)
	}
	if after.mhash != "sha256:frozen-manifest" || after.phash != "sha256:frozen-package" {
		t.Errorf("hashes = %q/%q, want the frozen ones", after.mhash, after.phash)
	}
	if after.version != "2.0.0" {
		t.Errorf("current_version = %q, want 2.0.0", after.version)
	}
	if after.visibility != "space" {
		t.Errorf("visibility = %q; an upgrade must not change it", after.visibility)
	}
	live := map[string]bool{}
	rows, err := database.Query(`SELECT target_plugin_id FROM plugin_relations WHERE source_plugin_id='expert-1' AND deleted_at IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			t.Fatal(err)
		}
		live[target] = true
	}
	if !live["skill-new"] || live["skill-old"] {
		t.Fatalf("membership after approve = %v, want exactly the reviewed graph", live)
	}
}

// Approval promises "this plugin is in the market now", and the market list needs
// TWO things to be true: the visibility predicate AND an INNER JOIN on a
// `visible=1` default placement. Flipping visibility alone therefore lists nothing
// for a row whose placement is hidden (publish-era `visible=0`) or missing
// (pre-auto-placement legacy) — and the author cannot repair it by saving, because
// a listed plugin's ordinary write path is 409 listed_requires_review. So approve
// self-heals the placement forward, in the same transaction, and never hides one.
func TestApproveSelfHealsTheDefaultPlacement(t *testing.T) {
	database := reviewDB(t)
	repo := pluginrepo.New(database)
	ctx := context.Background()

	for _, id := range []string{"plugin-hidden", "plugin-missing", "plugin-visible"} {
		seed(t, database, seedPlugin{id: id, visibility: "space", listingState: "draft", currentVersionID: "ver-1", currentVersion: "0.9.0"})
	}
	// A publish-era row: the placement exists but was hidden when publish was removed.
	if _, err := database.Exec(`UPDATE plugin_placements SET visible=0 WHERE plugin_id='plugin-hidden'`); err != nil {
		t.Fatalf("hide placement: %v", err)
	}
	// A pre-auto-placement row: no placement at all. It carries a category, which
	// the inserted placement must pick up.
	if _, err := database.Exec(`DELETE FROM plugin_placements WHERE plugin_id='plugin-missing'`); err != nil {
		t.Fatalf("drop placement: %v", err)
	}
	if _, err := database.Exec(`UPDATE plugins SET category_id='cat-legacy' WHERE plugin_id='plugin-missing'`); err != nil {
		t.Fatalf("set category: %v", err)
	}
	// The healthy row's placement must not be rewritten at all, so remember it.
	var healthyBefore time.Time
	if err := database.QueryRow(`SELECT updated_at FROM plugin_placements WHERE plugin_id='plugin-visible' AND placement_code='default'`).Scan(&healthyBefore); err != nil {
		t.Fatalf("read healthy placement: %v", err)
	}

	approve := func(t *testing.T, pluginID string) {
		t.Helper()
		req := newRequest(pluginID, "1.0.0")
		if err := repo.InsertReviewRequest(ctx, tenantScope(), req, snapshotOf(`{"plugin_name":"Frozen"}`, `{"attachments":[]}`, nil)); err != nil {
			t.Fatalf("InsertReviewRequest: %v", err)
		}
		out, err := repo.ApproveReview(ctx, reviewerScope(), pluginrepo.ApproveReviewParams{
			ReviewID: req.ID, ReviewerUID: "admin-1", ReviewerName: "Adam",
		})
		if err != nil {
			t.Fatalf("ApproveReview: %v", err)
		}
		if out.Visibility != model.PluginVisibilitySpace {
			t.Fatalf("visibility = %q, want space", out.Visibility)
		}
	}

	// listedToTheOrg drives the REAL market list as a colleague: the only proof
	// that matters is whether the placement JOIN lets the plugin through.
	listedToTheOrg := func(t *testing.T, pluginID string) bool {
		t.Helper()
		items, _, err := repo.List(ctx, pluginrepo.Scope{CallerUID: "user-2", SpaceID: "space-a"},
			pluginrepo.ListFilter{PlacementCode: "default"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, item := range items {
			if item.ID == pluginID {
				return true
			}
		}
		return false
	}

	type placementRow struct {
		rows     int
		visible  bool
		category sql.NullString
		updated  time.Time
	}
	readPlacement := func(t *testing.T, pluginID string) placementRow {
		t.Helper()
		var out placementRow
		if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_placements WHERE plugin_id=? AND placement_code='default'`, pluginID).Scan(&out.rows); err != nil {
			t.Fatalf("count placements: %v", err)
		}
		if out.rows == 0 {
			return out
		}
		if err := database.QueryRow(`SELECT visible,category_id,updated_at FROM plugin_placements WHERE plugin_id=? AND placement_code='default'`, pluginID).
			Scan(&out.visible, &out.category, &out.updated); err != nil {
			t.Fatalf("read placement: %v", err)
		}
		return out
	}

	t.Run("a hidden default placement is flipped visible", func(t *testing.T) {
		if listedToTheOrg(t, "plugin-hidden") {
			t.Fatal("a private draft with a hidden placement is already listed")
		}
		approve(t, "plugin-hidden")
		got := readPlacement(t, "plugin-hidden")
		if got.rows != 1 {
			t.Fatalf("placement rows = %d, want exactly 1", got.rows)
		}
		if !got.visible {
			t.Error("approval left the default placement hidden; the plugin is approved but unlistable")
		}
		if !listedToTheOrg(t, "plugin-hidden") {
			t.Error("the approved plugin is still filtered out of the market list")
		}
	})

	t.Run("a missing default placement is inserted", func(t *testing.T) {
		if got := readPlacement(t, "plugin-missing"); got.rows != 0 {
			t.Fatalf("fixture has %d placements, want none", got.rows)
		}
		approve(t, "plugin-missing")
		got := readPlacement(t, "plugin-missing")
		if got.rows != 1 {
			t.Fatalf("placement rows = %d, want exactly 1 inserted row", got.rows)
		}
		if !got.visible {
			t.Error("the inserted placement is hidden")
		}
		if got.category.String != "cat-legacy" {
			t.Errorf("inserted placement category = %q, want the plugin's own category", got.category.String)
		}
		if !listedToTheOrg(t, "plugin-missing") {
			t.Error("the approved plugin is still filtered out of the market list")
		}
	})

	t.Run("an already-visible placement is untouched", func(t *testing.T) {
		approve(t, "plugin-visible")
		got := readPlacement(t, "plugin-visible")
		if got.rows != 1 {
			t.Fatalf("placement rows = %d, want exactly 1 (no duplicate insert)", got.rows)
		}
		if !got.visible {
			t.Fatal("the healthy placement was hidden by approval")
		}
		if !got.updated.Equal(healthyBefore) {
			t.Errorf("updated_at moved from %v to %v; the healthy path must be a no-op", healthyBefore, got.updated)
		}
		if !listedToTheOrg(t, "plugin-visible") {
			t.Error("the approved plugin is not in the market list")
		}
	})
}
