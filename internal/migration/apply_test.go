package migration

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"testing"
	"time"
)

func TestApplyRejectsProductionBeforeWrites(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db"}).AddRow("octo_marketplace_prod"))
	_, e = Apply(context.Background(), db, Plan{ID: "run", SnapshotID: "snapshot"})
	if e == nil {
		t.Fatal("production accepted")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestApplyIssuesNeverWrites(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	_, e := Apply(context.Background(), db, Plan{Issues: []Issue{{Code: "missing"}}})
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
func TestVerifyNeverWritesProduction(t *testing.T) {
	db, m, _ := sqlmock.New()
	defer db.Close()
	m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db"}).AddRow("octo_marketplace_rehearsal_prod"))
	if _, e := Verify(context.Background(), db, Plan{}); e == nil {
		t.Fatal("production accepted")
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
