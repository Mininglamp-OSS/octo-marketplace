package db

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/pinbackfill"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migrationauth"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

type pinBackfillRef struct {
	RelationID   string
	RelationType string
	TargetID     string
	Pin          *string
}

func pinBackfillFixtureTime() time.Time {
	return time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)
}

func pinBackfillTrust(t *testing.T, scope string) (*migrationauth.TrustConfig, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := migrationauth.TrustConfig{
		SchemaVersion: migrationauth.SchemaVersion,
		EnvironmentID: "test-env",
		ActorID:       "pin-operator",
		Keys: []migrationauth.TrustKey{{
			KeyID: "pin-key", IssuerID: "pin-issuer",
			PublicKey: base64.RawURLEncoding.EncodeToString(public), Enabled: true,
		}},
		Issuers: []migrationauth.TrustIssuer{{
			IssuerID: "pin-issuer", AllowedSourceScopes: []string{scope}, Enabled: true,
		}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := migrationauth.ParseTrustConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	return trust, private
}

func pinBackfillEnvelope(t *testing.T, plan pinbackfill.Plan, digest, authID string, private ed25519.PrivateKey, now time.Time) migrationauth.Envelope {
	t.Helper()
	payload := migrationauth.Payload{
		SchemaVersion: migrationauth.SchemaVersion, AuthorizationID: authID,
		KeyID: "pin-key", IssuerID: "pin-issuer", ActorID: "pin-operator", EnvironmentID: "test-env",
		SourceScope: plan.SourceScope, SourceIdentity: plan.SourceIdentity, PlanDigest: digest,
		BatchID: plan.BatchID, IssuedAt: now.Add(-time.Minute).Format(time.RFC3339),
		ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
	}
	envelope, err := migrationauth.Sign(payload, private)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func pinBackfillApplyPlan(t *testing.T, database *sql.DB, plan pinbackfill.Plan, digest string, trust *migrationauth.TrustConfig, envelope migrationauth.Envelope, now time.Time) (pinbackfill.Result, error) {
	t.Helper()
	return pinbackfill.Apply(context.Background(), database, pinbackfill.ApplyOptions{
		Plan: plan, PlanDigest: digest, Trust: trust, Authorization: envelope, Now: now,
	})
}

// seedPinBackfillSource writes one released source snapshot plus its matching
// reverse-reference rows. The caller invokes markReferenceIndexFixtures once all
// fixtures exist so the coverage marker mirrors the seeded facts.
func seedPinBackfillSource(t *testing.T, database *sql.DB, id string, typ model.PluginType, versionID, label string, edges []model.PluginRelation) {
	t.Helper()
	seed(t, database, seedPlugin{id: id, typ: string(typ), visibility: "private", listingState: "published", currentVersion: label})
	releasePinnedVersion(t, database, id, versionID, label, edges)
	for _, edge := range edges {
		ref := pinBackfillRef{RelationID: edge.ID, RelationType: edge.Type, TargetID: edge.TargetPluginID, Pin: edge.TargetVersionID}
		var pin any
		if ref.Pin != nil {
			pin = *ref.Pin
		}
		if _, err := database.Exec(`INSERT INTO plugin_version_relation_refs
			(source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id)
			VALUES (?,?,?,?,?,?)`, id, versionID, ref.RelationID, ref.RelationType, ref.TargetID, pin); err != nil {
			t.Fatalf("seed reference %s: %v", ref.RelationID, err)
		}
	}
}

func pinBackfillAuditRows(t *testing.T, database *sql.DB, digest string) int {
	t.Helper()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_pin_backfill_runs WHERE plan_digest=?`, digest).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func pinBackfillIndexStateMatches(t *testing.T, database *sql.DB, sourceVersionID string) bool {
	t.Helper()
	var digestOK, countOK bool
	err := database.QueryRow(`SELECT idx.snapshot_digest=SHA2(CAST(v.relations_json AS CHAR CHARACTER SET utf8mb4),256),
		idx.relation_count=(SELECT COUNT(*) FROM plugin_version_relation_refs r WHERE r.source_version_id=v.version_id)
		FROM plugin_versions v JOIN plugin_version_reference_index_state idx
		  ON idx.source_plugin_id=v.plugin_id AND idx.source_version_id=v.version_id
		WHERE v.version_id=?`, sourceVersionID).Scan(&digestOK, &countOK)
	if err != nil {
		t.Fatalf("read index state %s: %v", sourceVersionID, err)
	}
	return digestOK && countOK
}

func TestPinBackfillPreviewFindsUnpinnedReleasedSourcesMySQL(t *testing.T) {
	database := reviewDB(t)
	seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")
	seedReleasedTarget(t, database, "member", model.PluginTypeExpert, "private", "member-v1", "1", "1.0.0")
	seed(t, database, seedPlugin{id: "connector", typ: "connector", visibility: "system", listingState: "published", currentVersion: "1.0.0"})

	seedPinBackfillSource(t, database, "expert", model.PluginTypeExpert, "expert-v1", "1.0.0", []model.PluginRelation{
		pinnedTestEdge("expert", "skill", "expert_skill", ""),
		pinnedTestEdge("expert", "connector", "expert_connector", ""),
	})
	seedPinBackfillSource(t, database, "team", model.PluginTypeExpertTeam, "team-v1", "1.0.0", []model.PluginRelation{
		pinnedTestEdge("team", "member", "expert_team_expert", ""),
	})
	seedPinBackfillSource(t, database, "expert-valid", model.PluginTypeExpert, "expert-valid-v1", "1.0.0", []model.PluginRelation{
		pinnedTestEdge("expert-valid", "skill", "expert_skill", "skill-v1"),
	})
	seedPinBackfillSource(t, database, "expert-blocked", model.PluginTypeExpert, "expert-blocked-v1", "1.0.0", []model.PluginRelation{
		pinnedTestEdge("expert-blocked", "missing-target", "expert_skill", ""),
	})
	// A valid snapshot pin with no reverse-index row must stay out of this plan:
	// missing index coverage is the reference-index backfill's concern, and the
	// pin itself must never move.
	if _, err := database.Exec(`DELETE FROM plugin_version_relation_refs WHERE source_version_id='expert-valid-v1'`); err != nil {
		t.Fatal(err)
	}
	markReferenceIndexFixtures(t, database)
	// A newer skill release is the current head. The unpinned source must propose
	// it; the already-pinned source must remain untouched.
	releaseNextVersion(t, database, "skill", "skill-v2", "2", "2.0.0")

	plan, err := pinbackfill.Read(context.Background(), database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != pinbackfill.PlanSchemaVersion || plan.SourceScope != pinbackfill.PlanSourceScope ||
		plan.SourceIdentity != pinbackfill.PlanSourceIdentity || plan.BatchID != pinbackfill.PlanBatchID {
		t.Fatalf("unexpected plan header: %+v", plan)
	}
	if len(plan.Sources) != 2 {
		t.Fatalf("sources=%+v, want expert and team", plan.Sources)
	}
	if len(plan.Blocked) != 1 || plan.Blocked[0].PluginID != "expert-blocked" {
		t.Fatalf("blocked=%+v", plan.Blocked)
	}
	blockedReason := plan.Blocked[0].Reasons
	if len(blockedReason) != 1 || blockedReason[0].Code != "target_not_found" ||
		blockedReason[0].RelationID != "expert-blocked-missing-target" {
		t.Fatalf("blocked reasons=%+v", blockedReason)
	}
	bySource := map[string]pinbackfill.SourcePlan{}
	for _, source := range plan.Sources {
		bySource[source.PluginID] = source
	}
	expert, ok := bySource["expert"]
	if !ok {
		t.Fatal("unpinned expert was not planned")
	}
	if len(expert.Relations) != 1 {
		t.Fatalf("connector edge entered the pin plan or skill was lost: %+v", expert.Relations)
	}
	if expert.Relations[0].TargetPluginID != "skill" || expert.Relations[0].TargetVersionID != "skill-v2" ||
		expert.Relations[0].Resolution != pinbackfill.ResolutionCurrentRelease {
		t.Fatalf("expert plan=%+v, want current skill-v2", expert.Relations)
	}
	team, ok := bySource["team"]
	if !ok || len(team.Relations) != 1 || team.Relations[0].TargetVersionID != "member-v1" {
		t.Fatalf("team plan=%+v", team)
	}
	if _, ok := bySource["expert-valid"]; ok {
		t.Fatal("a valid existing pin was proposed for change")
	}
	// The digest is deterministic for the same read.
	again, err := pinbackfill.Read(context.Background(), database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, _ := pinbackfill.Digest(plan)
	secondDigest, _ := pinbackfill.Digest(again)
	if firstDigest != secondDigest {
		t.Fatalf("deterministic preview drifted: %s != %s", firstDigest, secondDigest)
	}
}

func TestPinBackfillApplyPinsSnapshotAndReferenceIndexMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")
	seedPinBackfillSource(t, database, "expert", model.PluginTypeExpert, "expert-v1", "1.0.0", []model.PluginRelation{
		pinnedTestEdge("expert", "skill", "expert_skill", ""),
	})
	seedPinBackfillSource(t, database, "expert-valid", model.PluginTypeExpert, "expert-valid-v1", "1.0.0", []model.PluginRelation{
		pinnedTestEdge("expert-valid", "skill", "expert_skill", "skill-v1"),
	})
	markReferenceIndexFixtures(t, database)

	plan, err := pinbackfill.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := pinbackfill.Digest(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sources) != 1 || plan.Sources[0].PluginID != "expert" {
		t.Fatalf("unexpected plan sources: %+v", plan.Sources)
	}
	now := pinBackfillFixtureTime()
	trust, private := pinBackfillTrust(t, plan.SourceScope)
	envelope := pinBackfillEnvelope(t, plan, digest, "auth-pin-1", private, now)
	result, err := pinBackfillApplyPlan(t, database, plan, digest, trust, envelope, now)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Status != "applied" || result.ActorID != "pin-operator" || len(result.Sources) != 1 ||
		result.Sources[0].Status != "applied" || result.Sources[0].InsertedRefs != 0 {
		t.Fatalf("apply result=%+v", result)
	}
	if pin := snapshotTargetPin(t, database, "expert-v1", "skill"); pin == nil || *pin != "skill-v1" {
		t.Fatalf("snapshot pin=%v", pin)
	}
	if pin := referenceIndexPin(t, database, "expert-v1", "skill"); !pin.Valid || pin.String != "skill-v1" {
		t.Fatalf("reference pin=%+v", pin)
	}
	if !pinBackfillIndexStateMatches(t, database, "expert-v1") {
		t.Fatal("reference index state was not refreshed")
	}
	// The cutover mutates only pin evidence: no version was minted and no
	// published pointer moved.
	var versions int
	var published string
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_versions WHERE plugin_id='expert'`).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("version count=%d err=%v", versions, err)
	}
	if err = database.QueryRow(`SELECT published_version_id FROM plugins WHERE plugin_id='expert'`).Scan(&published); err != nil || published != "expert-v1" {
		t.Fatalf("published pointer=%q err=%v", published, err)
	}
	// The already-valid source is not part of the plan and keeps its pin.
	if pin := snapshotTargetPin(t, database, "expert-valid-v1", "skill"); pin == nil || *pin != "skill-v1" {
		t.Fatalf("valid source pin changed: %v", pin)
	}
	if pin := referenceIndexPin(t, database, "expert-valid-v1", "skill"); !pin.Valid || pin.String != "skill-v1" {
		t.Fatalf("valid source reference pin changed: %+v", pin)
	}

	repo := pluginrepo.New(database)
	graph, err := repo.ResolvePinnedGraph(ctx, tenantScope(), "expert")
	if err != nil {
		t.Fatalf("resolve after backfill: %v", err)
	}
	if graph.Selected["skill"] != "skill-v1" ||
		graph.Versions[pluginrepo.InstallVersionKey{PluginID: "skill", VersionID: "skill-v1"}] == nil {
		t.Fatalf("pinned graph did not read the backfilled pin: %+v", graph.Selected)
	}
	// A later target release must not move the newly backfilled pin.
	releaseNextVersion(t, database, "skill", "skill-v2", "2", "2.0.0")
	graph, err = repo.ResolvePinnedGraph(ctx, tenantScope(), "expert")
	if err != nil {
		t.Fatalf("resolve after newer target release: %v", err)
	}
	if graph.Selected["skill"] != "skill-v1" ||
		graph.Versions[pluginrepo.InstallVersionKey{PluginID: "skill", VersionID: "skill-v2"}] != nil {
		t.Fatalf("newer target release leaked into the pinned graph: %+v", graph.Selected)
	}
	if pin := snapshotTargetPin(t, database, "expert-v1", "skill"); pin == nil || *pin != "skill-v1" {
		t.Fatalf("pin moved after target release: %v", pin)
	}

	// Replaying the exact signed plan is a no-op and appends no second audit row.
	replay, err := pinBackfillApplyPlan(t, database, plan, digest, trust, envelope, now)
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if replay.Status != "noop" || len(replay.Sources) != 1 || replay.Sources[0].Status != "noop" {
		t.Fatalf("replay result=%+v", replay)
	}
	if rows := pinBackfillAuditRows(t, database, digest); rows != 1 {
		t.Fatalf("audit rows after replay=%d, want 1", rows)
	}

	// A tampered plan digest is plan_stale, never an authorization success.
	if _, err = pinBackfillApplyPlan(t, database, plan, strings.Repeat("0", 64), trust, envelope, now); !errors.Is(err, pinbackfill.ErrPlanStale) {
		t.Fatalf("tampered digest error=%v", err)
	}
	// A valid signature over a different plan digest does not satisfy Expected.
	wrong := pinBackfillEnvelope(t, plan, strings.Repeat("1", 64), "auth-pin-wrong", private, now)
	if _, err = pinBackfillApplyPlan(t, database, plan, digest, trust, wrong, now); !errors.Is(err, pinbackfill.ErrAuthorization) {
		t.Fatalf("wrong authorization binding error=%v", err)
	}
	// A mutated payload invalidates the Ed25519 signature.
	tampered := envelope
	tampered.Payload.SourceIdentity = "other"
	if _, err = pinBackfillApplyPlan(t, database, plan, digest, trust, tampered, now); !errors.Is(err, pinbackfill.ErrAuthorization) {
		t.Fatalf("tampered authorization error=%v", err)
	}
}

func TestPinBackfillRejectsStaleSourceMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")
	seedPinBackfillSource(t, database, "expert", model.PluginTypeExpert, "expert-v1", "1.0.0", []model.PluginRelation{
		pinnedTestEdge("expert", "skill", "expert_skill", ""),
	})
	markReferenceIndexFixtures(t, database)
	plan, err := pinbackfill.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := pinbackfill.Digest(plan)
	now := pinBackfillFixtureTime()
	trust, private := pinBackfillTrust(t, plan.SourceScope)
	envelope := pinBackfillEnvelope(t, plan, digest, "auth-stale-source", private, now)
	// Publishing a newer source version moves the plugin's published pointer.
	releaseNextVersion(t, database, "expert", "expert-v2", "2", "2.0.0")
	if _, err = pinBackfillApplyPlan(t, database, plan, digest, trust, envelope, now); !errors.Is(err, pinbackfill.ErrPlanStale) {
		t.Fatalf("stale source error=%v", err)
	}
	if pin := referenceIndexPin(t, database, "expert-v1", "skill"); pin.Valid {
		t.Fatalf("stale source wrote a reference pin: %+v", pin)
	}
	if rows := pinBackfillAuditRows(t, database, digest); rows != 0 {
		t.Fatalf("stale source wrote %d audit rows", rows)
	}
}

func TestPinBackfillRejectsStaleTargetHeadMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")
	seedPinBackfillSource(t, database, "expert", model.PluginTypeExpert, "expert-v1", "1.0.0", []model.PluginRelation{
		pinnedTestEdge("expert", "skill", "expert_skill", ""),
	})
	markReferenceIndexFixtures(t, database)
	plan, err := pinbackfill.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := pinbackfill.Digest(plan)
	now := pinBackfillFixtureTime()
	trust, private := pinBackfillTrust(t, plan.SourceScope)
	envelope := pinBackfillEnvelope(t, plan, digest, "auth-stale-target", private, now)
	releaseNextVersion(t, database, "skill", "skill-v2", "2", "2.0.0")
	if _, err = pinBackfillApplyPlan(t, database, plan, digest, trust, envelope, now); !errors.Is(err, pinbackfill.ErrPlanStale) {
		t.Fatalf("stale target error=%v", err)
	}
	if pin := referenceIndexPin(t, database, "expert-v1", "skill"); pin.Valid {
		t.Fatalf("stale target wrote a reference pin: %+v", pin)
	}
	if rows := pinBackfillAuditRows(t, database, digest); rows != 0 {
		t.Fatalf("stale target wrote %d audit rows", rows)
	}
}

func TestPinBackfillTeamGraphUsesBackfilledMemberAndSkillPinsMySQL(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedReleasedTarget(t, database, "skill", model.PluginTypeSkill, "system", "skill-v1", "1", "1.0.0")
	seedPinBackfillSource(t, database, "member", model.PluginTypeExpert, "member-v1", "1.0.0", []model.PluginRelation{
		pinnedTestEdge("member", "skill", "expert_skill", ""),
	})
	seedPinBackfillSource(t, database, "team", model.PluginTypeExpertTeam, "team-v1", "1.0.0", []model.PluginRelation{
		pinnedTestEdge("team", "member", "expert_team_expert", ""),
	})
	markReferenceIndexFixtures(t, database)
	plan, err := pinbackfill.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sources) != 2 {
		t.Fatalf("team/member plan sources=%+v", plan.Sources)
	}
	digest, _ := pinbackfill.Digest(plan)
	now := pinBackfillFixtureTime()
	trust, private := pinBackfillTrust(t, plan.SourceScope)
	envelope := pinBackfillEnvelope(t, plan, digest, "auth-team", private, now)
	result, err := pinBackfillApplyPlan(t, database, plan, digest, trust, envelope, now)
	if err != nil {
		t.Fatalf("apply team plan: %v", err)
	}
	if result.Status != "applied" || len(result.Sources) != 2 {
		t.Fatalf("team apply result=%+v", result)
	}
	repo := pluginrepo.New(database)
	graph, err := repo.ResolvePinnedGraph(ctx, tenantScope(), "team")
	if err != nil {
		t.Fatalf("resolve team graph: %v", err)
	}
	if graph.Selected["team"] != "team-v1" || graph.Selected["member"] != "member-v1" ||
		graph.Selected["skill"] != "skill-v1" {
		t.Fatalf("team pins=%+v", graph.Selected)
	}
	// A newer member and skill release do not move either backfilled pin.
	releaseNextVersion(t, database, "member", "member-v2", "2", "2.0.0")
	releaseNextVersion(t, database, "skill", "skill-v2", "2", "2.0.0")
	graph, err = repo.ResolvePinnedGraph(ctx, tenantScope(), "team")
	if err != nil {
		t.Fatalf("resolve team graph after newer releases: %v", err)
	}
	if graph.Selected["member"] != "member-v1" || graph.Selected["skill"] != "skill-v1" {
		t.Fatalf("team pins moved: %+v", graph.Selected)
	}
}
