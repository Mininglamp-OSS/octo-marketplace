package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"testing"
	"time"
)

func TestApplyAndVerifyRequireExactDatabaseBindingBeforeWrites(t *testing.T) {
	for _, op := range []struct {
		name string
		run  func(context.Context, *sql.DB, Plan, DatabaseIdentity) (ApplyReport, error)
	}{{"apply", Apply}, {"verify", Verify}} {
		for _, expected := range []DatabaseIdentity{{}, {Name: "current"}, {Name: "other", ServerUUID: "server"}, {Name: "current", ServerUUID: "other"}} {
			t.Run(op.name+expected.Name+expected.ServerUUID, func(t *testing.T) {
				db, m, _ := sqlmock.New()
				defer db.Close()
				if expected.Name != "" && expected.ServerUUID != "" {
					m.ExpectQuery("SELECT DATABASE\\(\\),@@server_uuid").WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow("current", "server"))
				}
				if _, e := op.run(context.Background(), db, Plan{ID: "run", SnapshotID: "snapshot"}, expected); e == nil {
					t.Fatal("unbound or mismatched database accepted")
				}
				if e := m.ExpectationsWereMet(); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}
func TestDatabaseBindingAcceptsExplicitProductionName(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectQuery("SELECT DATABASE\\(\\),@@server_uuid").WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow("octo_marketplace_prod", "server"))
	if e := guardApply(context.Background(), db, DatabaseIdentity{Name: "octo_marketplace_prod", ServerUUID: "server"}); e != nil {
		t.Fatal(e)
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestApplyIssuesNeverWrites(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	_, e := Apply(context.Background(), db, Plan{Issues: []Issue{{Code: "missing"}}}, DatabaseIdentity{})
	if e == nil {
		t.Fatal("issues accepted")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestReceiptRejectsChangedNode(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	p := Plan{ID: "run"}
	n := Node{SourceID: "source"}
	m.ExpectQuery("SELECT node_hash,state_hash").WithArgs("run", "").WillReturnRows(sqlmock.NewRows([]string{"node_hash", "state_hash"}).AddRow("different", "same"))
	if e := verifyReceipt(context.Background(), db, p, n); e == nil {
		t.Fatal("changed node accepted")
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestPlacementMergePreservesHiddenScenesAndRejectsConflicts(t *testing.T) {
	rows := []model.PluginPlacement{{PlacementCode: "default", CategoryID: stringPtrApply("cat"), Visible: false, SortOrder: 7}, {PlacementCode: "custom", Visible: true, SortOrder: 42}}
	got, e := mergeMigrationPlacements(nil, rows)
	if e != nil || len(got) != 2 || got[0].Visible || got[0].SortOrder != 7 || got[1].PlacementCode != "custom" {
		t.Fatalf("placements changed: %#v %v", got, e)
	}
	conflict := rows[0]
	conflict.Visible = true
	if _, e = mergeMigrationPlacements(rows, []model.PluginPlacement{conflict}); e == nil {
		t.Fatal("conflicting visibility silently merged")
	}
}
func TestRetirePreservesHistoricalInstallIdentity(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectBegin()
	tx, _ := db.Begin()
	m.ExpectExec("UPDATE plugin_placements SET visible=0 WHERE plugin_id=").WithArgs("old").WillReturnResult(sqlmock.NewResult(0, 2))
	if e := retireMigrationCatalog(context.Background(), tx, "old"); e != nil {
		t.Fatal(e)
	}
	m.ExpectRollback()
	tx.Rollback()
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func stringPtrApply(s string) *string { return &s }
func TestTransferPlacementsSQLPreservesHiddenAndCustomMetadata(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectBegin()
	tx, _ := db.Begin()
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	m.ExpectQuery("SELECT placement_code,category_id,visible,sort_order,created_at,updated_at FROM plugin_placements").WithArgs("target").WillReturnRows(sqlmock.NewRows([]string{"scene", "cat", "visible", "sort", "created", "updated"}))
	m.ExpectQuery("SELECT placement_code,category_id,visible,sort_order,created_at,updated_at FROM plugin_placements").WithArgs("source").WillReturnRows(sqlmock.NewRows([]string{"scene", "cat", "visible", "sort", "created", "updated"}).AddRow("default", nil, false, 7, now, now).AddRow("research", nil, true, 42, now, now))
	for _, v := range []struct {
		scene   string
		visible bool
		sort    int
	}{{"default", false, 7}, {"research", true, 42}} {
		m.ExpectExec("INSERT INTO plugin_placements").WithArgs(applyStableID("run:placement:target:"+v.scene), v.scene, "target", nil, v.visible, v.sort, now, now).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	if e := transferMigrationPlacements(context.Background(), tx, Plan{ID: "run"}, Node{SourceID: "source", Plugin: model.Plugin{ID: "target"}, Clone: true}); e != nil {
		t.Fatal(e)
	}
	m.ExpectRollback()
	tx.Rollback()
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestJSONNullPersistsAsSQLNull(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage(" \n null\t")} {
		if got := jvalue(raw); got != nil {
			t.Errorf("JSON null must become SQL NULL, got %#v", got)
		}
	}
	if got := jvalue(json.RawMessage("{}")); got != "{}" {
		t.Fatalf("object changed: %#v", got)
	}
}
