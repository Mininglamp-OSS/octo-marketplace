package migration

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestPreflightChecksEverySourceTableBeforeDestinationPublication(t *testing.T) {
	for _, changed := range sourceTables {
		t.Run(changed.table, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rowHash, _ := sourceRowDigest(map[string][]byte{"plugin_id": []byte("source")})
			n := Node{SourceID: "source", SourceRowHash: rowHash, Plugin: model.Plugin{ID: "target"}, Evidence: map[string]SourceEvidence{"source": {RowHash: rowHash, Tables: emptySourceTables()}}}
			binding := DatabaseIdentity{Name: "test", ServerUUID: "server"}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(),@@server_uuid")).WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow("test", "server"))
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT COUNT.*information_schema.TABLES").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
			mock.ExpectQuery("SELECT \\* FROM plugin_category_placements ORDER BY placement_id FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"placement_id"}))
			mock.ExpectQuery("SELECT COUNT.*information_schema.TABLES.*marketplace_conversion_receipt").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
			mock.ExpectQuery(`SELECT \* FROM plugins WHERE plugin_id=\? FOR UPDATE`).WithArgs("source").WillReturnRows(sqlmock.NewRows([]string{"plugin_id"}).AddRow("source"))
			for _, spec := range sourceTables {
				rows := sqlmock.NewRows([]string{"metadata"})
				if spec.table == changed.table {
					rows.AddRow("drift")
				}
				query := "SELECT * FROM " + spec.table + " WHERE " + spec.owner + "=? ORDER BY " + spec.order + " FOR UPDATE"
				mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs("source").WillReturnRows(rows)
			}
			mock.ExpectRollback()
			if err := CheckPlanSources(context.Background(), db, Plan{CategorySnapshotCaptured: true, Nodes: []Node{n}}, binding); err == nil {
				t.Fatal("preflight accepted source inventory drift")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestApplyMySQLPreflightRejectsSourceIndexAndAuditDrift(t *testing.T) {
	for _, kind := range []string{"version refs", "index state", "audit"} {
		t.Run(kind, func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			in := seedPublicationInput(t, db, model.PluginTypeSkill, false, false)
			plan, err := Build(in, Policy{})
			if err != nil {
				t.Fatal(err)
			}
			var binding DatabaseIdentity
			if err = db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			if err = CheckPlanSources(ctx, db, plan, binding); err != nil {
				t.Fatal("unchanged source failed", err)
			}
			now := in.Resources[0].Plugin.UpdatedAt
			switch kind {
			case "version refs":
				_, err = db.Exec(`INSERT INTO plugin_version_relation_refs (source_plugin_id,source_version_id,relation_id,relation_type,target_plugin_id,target_version_id) VALUES ('source','old','changed','expert_connector','connector',NULL)`)
			case "index state":
				_, err = db.Exec(`INSERT INTO plugin_version_reference_index_state (source_plugin_id,source_version_id,relation_count,snapshot_digest,verified_at) VALUES ('source','old',0,REPEAT('a',64),?)`, now)
			case "audit":
				_, err = db.Exec(`INSERT INTO plugin_audit_logs (audit_log_id,plugin_id,action,operator_id,operator_name,request_id,created_at) VALUES ('changed','source','edit','owner','owner','request',?)`, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = CheckPlanSources(ctx, db, plan, binding); err == nil {
				t.Fatal("source drift passed preflight")
			}
			report, err := Apply(ctx, db, plan, binding)
			if err == nil || report.Applied != 0 {
				t.Fatalf("source drift applied: %v %#v", err, report)
			}
			var count int
			if err = db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE plugin_id<>'source'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("target created despite drift", err)
			}
		})
	}
}

func TestReferenceIndexTimestampDigestIsDriverIndependent(t *testing.T) {
	row := map[string][]byte{"verified_at": []byte("2026-10-28 00:00:00.123"), "relation_count": []byte("0")}
	hash, err := sourceRowDigest(row)
	if err != nil {
		t.Fatal(err)
	}
	row["verified_at"] = []byte("2026-10-28T00:00:00.123Z")
	same, err := sourceRowDigest(row)
	if err != nil || same != hash {
		t.Fatal("driver timestamp changed source evidence", err)
	}
}

// A matching persisted run checks its receipt instead of comparing an original
// source which this same run has already converted.
func TestPreflightRejectsStaleReceiptOnResume(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := Plan{CategorySnapshotCaptured: true, ID: "run", Nodes: []Node{{SourceID: "source", Plugin: model.Plugin{ID: "target"}}}}
	binding := DatabaseIdentity{Name: "test", ServerUUID: "server"}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(),@@server_uuid")).WillReturnRows(sqlmock.NewRows([]string{"db", "uuid"}).AddRow("test", "server"))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COUNT.*information_schema.TABLES").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery("SELECT run_id FROM marketplace_conversion_run").WillReturnRows(sqlmock.NewRows([]string{"run"}).AddRow("run"))
	expectMappingRunCheck(mock, p)
	mock.ExpectQuery("SELECT \\* FROM plugin_category_placements ORDER BY placement_id FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"placement_id"}))
	mock.ExpectQuery("SELECT COUNT.*information_schema.TABLES.*marketplace_conversion_receipt").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery("SELECT p.plugin_id,p.placement_code,p.category_id FROM marketplace_conversion_receipt").WithArgs("run").WillReturnRows(sqlmock.NewRows([]string{"plugin_id", "placement_code", "category_id"}))
	mock.ExpectQuery("SELECT node_hash FROM marketplace_conversion_receipt").WithArgs("run", "target").WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("stale"))
	mock.ExpectQuery("SELECT node_hash,state_hash FROM marketplace_conversion_receipt").WithArgs("run", "target").WillReturnRows(sqlmock.NewRows([]string{"hash", "state"}).AddRow("stale", "state"))
	mock.ExpectRollback()
	if err = CheckPlanSources(context.Background(), db, p, binding); err == nil {
		t.Fatal("stale receipt passed preflight")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyMySQLConnectorCloneDoesNotInheritExpertVersion(t *testing.T) {
	db := applyTestDB(t)
	ctx := context.Background()
	in := seedPublicationInput(t, db, model.PluginTypeExpert, false, true)
	if _, err := db.Exec(`UPDATE plugins SET current_version_id='old',current_version='1.0.0' WHERE plugin_id='source'`); err != nil {
		t.Fatal(err)
	}
	source := in.Resources[0].Plugin
	source.CurrentVersionID = stringPtrApply("old")
	source.CurrentVersion = stringPtrApply("1.0.0")
	target := source
	target.ID = "connector-clone"
	target.Type = model.PluginTypeConnector
	target.Name = "connector"
	target.CurrentVersionID = nil
	target.CurrentVersion = nil
	target.Manifest = []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"connector","plugin_type":"connector","name":"connector","description":"test","labels":[],"examples":[]}`)
	target.Package = []byte(`{"$schema":"cowork-plugin-package-2.0.json","connector":{"type":"mcp","source":"migration"},"attachments":[{"path":"mcp.json","content_type":"raw","raw_content":"{\"mcpServers\":{\"echo\":{\"command\":\"echo\"}}}"}]}`)
	rowHash := testSourceRowHash(t, db, "source")
	n := Node{SourceID: "source", Source: source, SourceRowHash: rowHash, SourcePublishedVersionID: stringPtrApply("old"), SourceRelease: &in.Releases[0], Plugin: target, Clone: true, Evidence: map[string]SourceEvidence{"source": testSourceEvidence(t, db, "source")}}
	p := Plan{CategorySnapshotCaptured: true, ID: "connector-run", SnapshotID: "snapshot", Nodes: []Node{n}}
	var binding DatabaseIdentity
	if err := db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
		t.Fatal(err)
	}
	if report, err := Apply(ctx, db, p, binding); err != nil || !report.Complete {
		t.Fatalf("connector apply: %v %#v", err, report)
	}
	var current, published, label sql.NullString
	if err := db.QueryRow(`SELECT current_version_id,published_version_id,current_version FROM plugins WHERE plugin_id='connector-clone'`).Scan(&current, &published, &label); err != nil {
		t.Fatal(err)
	}
	if current.Valid || published.Valid || label.Valid {
		t.Fatalf("connector inherited expert version: %#v %#v %#v", current, published, label)
	}
	if err := db.QueryRow(`SELECT current_version_id,published_version_id,current_version FROM plugins WHERE plugin_id='source'`).Scan(&current, &published, &label); err != nil {
		t.Fatal(err)
	}
	if current.String != "old" || published.String != "old" || label.String != "1.0.0" {
		t.Fatal("source version changed")
	}
}

func TestApplyMySQLSchemaDriftBlocksResume(t *testing.T) {
	for _, change := range []string{
		`ALTER TABLE rc_schema_probe ADD INDEX rc_added (value)`,
		`ALTER TABLE rc_schema_probe ADD UNIQUE KEY rc_unique (value)`,
		`ALTER TABLE rc_schema_probe DROP FOREIGN KEY rc_fk; ALTER TABLE rc_schema_probe ADD CONSTRAINT rc_fk FOREIGN KEY (parent_id) REFERENCES rc_schema_parent(id) ON DELETE SET NULL`,
		`ALTER TABLE rc_schema_probe DROP CHECK rc_check, ADD CONSTRAINT rc_check CHECK(value >= -1)`,
		`ALTER TABLE rc_schema_probe ALTER CHECK rc_check NOT ENFORCED`,
		`ALTER TABLE rc_schema_probe ALTER COLUMN value SET DEFAULT 0`,
		`ALTER TABLE rc_schema_probe DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	} {
		t.Run(change, func(t *testing.T) {
			db := applyTestDB(t)
			ctx := context.Background()
			in := seedPublicationInput(t, db, model.PluginTypeSkill, false, false)
			for _, query := range []string{
				`CREATE TABLE rc_schema_parent (id BIGINT PRIMARY KEY) ENGINE=InnoDB`,
				`CREATE TABLE rc_schema_probe (id BIGINT AUTO_INCREMENT PRIMARY KEY,parent_id BIGINT,value INT NULL DEFAULT NULL,CONSTRAINT rc_fk FOREIGN KEY(parent_id) REFERENCES rc_schema_parent(id),CONSTRAINT rc_check CHECK(value >= 0)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			} {
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			p, err := Build(in, Policy{})
			if err != nil {
				t.Fatal(err)
			}
			var binding DatabaseIdentity
			if err = db.QueryRow(`SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
				t.Fatal(err)
			}
			if _, err = Apply(ctx, db, p, binding); err != nil {
				t.Fatal(err)
			}
			before, err := applySchemaHash(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			// DML changes AUTO_INCREMENT and statistics, not the schema identity.
			if _, err = db.Exec(`INSERT INTO rc_schema_probe (value) VALUES (1),(2),(3)`); err != nil {
				t.Fatal(err)
			}
			if after, err := applySchemaHash(ctx, db); err != nil || after != before {
				t.Fatal("row writes changed schema identity", err)
			}
			if err = CheckPlanSources(ctx, db, p, binding); err != nil {
				t.Fatal("unchanged schema rejected", err)
			}
			for _, statement := range strings.Split(change, ";") {
				if _, err = db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if err = CheckPlanSources(ctx, db, p, binding); err == nil {
				t.Fatal("schema drift accepted by preflight")
			}
			if r, err := Apply(ctx, db, p, binding); err == nil || r.Applied != 0 {
				t.Fatal("schema drift resumed writes", err)
			}
			if r, err := Verify(ctx, db, p, binding); err == nil || r.Complete {
				t.Fatal("schema drift verified", err)
			}
		})
	}
}
