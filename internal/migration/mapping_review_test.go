package migration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestDeletedHistoryEmptyTargetMappingSurvivesBuildApplyVerify(t *testing.T) {
	deleted := resource("deleted-source", "deleted", "", "historical instructions")
	deletedAt := time.Unix(200, 0)
	deleted.Plugin.DeletedAt = &deletedAt
	plan, err := Build(Input{CategorySnapshotCaptured: true, SnapshotID: "mapping-history", Resources: []Resource{deleted}}, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Mapping{{SourceID: "deleted-source", TargetID: "", VersionID: "", Disposition: "deleted_history"}}
	if len(plan.Nodes) != 0 || !reflect.DeepEqual(plan.Mappings, want) {
		t.Fatalf("unexpected deleted-history plan: %+v", plan)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	binding := DatabaseIdentity{Name: "mapping-test", ServerUUID: "test-server"}
	expectGuard := func() {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(),@@server_uuid")).WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow(binding.Name, binding.ServerUUID))
	}
	expectGuard()
	expectEmptyTargetMappingSetup(mock, plan)
	// Apply calls Verify itself; a second explicit Verify checks the same
	// persisted mapping without setup issuing another INSERT.
	for i := 0; i < 2; i++ {
		expectGuard()
		expectMappingRunCheck(mock, plan)
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT \\* FROM plugin_category_placements ORDER BY placement_id FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"placement_id"}))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT source_id,target_id,version_id,disposition FROM marketplace_conversion_mapping WHERE run_id=? ORDER BY source_id,target_id")).
			WithArgs(plan.ID).
			WillReturnRows(sqlmock.NewRows([]string{"source_id", "target_id", "version_id", "disposition"}).AddRow("deleted-source", "", "", "deleted_history"))
		mock.ExpectCommit()
	}
	for _, run := range []struct {
		name string
		call func() (ApplyReport, error)
	}{
		{"Apply", func() (ApplyReport, error) { return Apply(context.Background(), db, plan, binding) }},
		{"Verify", func() (ApplyReport, error) { return Verify(context.Background(), db, plan, binding) }},
	} {
		report, err := run.call()
		if err != nil || !report.Complete || report.CommitState != "complete" {
			t.Fatalf("%s: report=%+v, err=%v", run.name, report, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPendingReviewEmptyTargetMappingIsBlockedBeforePersistence(t *testing.T) {
	pending := resource("pending-source", "pending", "", "pending instructions")
	pending.PendingReview = true
	plan, err := Build(Input{SnapshotID: "mapping-pending", Resources: []Resource{pending}}, Policy{})
	if !errors.Is(err, ErrIssues) || !reflect.DeepEqual(plan.Issues, []Issue{{ResourceID: "pending-source", Code: "pending_review"}}) {
		t.Fatalf("Build must block pending review: plan=%+v, err=%v", plan, err)
	}
	want := []Mapping{{SourceID: "pending-source", TargetID: "", VersionID: "", Disposition: "pending_review"}}
	if len(plan.Nodes) != 0 || !reflect.DeepEqual(plan.Mappings, want) {
		t.Fatalf("unexpected pending-review plan: %+v", plan)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// No SQL is expected: pending review is a planning blocker, rather than a
	// mapping that Apply promises to persist as a completed migration.
	if _, err := Apply(context.Background(), db, plan, DatabaseIdentity{}); !errors.Is(err, ErrIssues) {
		t.Fatalf("Apply error = %v, want ErrIssues", err)
	}
	if _, err := Verify(context.Background(), db, plan, DatabaseIdentity{}); !errors.Is(err, ErrIssues) {
		t.Fatalf("Verify error = %v, want ErrIssues", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyMySQLDeletedHistoryMappingAlongsideActiveSkill(t *testing.T) {
	db := applyTestDB(t)
	ctx := context.Background()
	in := seedPublicationInput(t, db, model.PluginTypeSkill, false, false)
	deleted := resource("deleted-source", "deleted", "", "retained historical instructions")
	stamp := time.Now().UTC().Truncate(time.Millisecond)
	deleted.Plugin.CreatedAt, deleted.Plugin.UpdatedAt, deleted.Plugin.DeletedAt = stamp, stamp, &stamp
	p := deleted.Plugin
	if _, err := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at,deleted_at) VALUES (?,?,?,'[]',?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.Type, p.OwnerUID, p.Visibility, p.ListingState, string(p.Manifest), string(p.Package), p.ManifestHash, p.PluginHash, p.Status, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	in.Resources = append(in.Resources, deleted)
	plan, err := Build(in, Policy{})
	if err != nil || len(plan.Nodes) != 1 || len(plan.Mappings) != 2 {
		t.Fatalf("Build: plan=%+v, err=%v", plan, err)
	}
	var binding DatabaseIdentity
	if err := db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if report, err := Apply(ctx, db, plan, binding); err != nil || !report.Complete || report.Verified != 1 {
			t.Fatalf("Apply attempt %d: report=%+v, err=%v", attempt, report, err)
		}
		if report, err := Verify(ctx, db, plan, binding); err != nil || !report.Complete || report.Verified != 1 {
			t.Fatalf("Verify attempt %d: report=%+v, err=%v", attempt, report, err)
		}
		var mapping Mapping
		if err := db.QueryRow(`SELECT source_id,target_id,version_id,disposition FROM marketplace_conversion_mapping WHERE run_id=? AND source_id=?`, plan.ID, "deleted-source").Scan(&mapping.SourceID, &mapping.TargetID, &mapping.VersionID, &mapping.Disposition); err != nil {
			t.Fatal(err)
		}
		if mapping != (Mapping{SourceID: "deleted-source", TargetID: "", VersionID: "", Disposition: "deleted_history"}) {
			t.Fatalf("deleted mapping changed: %+v", mapping)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM marketplace_conversion_mapping WHERE run_id=?`, plan.ID).Scan(&count); err != nil || count != 2 {
			t.Fatalf("expected active and deleted mappings once each: count=%d, err=%v", count, err)
		}
	}
	var retained string
	if err := db.QueryRow(`SELECT plugin_json FROM plugins WHERE plugin_id='deleted-source' AND deleted_at IS NOT NULL`).Scan(&retained); err != nil {
		t.Fatalf("deleted historical row lost: %v", err)
	}
	var before, after any
	if json.Unmarshal(p.Package, &before) != nil || json.Unmarshal([]byte(retained), &after) != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("deleted historical package changed")
	}
}

func expectEmptyTargetMappingSetup(mock sqlmock.Sqlmock, plan Plan) {
	for _, table := range []string{"marketplace_conversion_run", "marketplace_conversion_receipt", "marketplace_conversion_mapping"} {
		mock.ExpectExec("CREATE TABLE IF NOT EXISTS " + table).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	expectMappingSchema(mock)
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO marketplace_conversion_run VALUES (1,?,?,?)")).WithArgs(plan.ID, applyIdentity(plan), mappingEmptySchemaHash).WillReturnResult(sqlmock.NewResult(0, 1))
	expectMappingRunCheck(mock, plan)
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO marketplace_conversion_mapping VALUES (?,?,?,?,?)")).WithArgs(plan.ID, "deleted-source", "", "", "deleted_history").WillReturnResult(sqlmock.NewResult(0, 1))
}

const mappingEmptySchemaHash = "61ebc1b271f7646b4ca4c638c2e2585d610bed1a4707c5fcd94ad3fe750cb407"

func expectMappingSchema(mock sqlmock.Sqlmock) {
	for _, projection := range schemaIdentityQueries {
		mock.ExpectQuery(regexp.QuoteMeta(projection.query)).WillReturnRows(sqlmock.NewRows([]string{"unused"}))
	}
}

func expectMappingRunCheck(mock sqlmock.Sqlmock, plan Plan) {
	expectMappingSchema(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT run_id,identity_hash,schema_hash FROM marketplace_conversion_run WHERE slot=1")).WillReturnRows(sqlmock.NewRows([]string{"run_id", "identity_hash", "schema_hash"}).AddRow(plan.ID, applyIdentity(plan), mappingEmptySchemaHash))
}
