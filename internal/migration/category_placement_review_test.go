package migration

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestTransferMigrationPlacementsSplitUsesTargetCategoryRegistration(t *testing.T) {
	for _, existing := range []bool{true, false} {
		name := "missing connector registration uses incoming placement"
		if existing {
			name = "existing connector registration preserves metadata"
		}
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()

			category := "category"
			created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
			updated := created.Add(time.Hour)
			placementQuery := regexp.QuoteMeta(`SELECT placement_code,category_id,visible,sort_order,created_at,updated_at FROM plugin_placements WHERE plugin_id=? ORDER BY placement_code FOR UPDATE`)
			columns := []string{"placement_code", "category_id", "visible", "sort_order", "created_at", "updated_at"}
			mock.ExpectQuery(placementQuery).WithArgs("connector-target").WillReturnRows(sqlmock.NewRows(columns))
			mock.ExpectQuery(placementQuery).WithArgs("expert-source").WillReturnRows(sqlmock.NewRows(columns).AddRow("research", category, true, 17, created, updated))
			mock.ExpectExec("INSERT INTO plugin_placements").WithArgs(applyStableID("run:placement:connector-target:research"), "research", "connector-target", category, true, 17, created, updated).WillReturnResult(sqlmock.NewResult(0, 1))

			categoryRows := sqlmock.NewRows([]string{"placement_id", "visible", "sort_order"})
			if existing {
				categoryRows.AddRow("existing-category", false, 42)
			}
			mock.ExpectQuery("SELECT placement_id,visible,sort_order FROM plugin_category_placements").WithArgs("research", model.PluginTypeConnector, category).WillReturnRows(categoryRows)
			if !existing {
				mock.ExpectExec("INSERT INTO plugin_category_placements").WithArgs(applyStableID("category:research:connector:"+category), "research", model.PluginTypeConnector, category, true, 17, created, updated).WillReturnResult(sqlmock.NewResult(0, 1))
			}

			n := Node{SourceID: "expert-source", Source: model.Plugin{ID: "expert-source", Type: model.PluginTypeExpert}, Plugin: model.Plugin{ID: "connector-target", Type: model.PluginTypeConnector}, Clone: true}
			if err := transferMigrationPlacements(context.Background(), tx, Plan{CategorySnapshotCaptured: true, CategorySnapshot: map[string]string{"existing-category": "captured"}, ID: "run"}, n); err != nil {
				t.Fatal(err)
			}
			mock.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestApplyMySQLCategoryRegistrationKeepsExistingAndSeedsMissing(t *testing.T) {
	for _, existing := range []bool{true, false} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			category := "migration-category"
			if _, err := db.Exec(`INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at) VALUES ('source-placement','research','expert-source',?,1,17,?,?)`, category, now, now); err != nil {
				t.Fatal(err)
			}
			if existing {
				if _, err := db.Exec(`INSERT INTO plugin_category_placements (placement_id,placement_code,plugin_type,category_id,visible,sort_order,created_at,updated_at) VALUES ('existing-category','research','connector',?,0,42,?,?)`, category, now, now); err != nil {
					t.Fatal(err)
				}
			}
			categories, err := captureCategorySnapshot(ctx, db, false)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			n := Node{SourceID: "expert-source", Plugin: model.Plugin{ID: "connector-target", Type: model.PluginTypeConnector}, Clone: true}
			if err = transferMigrationPlacements(ctx, tx, Plan{CategorySnapshotCaptured: true, CategorySnapshot: categories, ID: "run"}, n); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var visible bool
			var order int
			var id string
			if err = db.QueryRow(`SELECT placement_id,visible,sort_order FROM plugin_category_placements WHERE placement_code='research' AND plugin_type='connector' AND category_id=?`, category).Scan(&id, &visible, &order); err != nil {
				t.Fatal(err)
			}
			if existing {
				if visible || order != 42 || id != "existing-category" {
					t.Fatal("existing registration overwritten")
				}
			} else if !visible || order != 17 {
				t.Fatal("missing registration did not use verified placement")
			}
		})
	}
}

// Shared category configuration and resource placements must both stay fixed.
func TestApplyMySQLRejectsCategoryChangeAfterPlan(t *testing.T) {
	for _, mutation := range []string{"update shared registration", "delete shared registration", "insert shared registration", "update resource placement"} {
		t.Run(mutation, func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			seedPublicationInput(t, db, model.PluginTypeSkill, false, true)
			now := time.Now().UTC().Truncate(time.Millisecond)
			const category = "review-category"
			if _, err := db.Exec(`INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at) VALUES ('source-placement','research','source',?,1,17,?,?)`, category, now, now); err != nil {
				t.Fatal(err)
			}
			insertCategory := func() {
				t.Helper()
				if _, err := db.Exec(`INSERT INTO plugin_category_placements (placement_id,placement_code,plugin_type,category_id,visible,sort_order,created_at,updated_at) VALUES ('shared-category','research','skill',?,0,42,?,?)`, category, now, now); err != nil {
					t.Fatal(err)
				}
			}
			if mutation != "insert shared registration" {
				insertCategory()
			}
			input, issues, err := InputFromDatabase(ctx, db, nil)
			if err != nil || len(issues) > 0 {
				t.Fatalf("input: %v %+v", err, issues)
			}
			plan, err := Build(input, Policy{})
			if err != nil {
				t.Fatalf("build: %v %+v", err, plan.Issues)
			}
			identity := applyIdentity(plan)
			var binding DatabaseIdentity
			if err = db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "update shared registration":
				_, err = db.Exec(`UPDATE plugin_category_placements SET sort_order=99 WHERE placement_id='shared-category'`)
			case "delete shared registration":
				_, err = db.Exec(`DELETE FROM plugin_category_placements WHERE placement_id='shared-category'`)
			case "insert shared registration":
				insertCategory()
			case "update resource placement":
				_, err = db.Exec(`UPDATE plugin_placements SET sort_order=99 WHERE placement_id='source-placement'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = CheckPlanSources(ctx, db, plan, binding); err == nil {
				t.Fatal("post-plan placement drift accepted")
			}
			if _, err = Apply(ctx, db, plan, binding); err == nil {
				t.Fatal("apply accepted post-plan placement drift")
			}
			if identity != applyIdentity(plan) {
				t.Fatal("plan changed")
			}
		})
	}
}

func TestApplyMySQLCategorySnapshotAllowsOnlyReceiptedOwnInsertOnResume(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			seedPublicationInput(t, db, model.PluginTypeSkill, false, true)
			if _, err := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,is_embedded,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at) SELECT 'source-two','source-two',plugin_type,tags_json,owner_uid,visibility,'draft',is_embedded,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at FROM plugins WHERE plugin_id='source'`); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Millisecond)
			for _, id := range []string{"source", "source-two"} {
				order := 17
				if conflict && id == "source-two" {
					order = 99
				}
				if _, err := db.Exec(`INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at) VALUES (?, 'research',?,'new-category',1,?,?,?)`, id+"-placement", id, order, now, now); err != nil {
					t.Fatal(err)
				}
			}
			input, issues, err := InputFromDatabase(ctx, db, nil)
			if err != nil || len(issues) > 0 {
				t.Fatalf("input %v %+v", err, issues)
			}
			plan, err := Build(input, Policy{})
			if err != nil {
				t.Fatalf("build %v %+v", err, plan.Issues)
			}
			if len(plan.Nodes) != 2 {
				t.Fatalf("want two nodes, got %d", len(plan.Nodes))
			}
			var binding DatabaseIdentity
			if err = db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			if conflict {
				if report, err := Apply(ctx, db, plan, binding); err == nil || report.Complete {
					t.Fatal("conflicting category seeds accepted")
				}
				return
			}
			if report, err := Apply(ctx, db, plan, binding); err != nil || !report.Complete {
				t.Fatalf("apply %v %+v", err, report)
			}
			if err = CheckPlanSources(ctx, db, plan, binding); err != nil {
				t.Fatal("own inserts blocked resume", err)
			}
			if report, err := Apply(ctx, db, plan, binding); err != nil || !report.Complete {
				t.Fatalf("resume %v %+v", err, report)
			}
			if _, err = Verify(ctx, db, plan, binding); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`UPDATE plugin_category_placements SET sort_order=99 WHERE category_id='new-category'`); err != nil {
				t.Fatal(err)
			}
			if err = CheckPlanSources(ctx, db, plan, binding); err == nil {
				t.Fatal("tampered own registration accepted")
			}
		})
	}
}

func TestApplyMySQLCompletedRunRejectsUnrelatedCategoryDrift(t *testing.T) {
	for _, kind := range []string{"insert", "update", "delete"} {
		t.Run(kind, func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			seedPublicationInput(t, db, model.PluginTypeSkill, false, true)
			now := time.Now().UTC().Truncate(time.Millisecond)
			if _, err := db.Exec(`INSERT INTO plugin_category_placements VALUES ('shared','research','skill','cat',0,42,?,?)`, now, now); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at) VALUES ('source-placement','research','source','cat',1,17,?,?)`, now, now); err != nil {
				t.Fatal(err)
			}
			input, issues, err := InputFromDatabase(ctx, db, nil)
			if err != nil || len(issues) > 0 {
				t.Fatalf("input %v %+v", err, issues)
			}
			p, err := Build(input, Policy{})
			if err != nil {
				t.Fatal(err)
			}
			var binding DatabaseIdentity
			if err = db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			if r, err := Apply(ctx, db, p, binding); err != nil || !r.Complete {
				t.Fatalf("apply %v %+v", err, r)
			}
			var visible bool
			var order int
			if err = db.QueryRow(`SELECT visible,sort_order FROM plugin_category_placements WHERE placement_id='shared'`).Scan(&visible, &order); err != nil || visible || order != 42 {
				t.Fatal("shared catalog policy was overwritten", err)
			}
			switch kind {
			case "insert":
				_, err = db.Exec(`INSERT INTO plugin_category_placements VALUES ('unrelated','elsewhere','expert','other',1,0,?,?)`, now, now)
			case "update":
				_, err = db.Exec(`UPDATE plugin_category_placements SET sort_order=99 WHERE placement_id='shared'`)
			case "delete":
				_, err = db.Exec(`DELETE FROM plugin_category_placements WHERE placement_id='shared'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if r, err := Apply(ctx, db, p, binding); err == nil || r.Complete {
				t.Fatal("completed apply accepted global category drift")
			}
			if r, err := Verify(ctx, db, p, binding); err == nil || r.Complete {
				t.Fatal("verify accepted global category drift")
			}
		})
	}
}

// Exercise the exact inter-transaction window from review: the first resource
// has committed its new registration and receipt, but the second has not run.
func TestApplyMySQLNewCategoryFullRowDriftBetweenNodeCommits(t *testing.T) {
	mutations := []struct{ name, set string }{
		{"placement identity", "placement_id='changed-id'"},
		{"scene", "placement_code='changed-scene'"},
		{"resource type", "plugin_type='expert'"},
		{"category", "category_id='changed-category'"},
		{"visibility", "visible=0"},
		{"order", "sort_order=99"},
		{"creation time", "created_at=DATE_ADD(created_at,INTERVAL 1 SECOND)"},
		{"update time", "updated_at=DATE_ADD(updated_at,INTERVAL 1 SECOND)"},
		{"deletion", ""},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			seedPublicationInput(t, db, model.PluginTypeSkill, false, true)
			if _, err := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,is_embedded,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at) SELECT 'source-two','source-two',plugin_type,tags_json,owner_uid,visibility,'draft',is_embedded,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at FROM plugins WHERE plugin_id='source'`); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Millisecond)
			for _, id := range []string{"source", "source-two"} {
				if _, err := db.Exec(`INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at) VALUES (?,'research',?,'intercommit-category',1,17,?,?)`, id+"-placement", id, now, now); err != nil {
					t.Fatal(err)
				}
			}
			input, issues, err := InputFromDatabase(ctx, db, nil)
			if err != nil || len(issues) != 0 {
				t.Fatalf("input: %v %+v", err, issues)
			}
			plan, err := Build(input, Policy{})
			if err != nil || len(plan.Nodes) != 2 {
				t.Fatalf("plan: %v %+v", err, plan.Issues)
			}
			var binding DatabaseIdentity
			if err = db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			if err = setupApply(ctx, db, plan); err != nil {
				t.Fatal(err)
			}
			if err = applyNode(ctx, db, plan, plan.Nodes[0]); err != nil {
				t.Fatal("first node failed", err)
			}
			if err = CheckPlanSources(ctx, db, plan, binding); err != nil {
				t.Fatal("untampered partial run rejected", err)
			}
			id := applyStableID("category:research:skill:intercommit-category")
			var originalVisible bool
			var originalOrder int
			var created, updated time.Time
			if err = db.QueryRow(`SELECT visible,sort_order,created_at,updated_at FROM plugin_category_placements WHERE placement_id=?`, id).Scan(&originalVisible, &originalOrder, &created, &updated); err != nil {
				t.Fatal("expected run-created category missing", err)
			}
			if mutation.set == "" {
				_, err = db.Exec(`DELETE FROM plugin_category_placements WHERE placement_id=?`, id)
			} else {
				_, err = db.Exec(`UPDATE plugin_category_placements SET `+mutation.set+` WHERE placement_id=?`, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = CheckPlanSources(ctx, db, plan, binding); err == nil {
				t.Fatal("partial-run preflight accepted category drift")
			}
			if err = applyNode(ctx, db, plan, plan.Nodes[1]); err == nil {
				t.Fatal("next node accepted category drift")
			}
			var committed int
			if err = db.QueryRow(`SELECT COUNT(*) FROM marketplace_conversion_receipt WHERE run_id=?`, plan.ID).Scan(&committed); err != nil || committed != 1 {
				t.Fatal("second node committed despite drift", err)
			}
			if report, err := Verify(ctx, db, plan, binding); err == nil || report.Complete {
				t.Fatal("final verification certified altered category policy")
			}
			// Restore the exact row and complete the original plan. This proves
			// the rejection was drift, not merely an incomplete two-node run.
			deleteID := id
			if mutation.name == "placement identity" {
				deleteID = "changed-id"
			}
			if _, err = db.Exec(`DELETE FROM plugin_category_placements WHERE placement_id=?`, deleteID); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO plugin_category_placements (placement_id,placement_code,plugin_type,category_id,visible,sort_order,created_at,updated_at) VALUES (?,'research','skill','intercommit-category',?,?,?,?)`, id, originalVisible, originalOrder, created, updated); err != nil {
				t.Fatal(err)
			}
			if report, err := Apply(ctx, db, plan, binding); err != nil || !report.Complete {
				t.Fatalf("restored partial run failed: %v %+v", err, report)
			}
			if mutation.set == "" {
				_, err = db.Exec(`DELETE FROM plugin_category_placements WHERE placement_id=?`, id)
			} else {
				_, err = db.Exec(`UPDATE plugin_category_placements SET `+mutation.set+` WHERE placement_id=?`, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			if report, err := Verify(ctx, db, plan, binding); err == nil || report.Complete {
				t.Fatal("completed-run verification certified category drift")
			}
		})
	}
}
