package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/backfill/stockmigration"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/migrationauth"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestOfflineMigrationPrivateRunCompletesAndResumesSameRun(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedStockRoot(t, database, "offline-private", "private", "published")
	d, plan, digest := freezeStockPlan(t, database, "offline-private")
	auth := stockTestAuthorization("auth-private", plan, digest)
	run, created, err := stockmigration.OpenRun(ctx, database, stockmigration.OpenRunOptions{
		Plan: plan, PlanDigest: digest, Environment: "test-env", Authorization: auth, Signature: "signature",
	})
	if err != nil || !created {
		t.Fatalf("open run created=%v err=%v", created, err)
	}
	status, reason, err := stockmigration.ExecuteRun(ctx, database, run, d, stockmigration.ExecuteOptions{ActorID: "tester", ActorName: "tester"})
	if err != nil || status != stockmigration.RunCompleted {
		t.Fatalf("execute status=%s reason=%s err=%v", status, reason, err)
	}
	loaded, err := stockmigration.LoadRun(ctx, database, run.RunID)
	if err != nil || loaded.Status != stockmigration.RunCompleted {
		t.Fatalf("loaded run: %+v err=%v", loaded, err)
	}
	// The old direct-release writer is gone: every release is attributable to the
	// managed publish/review actor, never to the retired stock-migration writer.
	var direct, managed int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE released_by='stock-migration'`).Scan(&direct); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases WHERE released_by='tester'`).Scan(&managed); err != nil {
		t.Fatal(err)
	}
	if direct != 0 || managed == 0 {
		t.Fatalf("direct=%d managed=%d", direct, managed)
	}
	// The retired pointer rollback cannot have run, and the batch/root audit base
	// is populated by the frozen-plan execution.
	var rolledBack, roots int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_stock_migration_batches WHERE state='rolled_back'`).Scan(&rolledBack); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_stock_migration_roots WHERE batch_id=?`, auth.BatchID).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if rolledBack != 0 || roots != 1 {
		t.Fatalf("rolledBack=%d roots=%d", rolledBack, roots)
	}
	var current, published string
	if err = database.QueryRow(`SELECT current_version_id,published_version_id FROM plugins WHERE plugin_id='offline-private'`).Scan(&current, &published); err != nil {
		t.Fatal(err)
	}
	if current == "offline-private-old" || published != current {
		t.Fatalf("root pointers were not moved forward: current=%s published=%s", current, published)
	}
	// Same frozen plan and authorization identity return the same run; a
	// completed run is terminal and is not re-executed.
	again, created, err := stockmigration.OpenRun(ctx, database, stockmigration.OpenRunOptions{
		Plan: plan, PlanDigest: digest, Environment: "test-env", Authorization: auth, Signature: "signature",
	})
	if err != nil || created || again.RunID != run.RunID {
		t.Fatalf("resume arbitration created=%v run=%s want=%s err=%v", created, again.RunID, run.RunID, err)
	}
	status, _, err = stockmigration.ExecuteRun(ctx, database, again, d, stockmigration.ExecuteOptions{ActorID: "tester", ActorName: "tester"})
	if err != nil || status != stockmigration.RunCompleted {
		t.Fatalf("terminal replay status=%s err=%v", status, err)
	}
	// A source edit after freeze cannot be applied under the old approval.
	if _, err = database.Exec(`UPDATE plugins SET plugin_name='independently edited' WHERE plugin_id='offline-private'`); err != nil {
		t.Fatal(err)
	}
	stale, err := stockmigration.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = stockmigration.Freeze(stale, plan); err == nil {
		t.Fatal("edited source accepted under the old frozen plan")
	}
}

func TestOfflineMigrationSpaceManualReviewWaitsAndResumes(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedStockRootWithMCP(t, database, "offline-space", "space", "published", false)
	if _, err := database.Exec(`INSERT INTO plugin_review_policies(space_id,is_auto_approve_enabled,updated_by,updated_by_name,created_at,updated_at) VALUES ('space-a',0,'admin','Admin',NOW(3),NOW(3))`); err != nil {
		t.Fatal(err)
	}
	d, plan, digest := freezeStockPlan(t, database, "offline-space")
	auth := stockTestAuthorization("auth-space", plan, digest)
	run, _, err := stockmigration.OpenRun(ctx, database, stockmigration.OpenRunOptions{
		Plan: plan, PlanDigest: digest, Environment: "test-env", Authorization: auth, Signature: "signature",
	})
	if err != nil {
		t.Fatal(err)
	}
	status, reason, err := stockmigration.ExecuteRun(ctx, database, run, d, stockmigration.ExecuteOptions{ActorID: "tester", ActorName: "tester"})
	if err != nil || status != stockmigration.RunWaitingReview {
		t.Fatalf("first status=%s reason=%s err=%v", status, reason, err)
	}
	repo := pluginrepo.New(database).WithVersionManagement(true).WithReferences(true)
	scope := pluginrepo.Scope{CallerUID: "user-1", SpaceID: "space-a"}
	for i := 0; i < 20; i++ {
		rows, err := stockmigration.ListNode(ctx, database, run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		waiting := ""
		for _, row := range rows {
			if row.StepState == stockmigration.StepWaitingReview && row.ReviewID != nil {
				waiting = *row.ReviewID
				break
			}
		}
		if waiting == "" {
			break
		}
		if _, err = repo.ApproveReview(ctx, scope, pluginrepo.ApproveReviewParams{
			ReviewID: waiting, ReviewerUID: "reviewer", ReviewerName: "reviewer",
			DecisionSource: model.ReviewDecisionSourceWeb, Now: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("approve %s: %v", waiting, err)
		}
		status, _, err = stockmigration.ExecuteRun(ctx, database, run, d, stockmigration.ExecuteOptions{ActorID: "tester", ActorName: "tester"})
		if err != nil {
			t.Fatalf("resume after approve: %v", err)
		}
		if status == stockmigration.RunCompleted {
			break
		}
	}
	loaded, err := stockmigration.LoadRun(ctx, database, run.RunID)
	if err != nil || loaded.Status != stockmigration.RunCompleted {
		t.Fatalf("run: %+v err=%v", loaded, err)
	}
	var reviews int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_review_requests WHERE plugin_id LIKE 'offline-space%' AND status='approved'`).Scan(&reviews); err != nil || reviews == 0 {
		t.Fatalf("approved reviews=%d err=%v", reviews, err)
	}
}

func TestOfflineMigrationAuthorizationCannotCreateSecondRun(t *testing.T) {
	database := reviewDB(t)
	ctx := context.Background()
	seedStockRoot(t, database, "offline-auth", "private", "published")
	_, plan, digest := freezeStockPlan(t, database, "offline-auth")
	first := stockTestAuthorization("auth-one", plan, digest)
	run, _, err := stockmigration.OpenRun(ctx, database, stockmigration.OpenRunOptions{
		Plan: plan, PlanDigest: digest, Environment: "test-env", Authorization: first, Signature: "signature-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	second := stockTestAuthorization("auth-two", plan, digest)
	second.BatchID = first.BatchID
	resumed, created, err := stockmigration.OpenRun(ctx, database, stockmigration.OpenRunOptions{
		Plan: plan, PlanDigest: digest, Environment: "test-env", Authorization: second, Signature: "signature-two",
	})
	if err != nil || created || resumed.RunID != run.RunID {
		t.Fatalf("renewal created=%v run=%s want=%s err=%v", created, resumed.RunID, run.RunID, err)
	}
	var events int
	if err = database.QueryRow(`SELECT COUNT(*) FROM plugin_migration_authorization_audit WHERE run_id=? AND event='renewed'`, run.RunID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("renewal audit events=%d err=%v", events, err)
	}
	// Authorization fields are bound to the frozen plan before a run is created.
	wrong := stockTestAuthorization("auth-wrong", plan, digest)
	wrong.PlanDigest = strings.Repeat("c", 64)
	if _, _, err = stockmigration.OpenRun(ctx, database, stockmigration.OpenRunOptions{
		Plan: plan, PlanDigest: digest, Environment: "test-env", Authorization: wrong, Signature: "signature-wrong",
	}); !errors.Is(err, stockmigration.ErrRunAuthorization) {
		t.Fatalf("wrong authorization accepted: %v", err)
	}
}

func freezeStockPlan(t *testing.T, database *sql.DB, root string) (stockmigration.Dataset, stockmigration.MigrationPlan, string) {
	t.Helper()
	ctx := context.Background()
	d, err := stockmigration.Read(ctx, database, 1000)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := stockmigration.PreviewDiscovery(d, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Exceptions) != 0 || preview.ProposedPlan == nil {
		t.Fatalf("preview exceptions: %+v", preview.Exceptions)
	}
	plan, digest, err := stockmigration.Freeze(d, *preview.ProposedPlan)
	if err != nil {
		t.Fatal(err)
	}
	return d, plan, digest
}

func stockTestAuthorization(id string, plan stockmigration.MigrationPlan, digest string) migrationauth.Payload {
	now := time.Now().UTC()
	return migrationauth.Payload{
		SchemaVersion: migrationauth.SchemaVersion, AuthorizationID: id, KeyID: "key-1",
		IssuerID: "issuer-1", ActorID: "tester", EnvironmentID: "test-env",
		SourceScope: plan.SourceScope, SourceIdentity: plan.SourceIdentity, PlanDigest: digest,
		BatchID: "batch-" + id, IssuedAt: now.Add(-time.Minute).Format(time.RFC3339),
		ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
	}
}

func applyOfflineMigrationForTest(t *testing.T, database *sql.DB, root, batch string) {
	t.Helper()
	ctx := context.Background()
	d, plan, digest := freezeStockPlan(t, database, root)
	authorization := stockTestAuthorization("auth-"+batch, plan, digest)
	authorization.BatchID = batch
	run, _, err := stockmigration.OpenRun(ctx, database, stockmigration.OpenRunOptions{
		Plan: plan, PlanDigest: digest, Environment: "test-env", Authorization: authorization, Signature: "signature",
	})
	if err != nil {
		t.Fatal(err)
	}
	status, reason, err := stockmigration.ExecuteRun(ctx, database, run, d, stockmigration.ExecuteOptions{ActorID: "tester", ActorName: "tester"})
	if err != nil || status != stockmigration.RunCompleted {
		t.Fatalf("offline migration status=%s reason=%s err=%v", status, reason, err)
	}
}

func seedStockRoot(t *testing.T, database *sql.DB, id, visibility, listing string) {
	seedStockRootWithMCP(t, database, id, visibility, listing, true)
}

func seedStockRootWithMCP(t *testing.T, database *sql.DB, id, visibility, listing string, includeMCP bool) {
	t.Helper()
	manifest := func(kind, name string) string {
		b, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-manifest-2.0.json", "plugin_name": name, "plugin_type": kind, "name": name, "description": "fixture", "labels": []any{}, "examples": []any{}})
		return string(b)
	}
	pkg := func(files map[string]string) string {
		atts := []any{}
		for path, content := range files {
			atts = append(atts, map[string]any{"path": path, "content_type": "raw", "mime_type": "text/plain", "raw_content": content})
		}
		b, _ := json.Marshal(map[string]any{"$schema": "cowork-plugin-package-2.0.json", "attachments": atts})
		return string(b)
	}
	seed(t, database, seedPlugin{id: id, typ: "expert_team", visibility: visibility, listingState: listing, currentVersion: "1.2.3", currentVersionID: id + "-old", manifest: manifest("expert_team", id), pkg: pkg(map[string]string{"AGENTS.md": "team instructions"})})
	expertFiles := map[string]string{"AGENTS.md": "member instructions", "expert/context.json": `{"role":"researcher"}`}
	if includeMCP {
		expertFiles["mcp.json"] = `{"mcpServers":{"search":{"command":"search"}}}`
	}
	seed(t, database, seedPlugin{id: id + "-expert", typ: "expert", visibility: "private", listingState: listing, embedded: true, manifest: manifest("expert", id+"-expert"), pkg: pkg(expertFiles)})
	seed(t, database, seedPlugin{id: id + "-skill", typ: "skill", visibility: "private", listingState: listing, embedded: true, manifest: manifest("skill", id+"-skill"), pkg: pkg(map[string]string{"SKILL.md": "---\nname: skill\ndescription: fixture\n---\nbody"})})
	for _, pid := range []string{id, id + "-expert", id + "-skill"} {
		if _, err := database.Exec(`UPDATE plugins SET plugin_name=? WHERE plugin_id=?`, pid, pid); err != nil {
			t.Fatal(err)
		}
	}
	for _, pid := range []string{id, id + "-expert", id + "-skill"} {
		var name, kind, manifest, pkg, tags string
		if err := database.QueryRow(`SELECT plugin_name,plugin_type,manifest_json,plugin_json,tags_json FROM plugins WHERE plugin_id=?`, pid).Scan(&name, &kind, &manifest, &pkg, &tags); err != nil {
			t.Fatal(err)
		}
		docs, err := pluginsvc.CanonicalizeMigratedDocuments(name, model.PluginType(kind), json.RawMessage(tags), json.RawMessage(manifest), json.RawMessage(pkg), "space-a")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(`UPDATE plugins SET manifest_hash=?,plugin_hash=? WHERE plugin_id=?`, docs.ManifestHash, docs.PluginHash, pid); err != nil {
			t.Fatal(err)
		}
	}
	seedVersionRow(t, database, id+"-old", id, "1")
	if _, err := database.Exec(`UPDATE plugin_versions v JOIN plugins p ON p.plugin_id=v.plugin_id SET v.manifest_hash=p.manifest_hash,v.plugin_hash=p.plugin_hash WHERE v.version_id=?`, id+"-old"); err != nil {
		t.Fatal(err)
	}
	seedRelation(t, database, id+"-member-edge", id, id+"-expert", "expert_team_expert")
	if _, err := database.Exec(`UPDATE plugin_relations SET relation_json=JSON_OBJECT('source_index',7,'member_key','m','role','researcher','is_leader',true),sort_order=7 WHERE relation_id=?`, id+"-member-edge"); err != nil {
		t.Fatal(err)
	}
	seedRelation(t, database, id+"-skill-edge", id+"-expert", id+"-skill", "expert_skill")
}
