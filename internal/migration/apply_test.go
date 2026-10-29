package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestSchemaIdentityIncludesIndexesAndConstraints(t *testing.T) {
	queries := []string{"SELECT TABLE_NAME,COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE", "SELECT TABLE_NAME,ENGINE", "SELECT TABLE_NAME,INDEX_NAME", "SELECT TABLE_NAME,CONSTRAINT_NAME,CONSTRAINT_TYPE", "SELECT TABLE_NAME,CONSTRAINT_NAME,COLUMN_NAME", "SELECT TABLE_NAME,CONSTRAINT_NAME,UNIQUE_CONSTRAINT_SCHEMA", "SELECT CONSTRAINT_NAME,CHECK_CLAUSE"}
	for changed := 1; changed < len(queries); changed++ {
		hash := func(value string) string {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for i, q := range queries {
				rows := sqlmock.NewRows([]string{"definition"})
				if i == changed {
					rows.AddRow(value)
				}
				mock.ExpectQuery(regexp.QuoteMeta(q)).WillReturnRows(rows)
			}
			h, err := applySchemaHash(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			return h
		}
		if hash("original") == hash("changed") {
			t.Fatalf("schema metadata projection %d was not bound", changed)
		}
	}
}

func TestSchemaIdentityRefusesIncompleteMetadata(t *testing.T) {
	for fail := range schemaIdentityQueries {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		for i, projection := range schemaIdentityQueries {
			if i == fail {
				mock.ExpectQuery(regexp.QuoteMeta(projection.query)).WillReturnError(errors.New("metadata unavailable"))
				break
			}
			mock.ExpectQuery(regexp.QuoteMeta(projection.query)).WillReturnRows(sqlmock.NewRows([]string{"unused"}))
		}
		if _, err := applySchemaHash(context.Background(), db); err == nil {
			t.Fatalf("projection %d failure ignored", fail)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
}

func TestApplyAndVerifyRequireExactDatabaseBindingBeforeWrites(t *testing.T) {
	for _, op := range []struct {
		name string
		run  func(context.Context, *sql.DB, Plan, DatabaseIdentity) (ApplyReport, error)
	}{{"apply", func(ctx context.Context, db *sql.DB, p Plan, binding DatabaseIdentity) (ApplyReport, error) {
		return Apply(ctx, db, p, binding)
	}}, {"verify", Verify}} {
		for _, expected := range []DatabaseIdentity{{}, {Name: "current"}, {Name: "other", ServerUUID: "server"}, {Name: "current", ServerUUID: "other"}} {
			t.Run(op.name+expected.Name+expected.ServerUUID, func(t *testing.T) {
				db, m, _ := sqlmock.New()
				defer db.Close()
				if expected.Name != "" && expected.ServerUUID != "" {
					m.ExpectQuery("SELECT DATABASE\\(\\),@@server_uuid").WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow("current", "server"))
				}
				if _, e := op.run(context.Background(), db, Plan{CategorySnapshotCaptured: true, ID: "run", SnapshotID: "snapshot"}, expected); e == nil {
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
	p := Plan{CategorySnapshotCaptured: true, ID: "run"}
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
	if e := transferMigrationPlacements(context.Background(), tx, Plan{CategorySnapshotCaptured: true, ID: "run"}, Node{SourceID: "source", Plugin: model.Plugin{ID: "target"}, Clone: true}); e != nil {
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

// Every persisted source column participates, including columns unknown to Plugin.
func TestApplyRejectsAnySourceColumnDriftBeforeWrites(t *testing.T) {
	columns := strings.Split(databaseSchema[0].columns, ",")
	columns = append(columns, "future_metadata")
	original := make(map[string][]byte, len(columns))
	for _, col := range columns {
		original[col] = []byte("old")
	}
	for _, col := range []string{"created_at", "updated_at", "deleted_at"} {
		original[col] = nil
	}
	for _, col := range columns {
		if strings.HasSuffix(col, "_json") {
			original[col] = []byte(`{}`)
		}
	}
	original["plugin_id"] = []byte("source")
	original["published_version_id"] = nil
	expected, err := sourceRowDigest(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range columns {
		t.Run(changed, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			vals := make([]driver.Value, len(columns))
			for i, col := range columns {
				vals[i] = original[col]
				if original[col] == nil {
					vals[i] = nil
				}
			}
			for i, col := range columns {
				if col == changed {
					switch {
					case strings.HasSuffix(col, "_json"):
						vals[i] = []byte(`{"changed":true}`)
					case col == "created_at" || col == "updated_at" || col == "deleted_at":
						vals[i] = []byte("2026-10-28 00:00:01")
					default:
						vals[i] = []byte("changed")
					}
				}
			}
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT node_hash FROM marketplace_conversion_receipt").WithArgs("run", "target").WillReturnError(sql.ErrNoRows)
			mock.ExpectQuery("SELECT \\* FROM plugin_category_placements ORDER BY placement_id FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"placement_id"}))
			mock.ExpectQuery(`SELECT \* FROM plugins WHERE plugin_id=\? FOR UPDATE`).WithArgs("source").WillReturnRows(sqlmock.NewRows(columns).AddRow(vals...))
			mock.ExpectRollback()
			n := Node{SourceID: "source", SourceRowHash: expected, Evidence: map[string]SourceEvidence{"source": {RowHash: expected, Tables: emptySourceTables()}}, Plugin: model.Plugin{ID: "target"}}
			if err := applyNode(context.Background(), db, Plan{CategorySnapshotCaptured: true, ID: "run"}, n); err == nil {
				t.Fatal("changed source accepted")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSourceRowDigestBindsNullsUnknownColumnsAndCanonicalValues(t *testing.T) {
	a := map[string][]byte{"created_at": []byte("2026-10-28 00:00:00.123000"), "tags_json": []byte(`{"b":9007199254740993,"a":1}`), "future_metadata": nil}
	b := map[string][]byte{"future_metadata": nil, "tags_json": []byte(`{ "a":1,"b":9007199254740993 }`), "created_at": []byte("2026-10-28T00:00:00.123Z")}
	first, err := sourceRowDigest(a)
	if err != nil {
		t.Fatal(err)
	}
	same, err := sourceRowDigest(b)
	if err != nil || same != first {
		t.Fatal("equivalent driver values differ", err)
	}
	b["future_metadata"] = []byte{}
	different, err := sourceRowDigest(b)
	if err != nil || different == first {
		t.Fatal("SQL NULL equals empty bytes", err)
	}
	delete(b, "future_metadata")
	missing, err := sourceRowDigest(b)
	if err != nil || missing == first {
		t.Fatal("column absence ignored", err)
	}
	b["tags_json"] = []byte(`{"b":9007199254740992,"a":1}`)
	changed, err := sourceRowDigest(b)
	if err != nil || changed == missing {
		t.Fatal("JSON integer precision lost", err)
	}
}

func TestApplyAcceptsEquivalentFullSourceRowBeforeClone(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expected, err := sourceRowDigest(map[string][]byte{
		"plugin_id": []byte("source"), "published_version_id": nil,
		"created_at": []byte("2026-10-28 00:00:00.123000"), "tags_json": []byte(`{"a":1,"b":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	reachedClone := errors.New("clone reached after successful source check")
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT node_hash FROM marketplace_conversion_receipt").WithArgs("run", "target").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT \\* FROM plugin_category_placements ORDER BY placement_id FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"placement_id"}))
	mock.ExpectQuery(`SELECT \* FROM plugins WHERE plugin_id=\? FOR UPDATE`).WithArgs("source").WillReturnRows(sqlmock.NewRows([]string{"plugin_id", "published_version_id", "created_at", "tags_json"}).AddRow("source", nil, time.Date(2026, 10, 28, 0, 0, 0, 123000000, time.UTC), `{ "b": 2, "a": 1 }`))
	expectSourceTables(mock, "source", true)
	mock.ExpectQuery("SELECT COLUMN_NAME FROM information_schema.COLUMNS").WillReturnError(reachedClone)
	mock.ExpectRollback()
	n := Node{SourceID: "source", SourceRowHash: expected, Evidence: map[string]SourceEvidence{"source": {RowHash: expected, Tables: emptySourceTables()}}, Plugin: model.Plugin{ID: "target"}}
	if err := applyNode(context.Background(), db, Plan{CategorySnapshotCaptured: true, ID: "run"}, n); !errors.Is(err, reachedClone) {
		t.Fatalf("unchanged row rejected: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRejectsCheckpointWithoutFullSourceEvidence(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT node_hash FROM marketplace_conversion_receipt").WithArgs("run", "target").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT \\* FROM plugin_category_placements ORDER BY placement_id FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"placement_id"}))
	mock.ExpectRollback()
	if err := applyNode(context.Background(), db, Plan{CategorySnapshotCaptured: true, ID: "run"}, Node{SourceID: "source", Plugin: model.Plugin{ID: "target"}}); err == nil {
		t.Fatal("missing evidence accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A source row can remain identical while its owned inventory changes.
func TestSourceInventoryDriftIncludesMappedDuplicates(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		for _, changed := range sourceTables {
			for _, mutation := range []string{"insert", "update", "delete"} {
				t.Run(fmt.Sprintf("mapped=%t/%s/%s", mapped, changed.table, mutation), func(t *testing.T) {
					db, mock, err := sqlmock.New()
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					id := "source"
					if mapped {
						id = "duplicate"
					}
					evidence := SourceEvidence{RowHash: "row", Tables: emptySourceTables()}
					oldRow := map[string][]byte{"id": []byte("old"), "metadata": []byte("original")}
					oldHash, err := sourceRowDigest(oldRow)
					if err != nil {
						t.Fatal(err)
					}
					if mutation != "insert" {
						raw, _ := json.Marshal([]string{oldHash})
						evidence.Tables[changed.table] = digest(raw)
					}
					n := Node{SourceID: "source", SourceRowHash: "row", Plugin: model.Plugin{ID: "target"}, Evidence: map[string]SourceEvidence{"source": {RowHash: "row", Tables: emptySourceTables()}}}
					n.Evidence[id] = evidence
					plan := Plan{Mappings: []Mapping{{SourceID: id, TargetID: "target"}}}
					mock.ExpectBegin()
					tx, _ := db.Begin()
					if mapped {
						mock.ExpectQuery(`SELECT \* FROM plugins WHERE plugin_id=\? FOR UPDATE`).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("original"))
						rowHash, _ := sourceRowDigest(map[string][]byte{"id": []byte("original")})
						evidence.RowHash = rowHash
						n.Evidence[id] = evidence
					}
					// captureSourceTables reads all row sets under locks before comparing.
					for _, spec := range sourceTables {
						rows := sqlmock.NewRows([]string{"id", "metadata"})
						if spec.table == changed.table && mutation != "delete" {
							rows.AddRow("old", "changed")
						}
						query := "SELECT * FROM " + spec.table + " WHERE " + spec.owner + "=? ORDER BY " + spec.order + " FOR UPDATE"
						mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(id).WillReturnRows(rows)
					}
					if err := verifyNodeSourceTables(context.Background(), tx, plan, n); err == nil {
						t.Fatal("source inventory drift accepted")
					}
					mock.ExpectRollback()
					tx.Rollback()
					if err := mock.ExpectationsWereMet(); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestPartialApplyReportUsesPersistedReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		ids         []string
		fail        bool
	}{
		{name: "none", state: "none"}, {name: "partial", state: "partial", ids: []string{"first"}}, {name: "unavailable", state: "unknown", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			query := mock.ExpectQuery("SELECT target_id FROM marketplace_conversion_receipt").WithArgs("run")
			if tc.fail {
				query.WillReturnError(errors.New("unavailable"))
			} else {
				rows := sqlmock.NewRows([]string{"target_id"})
				for _, id := range tc.ids {
					rows.AddRow(id)
				}
				query.WillReturnRows(rows)
			}
			r := ApplyReport{}
			describePartialApply(context.Background(), db, Plan{CategorySnapshotCaptured: true, ID: "run"}, &r)
			if r.CommitState != tc.state || len(r.CommittedIDs) != len(tc.ids) || r.Recovery != "retry_same_checkpoint_or_restore_backup" || r.Complete {
				t.Fatalf("unsafe recovery report: %#v", r)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
