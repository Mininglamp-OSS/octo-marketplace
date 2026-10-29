package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	mysql "github.com/go-sql-driver/mysql"
)

// Exercise the window after the receipt digest has been read but before its
// transaction ends. Writers must not change any hashed inventory in that window,
// including by inserting into an inventory that was empty during verification.
func TestReceiptMySQLLocksStateUntilTransactionEnds(t *testing.T) {
	db, plan, binding := receiptLockFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	writes := []struct {
		name, query string
	}{
		{"target metadata", `UPDATE plugins SET plugin_name='changed' WHERE plugin_id='target'`},
		{"target delete", `DELETE FROM plugins WHERE plugin_id='target'`},
		{"target history", `UPDATE plugin_versions SET changelog='changed' WHERE version_id='version'`},
		{"target release", `UPDATE plugin_version_releases SET released_by='changed' WHERE version_id='version'`},
		{"retained source", `UPDATE plugins SET plugin_name='changed' WHERE plugin_id='source'`},
		{"retained history", `UPDATE plugin_versions SET changelog='changed' WHERE version_id='old-release'`},
		{"retained release", `UPDATE plugin_version_releases SET released_by='changed' WHERE version_id='old-release'`},
		{"target placement", `UPDATE plugin_placements SET sort_order=99 WHERE plugin_id='target'`},
		{"retained placement", `UPDATE plugin_placements SET sort_order=99 WHERE plugin_id='source'`},
		{"audit", `UPDATE plugin_audit_logs SET remark='changed' WHERE plugin_id='target'`},
		{"mapping", `UPDATE marketplace_conversion_mapping SET disposition='changed' WHERE run_id='run' AND target_id='target'`},
		{"receipt", `UPDATE marketplace_conversion_receipt SET node_hash=REPEAT('0',64) WHERE run_id='run' AND target_id='target'`},
		{"category", `UPDATE plugin_category_placements SET sort_order=99 WHERE placement_id='receipt-category'`},
		{"category delete", `DELETE FROM plugin_category_placements WHERE placement_id='receipt-category'`},
		{"reference index", `UPDATE plugin_version_reference_index_state SET relation_count=99 WHERE source_plugin_id='target'`},
		{"empty reviews insert", `INSERT INTO plugin_review_requests (review_id,plugin_id,space_id,status,kind,version,manifest_json,plugin_json,relations_json,manifest_hash,plugin_hash,applicant_uid,applicant_name,submitted_at,created_at,updated_at) VALUES ('concurrent-review','target','space','rejected','create','1.0.0','{}','{}','[]','sha256:m','sha256:p','owner','Owner',UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))`},
		{"empty relations insert", `INSERT INTO plugin_relations (relation_id,source_plugin_id,target_plugin_id,relation_type,created_by,created_at,updated_at) VALUES ('concurrent-relation','target','source','expert_skill','owner',UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))`},
		{"empty references insert", `INSERT INTO plugin_version_relation_refs VALUES ('target','version','concurrent-ref','expert_skill','source','old-release')`},
		{"retained empty reviews insert", `INSERT INTO plugin_review_requests (review_id,plugin_id,space_id,status,kind,version,manifest_json,plugin_json,relations_json,manifest_hash,plugin_hash,applicant_uid,applicant_name,submitted_at,created_at,updated_at) VALUES ('concurrent-review','source','space','rejected','create','1.0.0','{}','{}','[]','sha256:m','sha256:p','owner','Owner',UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))`},
	}
	for i, write := range writes {
		t.Run(write.name, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err = verifyReceipt(ctx, tx, plan, plan.Nodes[0]); err != nil {
				t.Fatal("verify receipt", err)
			}
			writer, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if _, err = writer.ExecContext(ctx, `SET SESSION innodb_lock_wait_timeout=1`); err != nil {
				t.Fatal(err)
			}
			writeTx, err := writer.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer writeTx.Rollback()
			_, err = writeTx.ExecContext(ctx, write.query)
			var mysqlErr *mysql.MySQLError
			if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1205 {
				t.Fatalf("writer was not blocked until verification ended: %v", err)
			}
			if err = writeTx.Rollback(); err != nil {
				t.Fatal(err)
			}
			// Both successful verification and a failed/preflight transaction
			// release their locks. Retrying the same write must then succeed.
			if i%2 == 0 {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			writeTx, err = writer.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer writeTx.Rollback()
			result, err := writeTx.ExecContext(ctx, write.query)
			if err != nil {
				t.Fatal("write did not unblock after transaction ended", err)
			}
			if changed, err := result.RowsAffected(); err != nil || changed == 0 {
				t.Fatalf("write did not exercise a row: %d %v", changed, err)
			}
			// Keep each case on the exact same committed migration baseline.
			if err = writeTx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := Verify(ctx, db, plan, binding); err != nil {
		t.Fatal("unchanged baseline no longer verifies", err)
	}
}

// REPEATABLE READ may already have a snapshot before receipt verification.
// Verify against current locked rows, rather than certifying a pre-update view.
func TestReceiptMySQLRejectsDriftAfterReadView(t *testing.T) {
	db, plan, _ := receiptLockFixture(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var name string
	if err = tx.QueryRowContext(ctx, `SELECT plugin_name FROM plugins WHERE plugin_id='target'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE plugins SET plugin_name='changed after snapshot' WHERE plugin_id='target'`); err != nil {
		t.Fatal(err)
	}
	if err = verifyReceipt(ctx, tx, plan, plan.Nodes[0]); err == nil {
		t.Fatal("stale read view certified a changed target")
	}
}

func receiptLockFixture(t *testing.T) (*sql.DB, Plan, DatabaseIdentity) {
	t.Helper()
	db := applyTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	source := model.Plugin{ID: "source", Name: "source", Type: model.PluginTypeSkill, OwnerUID: "owner", Visibility: model.PluginVisibilitySystem, ListingState: model.PluginListingStatePublished, Manifest: json.RawMessage(`{"name":"source"}`), Package: json.RawMessage(`{"body":"original"}`), ManifestHash: "sha256:manifest", PluginHash: "sha256:plugin", Status: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := db.Exec(`INSERT INTO plugins (plugin_id,plugin_name,plugin_type,tags_json,owner_uid,visibility,listing_state,manifest_json,plugin_json,manifest_hash,plugin_hash,status,created_at,updated_at) VALUES (?,?,?,'[]',?,?,?,?,?,?,?,?,?,?)`, source.ID, source.Name, source.Type, source.OwnerUID, source.Visibility, source.ListingState, string(source.Manifest), string(source.Package), source.ManifestHash, source.PluginHash, 1, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_versions (version_id,plugin_id,version,manifest_json,plugin_json,manifest_hash,plugin_hash,relations_json,created_by,created_at) VALUES ('old-release','source','1',?,?,?,?,'[]','owner',?)`, string(source.Manifest), string(source.Package), source.ManifestHash, source.PluginHash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_version_releases VALUES ('source','old-release','0.1.0','owner',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugins SET current_version_id='old-release',published_version_id='old-release' WHERE plugin_id='source'`); err != nil {
		t.Fatal(err)
	}
	source.CurrentVersionID = stringPtrApply("old-release")
	if _, err := db.Exec(`INSERT INTO plugin_category_placements (placement_id,placement_code,plugin_type,category_id,visible,sort_order,created_at,updated_at) VALUES ('receipt-category','research','skill','category',1,7,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_placements (placement_id,placement_code,plugin_id,category_id,visible,sort_order,created_at,updated_at) VALUES ('source-placement','research','source','category',1,7,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	target := source
	target.ID = "target"
	n := Node{Evidence: map[string]SourceEvidence{source.ID: testSourceEvidence(t, db, source.ID)}, SourceRowHash: testSourceRowHash(t, db, source.ID), SourceID: source.ID, Source: source, SourcePublishedVersionID: stringPtrApply("old-release"), SourceRelease: &model.PluginVersionRelease{PluginID: "source", VersionID: "old-release", VersionLabel: "0.1.0", ReleasedBy: "owner", ReleasedAt: now}, Plugin: target, VersionID: "version", VersionLabel: "1.0.0", Clone: true, Release: true}
	categories, err := captureCategorySnapshot(ctx, db, false)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{CategorySnapshotCaptured: true, CategorySnapshot: categories, ID: "run", SnapshotID: "snapshot", Nodes: []Node{n}, Mappings: []Mapping{{SourceID: "source", TargetID: "target", VersionID: "version", Disposition: "canonical"}}}
	var binding DatabaseIdentity
	if err = db.QueryRowContext(ctx, `SELECT DATABASE(),@@server_uuid`).Scan(&binding.Name, &binding.ServerUUID); err != nil {
		t.Fatal(err)
	}
	if report, err := Apply(ctx, db, plan, binding); err != nil || !report.Complete {
		t.Fatalf("apply fixture: %v %+v", err, report)
	}
	return db, plan, binding
}
