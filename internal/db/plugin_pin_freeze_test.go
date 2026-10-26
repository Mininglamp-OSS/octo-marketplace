package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

// seedReleasedTarget creates an already-listed target with one formal release and
// points its published head at it.
func seedReleasedTarget(t *testing.T, database *sql.DB, id string, typ model.PluginType, visibility, versionID, seq, label string) {
	t.Helper()
	seed(t, database, seedPlugin{id: id, typ: string(typ), visibility: visibility, listingState: "published"})
	seedVersionRow(t, database, versionID, id, seq)
	if _, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,?,'user-1',NOW(3))`, id, versionID, label); err != nil {
		t.Fatalf("release %s: %v", versionID, err)
	}
	if _, err := database.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, versionID, id); err != nil {
		t.Fatalf("publish pointer %s: %v", id, err)
	}
}

// releaseNextVersion appends one more formal release and advances the target's
// published head, exactly like a later publish of that target.
func releaseNextVersion(t *testing.T, database *sql.DB, id, versionID, seq, label string) {
	t.Helper()
	seedVersionRow(t, database, versionID, id, seq)
	if _, err := database.Exec(`INSERT INTO plugin_version_releases(plugin_id,version_id,version_label,released_by,released_at) VALUES (?,?,?,'user-1',NOW(3))`, id, versionID, label); err != nil {
		t.Fatalf("release %s: %v", versionID, err)
	}
	if _, err := database.Exec(`UPDATE plugins SET published_version_id=? WHERE plugin_id=?`, versionID, id); err != nil {
		t.Fatalf("publish pointer %s: %v", id, err)
	}
}

// snapshotTargetPin returns the target_version_id of the one edge in a version
// snapshot to targetID.
func snapshotTargetPin(t *testing.T, database *sql.DB, versionID, targetID string) *string {
	t.Helper()
	var raw []byte
	if err := database.QueryRow(`SELECT relations_json FROM plugin_versions WHERE version_id=?`, versionID).Scan(&raw); err != nil {
		t.Fatalf("read snapshot %s: %v", versionID, err)
	}
	rels, err := model.DecodeSnapshotRelations(raw)
	if err != nil {
		t.Fatalf("decode snapshot %s: %v", versionID, err)
	}
	for _, rel := range rels {
		if rel.TargetPluginID == targetID {
			return rel.TargetVersionID
		}
	}
	t.Fatalf("snapshot %s has no edge to %s", versionID, targetID)
	return nil
}

// referenceIndexPin returns the pinned target version recorded in the immutable
// reverse index for a source snapshot.
func referenceIndexPin(t *testing.T, database *sql.DB, sourceVersionID, targetID string) sql.NullString {
	t.Helper()
	var pin sql.NullString
	if err := database.QueryRow(`SELECT target_version_id FROM plugin_version_relation_refs WHERE source_version_id=? AND target_plugin_id=?`, sourceVersionID, targetID).Scan(&pin); err != nil {
		t.Fatalf("read reference index for %s: %v", sourceVersionID, err)
	}
	return pin
}

func assertNoRelease(t *testing.T, database *sql.DB, pluginID string) {
	t.Helper()
	var releases int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE plugin_id=?`, pluginID).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if releases != 0 {
		t.Fatalf("failed publish left %d release rows", releases)
	}
	var published sql.NullString
	if err := database.QueryRow(`SELECT published_version_id FROM plugins WHERE plugin_id=?`, pluginID).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published.Valid {
		t.Fatalf("failed publish left published_version_id=%q", published.String)
	}
}

// The P model freezes an omitted Expert→Skill pin at publish time: the draft may
// carry NULL, but the released snapshot and its reverse index must pin the
// Skill's then-current formal release. A later Skill release must not move it.
func TestPublishFreezesOmittedSkillPinMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	owner := managedOwner()
	seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")

	req := managedWrite("1.0.0", "expert", model.PluginVisibilityPrivate)
	req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: "skill", Type: "expert_skill"}}
	draft, err := svc.Create(ctx, owner, req)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if draft.Plugin.CurrentVersionID == nil {
		t.Fatal("draft has no snapshot")
	}
	if pin := snapshotTargetPin(t, database, *draft.Plugin.CurrentVersionID, "skill"); pin != nil {
		t.Fatalf("draft snapshot was pinned early: %q", *pin)
	}

	pub, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: draft.Plugin.ID})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.Review != nil {
		t.Fatal("private publish opened a review request")
	}
	state, err := repo.GetVersionState(ctx, tenantScope(), draft.Plugin.ID)
	if err != nil || state.PublishedVersionID == nil {
		t.Fatalf("published state: %+v %v", state, err)
	}
	published := *state.PublishedVersionID
	if pin := snapshotTargetPin(t, database, published, "skill"); pin == nil || *pin != "skill-v1" {
		t.Fatalf("released snapshot pin = %v, want skill-v1", pin)
	}
	if pin := referenceIndexPin(t, database, published, "skill"); !pin.Valid || pin.String != "skill-v1" {
		t.Fatalf("released reference index pin = %+v, want skill-v1", pin)
	}

	// A newer formal Skill release must not move the Expert's frozen pin.
	releaseNextVersion(t, database, "skill", "skill-v2", "2", "2.0.0")
	g, err := repo.ResolvePinnedGraph(ctx, tenantScope(), draft.Plugin.ID)
	if err != nil {
		t.Fatalf("resolve pinned graph: %v", err)
	}
	if g.Selected["skill"] != "skill-v1" {
		t.Fatalf("pinned graph selected skill=%q, want skill-v1", g.Selected["skill"])
	}
	if g.Versions[pluginrepo.InstallVersionKey{PluginID: "skill", VersionID: "skill-v2"}] != nil {
		t.Fatal("later Skill release leaked into the pinned graph")
	}
}

// P-model decision: republish refreshes every Expert/Team pin to the target's
// then-current formal release, even if the draft carried an older explicit pin.
func TestPublishRefreshesExplicitPinMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	owner := managedOwner()
	seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")
	releaseNextVersion(t, database, "skill", "skill-v2", "2", "2.0.0")

	pin := "skill-v1"
	req := managedWrite("1.0.0", "expert", model.PluginVisibilityPrivate)
	req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: "skill", TargetVersionID: &pin, Type: "expert_skill"}}
	draft, err := svc.Create(ctx, owner, req)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: draft.Plugin.ID}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	state, err := repo.GetVersionState(ctx, tenantScope(), draft.Plugin.ID)
	if err != nil || state.PublishedVersionID == nil {
		t.Fatalf("published state: %+v %v", state, err)
	}
	if *state.PublishedVersionID == *draft.Plugin.CurrentVersionID {
		t.Fatal("republish should materialize a refreshed-pin snapshot")
	}
	if got := snapshotTargetPin(t, database, *state.PublishedVersionID, "skill"); got == nil || *got != "skill-v2" {
		t.Fatalf("explicit pin = %v, want refreshed skill-v2", got)
	}
	if got := referenceIndexPin(t, database, *state.PublishedVersionID, "skill"); !got.Valid || got.String != "skill-v2" {
		t.Fatalf("reference index pin = %+v, want skill-v2", got)
	}
}

// A Team pins its member Experts to the member's published version at Team
// publish. A later member release does not move the Team, and the pinned member
// snapshot keeps its own Skill pin.
func TestPublishFreezesOmittedTeamMemberPinMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	owner := managedOwner()
	seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")

	memberReq := managedWrite("1.0.0", "member", model.PluginVisibilityPrivate)
	memberReq.Relations = []pluginsvc.RelationRequest{{TargetPluginID: "skill", Type: "expert_skill"}}
	member, err := svc.Create(ctx, owner, memberReq)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	publishManaged(t, svc, owner, member.Plugin.ID)
	memberState, err := repo.GetVersionState(ctx, tenantScope(), member.Plugin.ID)
	if err != nil || memberState.PublishedVersionID == nil {
		t.Fatalf("member state: %+v %v", memberState, err)
	}
	memberV1 := *memberState.PublishedVersionID

	teamReq := managedWrite("1.0.0", "team", model.PluginVisibilityPrivate)
	teamReq.Type = model.PluginTypeExpertTeam
	var manifest map[string]any
	if err := json.Unmarshal(teamReq.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["plugin_type"] = "expert_team"
	teamReq.Manifest, _ = json.Marshal(manifest)
	teamReq.Relations = []pluginsvc.RelationRequest{{TargetPluginID: member.Plugin.ID, Type: "expert_team_expert", Data: json.RawMessage(`{"member_key":"lead","role":"leader","is_leader":true}`)}}
	team, err := svc.Create(ctx, owner, teamReq)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	if pin := snapshotTargetPin(t, database, *team.Plugin.CurrentVersionID, member.Plugin.ID); pin != nil {
		t.Fatalf("team draft snapshot was pinned early: %q", *pin)
	}
	if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: team.Plugin.ID}); err != nil {
		t.Fatalf("publish team: %v", err)
	}
	teamState, err := repo.GetVersionState(ctx, tenantScope(), team.Plugin.ID)
	if err != nil || teamState.PublishedVersionID == nil {
		t.Fatalf("team state: %+v %v", teamState, err)
	}
	teamVersion := *teamState.PublishedVersionID
	if pin := snapshotTargetPin(t, database, teamVersion, member.Plugin.ID); pin == nil || *pin != memberV1 {
		t.Fatalf("team member pin = %v, want %s", pin, memberV1)
	}

	// A newer member Expert release and a newer Skill release must not move the
	// Team's member pin, nor the pinned member's own Skill pin.
	releaseNextVersion(t, database, member.Plugin.ID, "member-v2", "2", "2.0.0")
	releaseNextVersion(t, database, "skill", "skill-v2", "2", "2.0.0")
	g, err := repo.ResolvePinnedGraph(ctx, tenantScope(), team.Plugin.ID)
	if err != nil {
		t.Fatalf("resolve pinned graph: %v", err)
	}
	if g.Selected[member.Plugin.ID] != memberV1 || g.Selected["skill"] != "skill-v1" {
		t.Fatalf("pinned selection drifted: %+v", g.Selected)
	}
	if pin := snapshotTargetPin(t, database, teamVersion, member.Plugin.ID); pin == nil || *pin != memberV1 {
		t.Fatalf("team pin moved after newer member release: %v", pin)
	}
}

// The tenant review boundary freezes the submitted graph: the review document
// itself carries the pin, and approval persists that same pin.
func TestSubmitReviewFreezesOmittedSkillPinMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	owner := managedOwner()
	if _, err := repo.UpsertReviewPolicy(ctx, tenantScope(), false, "reviewer", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")

	req := managedWrite("1.0.0", "expert", model.PluginVisibilitySpace)
	req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: "skill", Type: "expert_skill"}}
	draft, err := svc.Create(ctx, owner, req)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	pub, err := svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: draft.Plugin.ID})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.Review == nil {
		t.Fatal("space publish did not open a review")
	}
	var raw []byte
	if err = database.QueryRow(`SELECT relations_json FROM plugin_review_requests WHERE review_id=?`, pub.Review.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	rels, err := model.DecodeSnapshotRelations(raw)
	if err != nil || len(rels) != 1 || rels[0].TargetVersionID == nil || *rels[0].TargetVersionID != "skill-v1" {
		t.Fatalf("review snapshot pin = %+v err=%v", rels, err)
	}
	if _, err = svc.ApproveReview(ctx, managedReviewer(), pub.Review.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	state, err := repo.GetVersionState(ctx, tenantScope(), draft.Plugin.ID)
	if err != nil || state.PublishedVersionID == nil {
		t.Fatalf("published state: %+v %v", state, err)
	}
	if pin := snapshotTargetPin(t, database, *state.PublishedVersionID, "skill"); pin == nil || *pin != "skill-v1" {
		t.Fatalf("approved snapshot pin = %v, want skill-v1", pin)
	}
}

// The admin surface has no draft step: AdminCreate and AdminUpdate ARE
// publications, so an omitted pin must be frozen on both.
func TestAdminDirectWriteFreezesOmittedSkillPinMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database)
	svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
	admin := pluginsvc.Caller{UID: "market-admin", Name: "Admin", IsSystemAdmin: true}
	seedReleasedTarget(t, database, "skill-a", model.PluginTypeSkill, "system", "skill-a1", "1", "1.0.0")
	seedReleasedTarget(t, database, "skill-b", model.PluginTypeSkill, "system", "skill-b1", "1", "1.0.0")

	req := managedWrite("1.0.0", "admin expert", model.PluginVisibilitySystem)
	req.AllowContainerTypes = true
	req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: "skill-a", Type: "expert_skill"}}
	created, err := svc.AdminCreate(ctx, admin, req)
	if err != nil {
		t.Fatalf("admin create: %v", err)
	}
	adminScope := pluginrepo.Scope{CallerUID: admin.UID, Admin: true}
	state, err := repo.GetVersionState(ctx, adminScope, created.Plugin.ID)
	if err != nil || state.PublishedVersionID == nil {
		t.Fatalf("admin create state: %+v %v", state, err)
	}
	if pin := snapshotTargetPin(t, database, *state.PublishedVersionID, "skill-a"); pin == nil || *pin != "skill-a1" {
		t.Fatalf("admin create pin = %v, want skill-a1", pin)
	}
	if pin := referenceIndexPin(t, database, *state.PublishedVersionID, "skill-a"); !pin.Valid || pin.String != "skill-a1" {
		t.Fatalf("admin create reference index pin = %+v, want skill-a1", pin)
	}

	revision, err := repo.GetEditRevision(ctx, adminScope, created.Plugin.ID)
	if err != nil {
		t.Fatal(err)
	}
	upd := pluginsvc.WriteRequest{
		ExpectedEditRevision: &revision,
		AllowContainerTypes:  true,
		Name:                 created.Plugin.Name,
		Type:                 created.Plugin.Type,
		Visibility:           created.Plugin.Visibility,
		Version:              "2.0.0",
		Manifest:             created.Plugin.Manifest,
		Package:              created.Plugin.Package,
		Tags:                 created.Plugin.Tags,
		Relations:            []pluginsvc.RelationRequest{{TargetPluginID: "skill-b", Type: "expert_skill"}},
	}
	updated, err := svc.AdminUpdate(ctx, admin, created.Plugin.ID, upd)
	if err != nil {
		t.Fatalf("admin update: %v", err)
	}
	state, err = repo.GetVersionState(ctx, adminScope, updated.Plugin.ID)
	if err != nil || state.PublishedVersionID == nil {
		t.Fatalf("admin update state: %+v %v", state, err)
	}
	if pin := snapshotTargetPin(t, database, *state.PublishedVersionID, "skill-b"); pin == nil || *pin != "skill-b1" {
		t.Fatalf("admin update pin = %v, want skill-b1", pin)
	}
	if pin := referenceIndexPin(t, database, *state.PublishedVersionID, "skill-b"); !pin.Valid || pin.String != "skill-b1" {
		t.Fatalf("admin update reference index pin = %+v, want skill-b1", pin)
	}
}

// Publish fails closed when the omitted pin cannot be resolved to a currently
// listed, formally released target. No release row may be written.
func TestPublishFailsWhenOmittedPinCannotBeFrozenMySQL(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(t *testing.T, database *sql.DB)
		want   error
	}{
		{
			name: "target delisted",
			break_: func(t *testing.T, database *sql.DB) {
				t.Helper()
				if _, err := database.Exec(`UPDATE plugins SET listing_state='draft' WHERE plugin_id='skill'`); err != nil {
					t.Fatal(err)
				}
			},
			want: pluginrepo.ErrInvalidRelation,
		},
		{
			name: "target deleted",
			break_: func(t *testing.T, database *sql.DB) {
				t.Helper()
				if _, err := database.Exec(`UPDATE plugins SET deleted_at=NOW(3) WHERE plugin_id='skill'`); err != nil {
					t.Fatal(err)
				}
			},
			want: pluginrepo.ErrNotFound,
		},
		{
			name: "target unreleased",
			break_: func(t *testing.T, database *sql.DB) {
				t.Helper()
				if _, err := database.Exec(`UPDATE plugins SET published_version_id=NULL WHERE plugin_id='skill'`); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`DELETE FROM plugin_version_releases WHERE plugin_id='skill'`); err != nil {
					t.Fatal(err)
				}
			},
			want: pluginrepo.ErrInvalidRelation,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := reviewDB(t)
			ctx := context.Background()
			repo := pluginrepo.New(database)
			svc := pluginsvc.New(repo).WithVersionManagement(true).WithReferences(true)
			owner := managedOwner()
			seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")
			req := managedWrite("1.0.0", "expert", model.PluginVisibilityPrivate)
			req.Relations = []pluginsvc.RelationRequest{{TargetPluginID: "skill", Type: "expert_skill"}}
			draft, err := svc.Create(ctx, owner, req)
			if err != nil {
				t.Fatalf("create draft: %v", err)
			}
			tc.break_(t, database)
			_, err = repo.PublishManaged(ctx, tenantScope(), pluginrepo.PublishParams{PluginID: draft.Plugin.ID, ExpectedVersionID: *draft.Plugin.CurrentVersionID, OperatorID: owner.UID})
			if !errors.Is(err, tc.want) {
				t.Fatalf("publish error = %v, want %v", err, tc.want)
			}
			assertNoRelease(t, database, draft.Plugin.ID)
		})
	}
}

// Connector edges never carry a version. The shared freeze helper rejects one
// even before any target read, and the normal write path refuses it too.
func TestFreezeRejectsConnectorVersionMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	seed(t, database, seedPlugin{id: "connector", typ: "connector", visibility: "system", listingState: "published", currentVersion: "1.0.0"})
	seedConnectorEffectiveForTest(t, database, "connector")
	source := model.Plugin{ID: "expert", Name: "Expert", Type: model.PluginTypeExpert, Visibility: model.PluginVisibilitySystem, ListingState: model.PluginListingStatePublished}
	pin := "connector-v1"
	edge := model.PluginRelation{ID: "connector-edge", SourcePluginID: "expert", TargetPluginID: "connector", TargetPluginType: model.PluginTypeConnector, Type: "expert_connector", TargetVersionID: &pin, Status: 1}

	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = pluginrepo.FreezeRelationPins(ctx, tx, source, []model.PluginRelation{edge}); !errors.Is(err, pluginrepo.ErrInvalidRelation) {
		tx.Rollback()
		t.Fatalf("freeze accepted a connector version: %v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	version := "1.0.0"
	p := model.Plugin{ID: "expert-2", Name: "Expert", Type: model.PluginTypeExpert, Visibility: model.PluginVisibilitySystem, ListingState: model.PluginListingStatePublished, CurrentVersion: &version, Tags: json.RawMessage(`[]`), Manifest: json.RawMessage(`{"plugin_name":"Expert"}`), Package: json.RawMessage(`{"attachments":[]}`), Status: 1}
	edge.SourcePluginID = p.ID
	_, err = repo.Create(ctx, pluginrepo.Scope{Admin: true}, pluginrepo.Mutation{Plugin: p, Relations: []model.PluginRelation{edge}, SnapshotVersion: true, OperatorID: "admin"})
	if !errors.Is(err, pluginrepo.ErrInvalidRelation) {
		t.Fatalf("create accepted a connector version: %v", err)
	}
	var count int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugins WHERE plugin_id=?`, p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed create left a plugin row: %d %v", count, err)
	}
}
