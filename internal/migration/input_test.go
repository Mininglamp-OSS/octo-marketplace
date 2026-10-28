package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSourceRowPreservesStateAndNulls(t *testing.T) {
	row := map[string][]byte{"plugin_id": []byte("skill-1"), "plugin_type": []byte("skill"), "plugin_name": []byte("x"), "category_id": []byte("category-a"), "icon": []byte("icon-key"), "visibility": []byte("private"), "owner_uid": []byte("owner"), "space_id": []byte("space-a"), "status": []byte("0"), "is_embedded": []byte("1"), "rating": []byte("4"), "updated_at": []byte("2026-10-28 12:34:56.123"), "created_at": []byte("2026-10-27 00:00:00"), "manifest_json": []byte(`{}`), "plugin_json": []byte(`{}`), "tags_json": []byte(`[]`), "listing_state": []byte("delisted")}
	r, e := sourceResource(row)
	if e != nil {
		t.Fatal(e)
	}
	if r.Plugin.Rating == nil || *r.Plugin.Rating != 4 || r.Plugin.CategoryID == nil || *r.Plugin.CategoryID != "category-a" || r.Plugin.Icon != "icon-key" || r.Plugin.Status != 0 || !r.Plugin.IsEmbedded || r.Plugin.DeletedAt != nil || r.Plugin.SpaceID == nil || *r.Plugin.SpaceID != "space-a" || r.Plugin.UpdatedAt != time.Date(2026, 10, 28, 12, 34, 56, 123000000, time.UTC) {
		t.Fatalf("incorrect source %v", r.Plugin)
	}
	row["updated_at"] = []byte("not a date")
	if _, e = sourceResource(row); e == nil {
		t.Fatal("invalid timestamp accepted")
	}
}

// sqlmock itself ignores transaction options, so record the actual driver call.
type inputConnector struct {
	driver driver.Driver
	dsn    string
	opts   *driver.TxOptions
}

func (c inputConnector) Driver() driver.Driver { return c.driver }
func (c inputConnector) Connect(context.Context) (driver.Conn, error) {
	v, e := c.driver.Open(c.dsn)
	if e != nil {
		return nil, e
	}
	return inputConn{v, c.opts}, nil
}

type inputConn struct {
	driver.Conn
	opts *driver.TxOptions
}

func (c inputConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	*c.opts = o
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
}
func (c inputConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}
func inputMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *driver.TxOptions) {
	t.Helper()
	dsn := fmt.Sprintf("input-%s", t.Name())
	base, m, e := sqlmock.NewWithDSN(dsn)
	if e != nil {
		t.Fatal(e)
	}
	opts := new(driver.TxOptions)
	db := sql.OpenDB(inputConnector{base.Driver(), dsn, opts})
	t.Cleanup(func() { db.Close(); base.Close() })
	return db, m, opts
}
func expectInputSchema(m sqlmock.Sqlmock) {
	for _, s := range databaseSchema {
		m.ExpectQuery(regexp.QuoteMeta("SELECT " + s.columns + " FROM " + s.table + " LIMIT 0")).WillReturnRows(sqlmock.NewRows([]string{"unused"}))
	}
}
func expectInputData(m sqlmock.Sqlmock, badDate bool) {
	stamp := "2026-10-28 01:02:03.004"
	if badDate {
		stamp = "broken"
	}
	m.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugins ORDER BY plugin_id")).WillReturnRows(sqlmock.NewRows([]string{"plugin_id", "plugin_name", "plugin_type", "owner_uid", "space_id", "visibility", "listing_state", "status", "is_embedded", "plugin_json", "manifest_json", "tags_json", "created_at", "updated_at", "deleted_at", "published_version_id"}).AddRow("p", "name", "skill", "owner", nil, "private", "delisted", 0, 1, `{"attachments":[{"path":"SKILL.md","content_type":"storage","storage_uri":"skills/p/SKILL.md"}]}`, `{}`, `[]`, stamp, stamp, nil, "v"))
	if badDate {
		return
	}
	m.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugin_relations ORDER BY relation_id")).WillReturnRows(sqlmock.NewRows([]string{"relation_id", "source_plugin_id", "target_plugin_id", "target_version_id", "relation_type", "status", "created_at", "updated_at"}).AddRow("r", "p", "p", nil, "expert_connector", 0, stamp, stamp))
	m.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugin_versions ORDER BY version_id")).WillReturnRows(sqlmock.NewRows([]string{"version_id", "plugin_id", "version", "declared_version", "plugin_json", "manifest_json", "relations_json", "artifacts_json", "created_at", "created_by", "snapshot_schema_version"}).AddRow("v", "p", "1", "1.2.3", `{"historical":"not downloaded"}`, `{}`, `[]`, nil, stamp, "owner", 1))
	m.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugin_version_releases ORDER BY plugin_id,version_id")).WillReturnRows(sqlmock.NewRows([]string{"plugin_id", "version_id", "version_label", "released_by", "released_at"}).AddRow("p", "v", "1.2.3", "owner", stamp))
	m.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugin_review_requests ORDER BY review_id")).WillReturnRows(sqlmock.NewRows([]string{"review_id", "plugin_id", "status", "deleted_at"}).AddRow("rq", "p", "pending", nil))
}
func TestInputFromDatabaseReadOnlyAndPreservesMetadata(t *testing.T) {
	db, m, opts := inputMock(t)
	m.ExpectBegin()
	expectInputSchema(m)
	expectInputData(m, false)
	m.ExpectCommit()
	calls := 0
	in, issues, e := InputFromDatabase(context.Background(), db, func(_ context.Context, key string) ([]byte, error) {
		calls++
		if key != "skills/p/SKILL.md" {
			t.Fatal(key)
		}
		return []byte("skill body"), nil
	})
	if e != nil || len(issues) != 0 {
		t.Fatalf("%v %v", e, issues)
	}
	if !opts.ReadOnly || sql.IsolationLevel(opts.Isolation) != sql.LevelRepeatableRead {
		t.Fatalf("transaction options: %+v", opts)
	}
	r := in.Resources[0]
	if r.Plugin.OwnerUID != "owner" || r.Plugin.SpaceID != nil || r.Plugin.Visibility != "private" || r.Plugin.ListingState != "delisted" || r.Plugin.Status != 0 || !r.Plugin.IsEmbedded || !r.PendingReview || r.PublishedVersionID == nil || *r.PublishedVersionID != "v" || string(r.Files[0].Data) != "skill body" || r.Relations[0].Status != 0 || r.Relations[0].TargetVersionID != nil {
		t.Fatalf("lost state: %+v", r)
	}
	if calls != 2 || in.Versions[0].DeclaredVersion == nil || in.Versions[0].CreatedBy != "owner" || in.Releases[0].ReleasedBy != "owner" {
		t.Fatalf("history or artifacts lost: %+v", in)
	}
	if len(in.SnapshotID) != 64 {
		t.Fatalf("invalid input ID %q", in.SnapshotID)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestInputDatabaseIdentityIncludesResolvedBytes(t *testing.T) {
	ids := []string{}
	for i, body := range []string{"same", "same", "different"} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			db, m, _ := inputMock(t)
			m.ExpectBegin()
			expectInputSchema(m)
			expectInputData(m, false)
			m.ExpectCommit()
			in, _, e := InputFromDatabase(context.Background(), db, func(context.Context, string) ([]byte, error) { return []byte(body), nil })
			if e != nil {
				t.Fatal(e)
			}
			ids = append(ids, in.SnapshotID)
			if e = m.ExpectationsWereMet(); e != nil {
				t.Fatal(e)
			}
		})
	}
	if len(ids) != 3 {
		t.Fatal("input reads failed")
	}
	if ids[0] != ids[1] || ids[0] == ids[2] {
		t.Fatalf("identity does not bind bytes: %v", ids)
	}
}
func TestInputDatabaseRollsBackParseFailure(t *testing.T) {
	db, m, _ := inputMock(t)
	m.ExpectBegin()
	expectInputSchema(m)
	expectInputData(m, true)
	m.ExpectRollback()
	_, _, e := InputFromDatabase(context.Background(), db, nil)
	if e == nil {
		t.Fatal("accepted bad date")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestInputDatabaseSchemaFailureBeforeDataRead(t *testing.T) {
	db, m, _ := inputMock(t)
	m.ExpectBegin()
	m.ExpectQuery("SELECT .* FROM plugins LIMIT 0").WillReturnError(errors.New("missing column"))
	m.ExpectRollback()
	_, _, e := InputFromDatabase(context.Background(), db, nil)
	if e == nil {
		t.Fatal("accepted incomplete schema")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestCheckDatabaseSchemaReadOnly(t *testing.T) {
	db, m, _ := inputMock(t)
	expectInputSchema(m)
	if e := CheckDatabaseSchema(context.Background(), db); e != nil {
		t.Fatal(e)
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestInputRowsSQLNullAndCopy(t *testing.T) {
	db, m, _ := inputMock(t)
	m.ExpectQuery("SELECT test").WillReturnRows(sqlmock.NewRows([]string{"id", "optional", "empty"}).AddRow("first", nil, []byte{}).AddRow("second", nil, []byte{}))
	var got []map[string][]byte
	e := readDatabaseRows(context.Background(), db, "SELECT test", func(row map[string][]byte) error { got = append(got, row); return nil })
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(got[0]["id"], []byte("first")) || got[0]["optional"] != nil || got[0]["empty"] == nil {
		t.Fatalf("SQL values changed: %#v", got)
	}
}
func TestInputRowsIterationError(t *testing.T) {
	db, m, _ := inputMock(t)
	m.ExpectQuery("SELECT test").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("first").RowError(0, io.ErrUnexpectedEOF))
	if e := readDatabaseRows(context.Background(), db, "SELECT test", func(map[string][]byte) error { return nil }); e == nil {
		t.Fatal("ignored rows error")
	}
}

func TestDatabaseSchemaRejectsMissingVersionReferenceTables(t *testing.T) {
	for i := 2; i < len(databaseSchema); i++ {
		t.Run(databaseSchema[i].table, func(t *testing.T) {
			db, m, _ := inputMock(t)
			for j, s := range databaseSchema {
				q := m.ExpectQuery(regexp.QuoteMeta("SELECT " + s.columns + " FROM " + s.table + " LIMIT 0"))
				if j == i {
					q.WillReturnError(errors.New("missing schema"))
					break
				}
				q.WillReturnRows(sqlmock.NewRows([]string{"unused"}))
			}
			if e := CheckDatabaseSchema(context.Background(), db); e == nil {
				t.Fatal("accepted missing version/reference schema")
			}
			if e := m.ExpectationsWereMet(); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestDatabaseIdentityNormalizesJSONAndPreservesNumbers(t *testing.T) {
	a := Input{Resources: []Resource{{Files: []File{{Path: "a", MIME: "text/plain", Data: []byte("a")}}}}}
	a.Resources[0].Plugin.Package = []byte(`{"z":9007199254740993,"a":1}`)
	one, e := databaseInputID(a)
	if e != nil {
		t.Fatal(e)
	}
	a.Resources[0].Plugin.Package = []byte(`{ "a": 1, "z": 9007199254740993 }`)
	two, e := databaseInputID(a)
	if e != nil {
		t.Fatal(e)
	}
	if one != two {
		t.Fatal("JSON layout affects identity")
	}
	a.Resources[0].Plugin.Package = []byte(`{"a":1,"z":9007199254740992}`)
	three, e := databaseInputID(a)
	if e != nil {
		t.Fatal(e)
	}
	if one == three {
		t.Fatal("JSON number precision lost")
	}
	a.Resources[0].Plugin.Package = []byte(`broken`)
	if _, e = databaseInputID(a); e == nil {
		t.Fatal("accepted malformed source JSON")
	}
}
func TestInputSourceRejectsBadInteger(t *testing.T) {
	for _, field := range []string{"status", "tool_count", "rating"} {
		if _, e := sourceResource(map[string][]byte{field: []byte("broken")}); e == nil {
			t.Fatalf("accepted invalid %s", field)
		}
	}
}

func TestInputDatabaseResolvesUndeletedResourcesOnly(t *testing.T) {
	db, m, _ := inputMock(t)
	m.ExpectBegin()
	expectInputSchema(m)
	m.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugins ORDER BY plugin_id")).WillReturnRows(sqlmock.NewRows([]string{"plugin_id", "plugin_type", "plugin_json", "deleted_at"}).
		AddRow("deleted", "skill", `{"attachments":[{"path":"SKILL.md","content_type":"storage","storage_uri":"skills/deleted/SKILL.md"}]}`, "2026-10-28 00:00:00").
		AddRow("invalid", "skill", `{"attachments":[]}`, nil).
		AddRow("live", "skill", `{"attachments":[{"path":"SKILL.md","content_type":"raw","raw_content":"body"}]}`, nil))
	m.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugin_relations ORDER BY relation_id")).WillReturnRows(sqlmock.NewRows([]string{"relation_id", "source_plugin_id"}))
	m.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugin_versions ORDER BY version_id")).WillReturnRows(sqlmock.NewRows([]string{"version_id"}))
	m.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugin_version_releases ORDER BY plugin_id,version_id")).WillReturnRows(sqlmock.NewRows([]string{"version_id"}))
	m.ExpectQuery(regexp.QuoteMeta("SELECT * FROM plugin_review_requests ORDER BY review_id")).WillReturnRows(sqlmock.NewRows([]string{"review_id", "plugin_id", "status", "deleted_at"}).AddRow("r1", "live", "pending", "2026-10-28 00:00:00").AddRow("r2", "live", "approved", nil))
	m.ExpectCommit()
	in, issues, e := InputFromDatabase(context.Background(), db, func(context.Context, string) ([]byte, error) { t.Fatal("deleted object fetched"); return nil, nil })
	if e != nil {
		t.Fatal(e)
	}
	if len(issues) != 1 || issues[0].ResourceID != "invalid" || issues[0].Code != "invalid_package_attachments" {
		t.Fatalf("unexpected issues: %v", issues)
	}
	if len(in.Resources[0].Files) != 0 || len(in.Resources[2].Files) != 1 || in.Resources[2].PendingReview {
		t.Fatalf("incorrect resolution/review state: %+v", in.Resources)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
