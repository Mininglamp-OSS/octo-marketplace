package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/migration/artifacts"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// ApplyReport describes database conversion only; it never certifies installation.
type ApplyReport struct {
	ObjectInventory       []ObjectInventoryEntry `json:"object_inventory,omitempty"`
	CommitState           string                 `json:"commit_state,omitempty"`
	CommittedIDs          []string               `json:"committed_ids,omitempty"`
	FailedResourceID      string                 `json:"failed_resource_id,omitempty"`
	Recovery              string                 `json:"recovery,omitempty"`
	RunID                 string                 `json:"run_id"`
	Complete              bool                   `json:"complete"`
	Applied, Verified     int
	Issues                []Issue  `json:"issues,omitempty"`
	InstallVerified       bool     `json:"install_verified"`
	HistoricalIdentityIDs []string `json:"historical_identity_ids,omitempty"`
}

var errApply = errors.New("migration persistence verification failed")

type applyQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func guardApply(ctx context.Context, db *sql.DB, expected DatabaseIdentity) error {
	if db == nil || expected.Name == "" || expected.ServerUUID == "" {
		return errors.New("migration database binding is required")
	}
	var actual DatabaseIdentity
	if e := db.QueryRowContext(ctx, "SELECT DATABASE(),@@server_uuid").Scan(&actual.Name, &actual.ServerUUID); e != nil {
		return errors.New("migration database binding could not be verified")
	}
	if actual != expected {
		return errors.New("migration database binding mismatch")
	}
	return nil
}
func applyIdentity(p Plan) string {
	b, _ := json.Marshal(struct {
		Categories any
		Plan       Plan
		Nodes      []any
	}{[]any{p.CategorySnapshotCaptured, p.CategorySnapshot}, p, func() []any {
		a := []any{}
		for _, n := range p.Nodes {
			a = append(a, []any{n.Source, n.SourceRowHash, n.Evidence, n.SourcePublishedVersionID, n.SourceRelease, n.Plugin, n.Relations, n.Artifacts, n.Files, n.IconFile})
		}
		return a
	}()})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func setupApply(ctx context.Context, db *sql.DB, p Plan) error {
	for _, q := range []string{`CREATE TABLE IF NOT EXISTS marketplace_conversion_run (slot TINYINT PRIMARY KEY,run_id VARCHAR(64) NOT NULL,identity_hash CHAR(64) NOT NULL,schema_hash CHAR(64) NOT NULL) ENGINE=InnoDB`, `CREATE TABLE IF NOT EXISTS marketplace_conversion_receipt (run_id VARCHAR(64) NOT NULL,target_id VARCHAR(64) NOT NULL,node_hash CHAR(64) NOT NULL,state_hash CHAR(64) NOT NULL,PRIMARY KEY(run_id,target_id)) ENGINE=InnoDB`, `CREATE TABLE IF NOT EXISTS marketplace_conversion_mapping (run_id VARCHAR(64) NOT NULL,source_id VARCHAR(64) NOT NULL,target_id VARCHAR(64) NOT NULL,version_id VARCHAR(64) NOT NULL,disposition VARCHAR(64) NOT NULL,PRIMARY KEY(run_id,source_id,target_id)) ENGINE=InnoDB`} {
		if _, e := db.ExecContext(ctx, q); e != nil {
			return errApply
		}
	}
	schema, e := applySchemaHash(ctx, db)
	if e != nil {
		return errApply
	}
	if _, e := db.ExecContext(ctx, `INSERT IGNORE INTO marketplace_conversion_run VALUES (1,?,?,?)`, p.ID, applyIdentity(p), schema); e != nil {
		return errApply
	}
	if e := checkRun(ctx, db, p); e != nil {
		return e
	}
	for _, m := range p.Mappings {
		if m.TargetID != "" {
			continue
		}
		if _, e := db.ExecContext(ctx, `INSERT IGNORE INTO marketplace_conversion_mapping VALUES (?,?,?,?,?)`, p.ID, m.SourceID, m.TargetID, m.VersionID, m.Disposition); e != nil {
			return errApply
		}
	}
	return nil
}
func checkRun(ctx context.Context, q applyQuery, p Plan) error {
	var id, h, schema string
	actual, e := applySchemaHash(ctx, q)
	if e != nil {
		return errApply
	}
	if e := q.QueryRowContext(ctx, `SELECT run_id,identity_hash,schema_hash FROM marketplace_conversion_run WHERE slot=1`).Scan(&id, &h, &schema); e != nil || id != p.ID || h != applyIdentity(p) || schema != actual {
		return errors.New("migration run identity conflict")
	}
	return nil
}
func Apply(ctx context.Context, db *sql.DB, p Plan, expected DatabaseIdentity, readers ...artifacts.Reader) (ApplyReport, error) {
	p.Issues = append(p.Issues, validateMigrationReleases(p)...)
	p.Issues = append(p.Issues, validateFinalContracts(p)...)
	r := ApplyReport{RunID: p.ID, Issues: p.Issues}
	if len(p.Issues) > 0 {
		return r, ErrIssues
	}
	if p.ID == "" || p.SnapshotID == "" {
		return r, errApply
	}
	if e := guardApply(ctx, db, expected); e != nil {
		return r, e
	}
	if !p.CategorySnapshotCaptured {
		return r, errApply
	}
	if e := setupApply(ctx, db, p); e != nil {
		return r, e
	}
	for _, n := range p.Nodes {
		if e := applyNode(ctx, db, p, n, readers...); e != nil {
			r.Issues = append(r.Issues, Issue{ResourceID: n.Plugin.ID, Code: "persistence_conflict"})
			r.FailedResourceID = n.Plugin.ID
			describePartialApply(ctx, db, p, &r)
			return r, errApply
		}
		r.Applied++
	}
	verified, err := Verify(ctx, db, p, expected)
	verified.Applied = r.Applied
	if err != nil {
		describePartialApply(ctx, db, p, &verified)
	}
	return verified, err
}
func Verify(ctx context.Context, db *sql.DB, p Plan, expected DatabaseIdentity) (ApplyReport, error) {
	p.Issues = append(p.Issues, validateMigrationReleases(p)...)
	p.Issues = append(p.Issues, validateFinalContracts(p)...)
	r := ApplyReport{RunID: p.ID, Issues: p.Issues}
	if len(p.Issues) > 0 {
		return r, ErrIssues
	}
	if e := guardApply(ctx, db, expected); e != nil {
		return r, e
	}
	if !p.CategorySnapshotCaptured {
		return r, errApply
	}
	if e := checkRun(ctx, db, p); e != nil {
		return r, e
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return r, errApply
	}
	defer tx.Rollback()
	if err = verifyCategorySnapshot(ctx, tx, p); err != nil {
		return r, errApply
	}
	for _, n := range p.Nodes {
		if e := verifyMigrationRelease(ctx, tx, n); e != nil {
			r.Issues = append(r.Issues, Issue{ResourceID: n.Plugin.ID, Code: "release_contract_mismatch"})
			return r, errApply
		}
		if e := verifyReceipt(ctx, tx, p, n); e != nil {
			r.Issues = append(r.Issues, Issue{ResourceID: n.Plugin.ID, Code: "persisted_state_changed"})
			return r, errApply
		}
		r.Verified++
	}

	rows, e := tx.QueryContext(ctx, `SELECT source_id,target_id,version_id,disposition FROM marketplace_conversion_mapping WHERE run_id=? ORDER BY source_id,target_id`, p.ID)
	if e != nil {
		return r, errApply
	}
	actual := []Mapping{}
	for rows.Next() {
		var m Mapping
		if e = rows.Scan(&m.SourceID, &m.TargetID, &m.VersionID, &m.Disposition); e != nil {
			rows.Close()
			return r, errApply
		}
		actual = append(actual, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return r, errApply
	}
	expectedMappings := append([]Mapping{}, p.Mappings...)
	less := func(a, b Mapping) bool {
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		return a.TargetID < b.TargetID
	}
	sort.Slice(actual, func(i, j int) bool { return less(actual[i], actual[j]) })
	sort.Slice(expectedMappings, func(i, j int) bool { return less(expectedMappings[i], expectedMappings[j]) })
	aa, _ := json.Marshal(actual)
	bb, _ := json.Marshal(expectedMappings)
	if string(aa) != string(bb) {
		return r, errApply
	}
	for _, m := range p.Mappings {
		if m.TargetID != "" && m.SourceID != m.TargetID && !plannedTarget(p, m.SourceID) {
			r.HistoricalIdentityIDs = append(r.HistoricalIdentityIDs, m.SourceID)
		}
	}
	sort.Strings(r.HistoricalIdentityIDs)
	if err = tx.Commit(); err != nil {
		return r, errApply
	}
	r.Complete = true
	r.CommitState = "complete"
	return r, nil
}
func nodeHash(n Node) string {
	b, _ := json.Marshal([]any{n.SourceID, n.Source, n.SourceRowHash, n.Evidence, n.SourcePublishedVersionID, n.SourceRelease, n.Plugin, n.Relations, n.VersionID, n.VersionLabel, n.Artifacts, n.Release, n.Clone, n.Files, n.IconFile})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func verifyReceipt(ctx context.Context, q applyQuery, p Plan, n Node) error {
	var nh, sh string
	if e := q.QueryRowContext(ctx, `SELECT node_hash,state_hash FROM marketplace_conversion_receipt WHERE run_id=? AND target_id=?`, p.ID, n.Plugin.ID).Scan(&nh, &sh); e != nil {
		return errApply
	}
	if nh != nodeHash(n) {
		return errApply
	}
	actual, e := stateHash(ctx, q, p, n)
	if e != nil || nh != nodeHash(n) || sh != actual {
		return errApply
	}
	return nil
}

// Hash every column of owned rows, including immutable history, metadata, live
// edges and provenance. This catches edits that retain a stale plugin_hash.
func stateHash(ctx context.Context, q applyQuery, p Plan, n Node) (string, error) {
	h := sha256.New()
	queries := []struct {
		q string
		a []any
	}{
		{`SELECT * FROM plugins WHERE plugin_id=? ORDER BY plugin_id`, []any{n.Plugin.ID}},
		{`SELECT * FROM plugin_relations WHERE source_plugin_id=? ORDER BY relation_id`, []any{n.Plugin.ID}},
		{`SELECT * FROM plugin_versions WHERE plugin_id=? ORDER BY version_id`, []any{n.Plugin.ID}},
		{`SELECT * FROM plugin_version_releases WHERE plugin_id=? ORDER BY version_id`, []any{n.Plugin.ID}},
		{`SELECT * FROM plugin_version_relation_refs WHERE source_plugin_id=? ORDER BY source_version_id,relation_id`, []any{n.Plugin.ID}},
		{`SELECT * FROM plugin_version_reference_index_state WHERE source_plugin_id=? ORDER BY source_version_id`, []any{n.Plugin.ID}},
		{`SELECT * FROM plugin_audit_logs WHERE plugin_id=? ORDER BY audit_log_id`, []any{n.Plugin.ID}},
		{`SELECT * FROM plugin_review_requests WHERE plugin_id=? ORDER BY review_id`, []any{n.Plugin.ID}},
		{`SELECT * FROM plugin_placements WHERE plugin_id=? ORDER BY placement_id`, []any{n.Plugin.ID}},
		// Also bind run-created shared registrations absent from CategorySnapshot.
		// verifyCategorySnapshot relies on this full-row receipt digest on resume.
		{`SELECT c.* FROM plugin_category_placements c WHERE c.plugin_type=? AND EXISTS (SELECT 1 FROM plugin_placements p WHERE p.plugin_id=? AND p.placement_code=c.placement_code AND p.category_id=c.category_id) ORDER BY c.placement_id`, []any{n.Plugin.Type, n.Plugin.ID}},
		{`SELECT * FROM marketplace_conversion_mapping WHERE run_id=? AND target_id=? ORDER BY source_id,target_id`, []any{p.ID, n.Plugin.ID}},
	}
	for _, m := range p.Mappings {
		if m.TargetID == n.Plugin.ID && m.SourceID != m.TargetID && !plannedTarget(p, m.SourceID) {
			queries = append(queries, struct {
				q string
				a []any
			}{`SELECT * FROM plugins WHERE plugin_id=? ORDER BY plugin_id`, []any{m.SourceID}}, struct {
				q string
				a []any
			}{`SELECT * FROM plugin_placements WHERE plugin_id=? ORDER BY placement_id`, []any{m.SourceID}})
			// The migrated target cannot certify itself while its retained
			// original history or relationships have changed underneath it.
			for _, spec := range sourceTables {
				if spec.table == "plugin_placements" {
					continue
				}
				queries = append(queries, struct {
					q string
					a []any
				}{"SELECT * FROM " + spec.table + " WHERE " + spec.owner + "=? ORDER BY " + spec.order, []any{m.SourceID}})
			}
		}
	}
	for _, x := range queries {
		rows, e := q.QueryContext(ctx, x.q, x.a...)
		if e != nil {
			return "", e
		}
		cols, e := rows.Columns()
		if e != nil {
			rows.Close()
			return "", e
		}
		fmt.Fprintf(h, "%s", x.q)
		for rows.Next() {
			v := make([]sql.RawBytes, len(cols))
			a := make([]any, len(cols))
			for i := range v {
				a[i] = &v[i]
			}
			if e = rows.Scan(a...); e != nil {
				rows.Close()
				return "", e
			}
			for i, b := range v {
				fmt.Fprintf(h, "%s:%t:%d:", cols[i], b == nil, len(b))
				h.Write(b)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return "", e
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func jvalue(b json.RawMessage) any {
	if len(b) == 0 || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	return string(b)
}
func applyNode(ctx context.Context, db *sql.DB, p Plan, n Node, readers ...artifacts.Reader) (err error) {
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// Lock dependencies in a stable order; FK checks alone cannot freeze edits.
	deps := []string{}
	for _, edge := range n.Relations {
		deps = append(deps, edge.TargetPluginID)
	}
	sort.Strings(deps)
	for _, id := range deps {
		var locked string
		if e = tx.QueryRowContext(ctx, `SELECT plugin_id FROM plugins WHERE plugin_id=? FOR UPDATE`, id).Scan(&locked); e != nil {
			return e
		}
	}
	var saved string
	e = tx.QueryRowContext(ctx, `SELECT node_hash FROM marketplace_conversion_receipt WHERE run_id=? AND target_id=? FOR UPDATE`, p.ID, n.Plugin.ID).Scan(&saved)
	if e == nil {
		if e = verifyCategorySnapshot(ctx, tx, p); e != nil {
			return e
		}
		if e = verifyReceipt(ctx, tx, p, n); e != nil {
			return e
		}
		return tx.Commit()
	}
	if e != sql.ErrNoRows {
		return e
	}
	if e = verifyCategorySnapshot(ctx, tx, p); e != nil {
		return e
	}
	if e = verifySourceRows(ctx, tx, p, n); e != nil {
		return e
	}
	s := n.Source

	var reader artifacts.Reader
	if len(readers) > 1 {
		return errApply
	}
	if len(readers) == 1 {
		reader = readers[0]
	}
	if e = VerifySourceObjects(ctx, n.Evidence, reader); e != nil {
		return e
	}
	if n.Plugin.ID != n.SourceID {
		if e = cloneRow(ctx, tx, n); e != nil {
			return e
		}
	}
	a := n.Plugin
	_, e = tx.ExecContext(ctx, `UPDATE plugins SET plugin_name=?,plugin_type=?,owner_uid=?,space_id=?,visibility=?,listing_state=?,status=?,is_embedded=?,manifest_json=?,plugin_json=?,attachment_keys_json=?,manifest_hash=?,plugin_hash=?,updated_at=? WHERE plugin_id=?`, a.Name, a.Type, a.OwnerUID, a.SpaceID, a.Visibility, a.ListingState, a.Status, a.IsEmbedded, string(a.Manifest), string(a.Package), jvalue(a.AttachmentKeys), a.ManifestHash, a.PluginHash, a.UpdatedAt, a.ID)
	if e != nil {
		return e
	}
	// Old live relations become historical rows; immutable snapshots are untouched.
	if _, e = tx.ExecContext(ctx, `UPDATE plugin_relations SET status=0,deleted_at=UTC_TIMESTAMP(3) WHERE source_plugin_id=? AND deleted_at IS NULL`, a.ID); e != nil {
		return e
	}
	for _, r := range n.Relations {
		if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_relations (relation_id,source_plugin_id,target_plugin_id,target_version_id,relation_type,sort_order,relation_json,status,created_by,created_at,updated_at,deleted_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, r.ID, a.ID, r.TargetPluginID, r.TargetVersionID, r.Type, r.SortOrder, jvalue(r.Data), r.Status, r.CreatedBy, r.CreatedAt, r.UpdatedAt, r.DeletedAt); e != nil {
			return e
		}
	}
	if a.Type != model.PluginTypeConnector {
		if e = writeMigrationVersion(ctx, tx, n); e != nil {
			return e
		}
	}
	audit := applyStableID(p.ID + ":audit:" + a.ID)
	if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_audit_logs (audit_log_id,plugin_id,action,operator_id,operator_name,request_id,before_hash,after_hash,manifest_snapshot_json,plugin_snapshot_json,remark,created_at) VALUES (?,?,'migration','migration','migration',?,?,?,?,?,'snapshot conversion',UTC_TIMESTAMP(3))`, audit, a.ID, p.ID, s.PluginHash, a.PluginHash, string(a.Manifest), string(a.Package)); e != nil {
		return e
	}
	if e = transferMigrationPlacements(ctx, tx, p, n); e != nil {
		return e
	}
	for _, m := range p.Mappings {
		if m.TargetID != a.ID {
			continue
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO marketplace_conversion_mapping VALUES (?,?,?,?,?)`, p.ID, m.SourceID, m.TargetID, m.VersionID, m.Disposition); e != nil {
			return e
		}
		if m.SourceID != m.TargetID && !plannedTarget(p, m.SourceID) { // retire only explicitly mapped noncanonical catalog rows
			if e = retireMigrationCatalog(ctx, tx, m.SourceID); e != nil {
				return e
			}
		}
	}
	sh, e := stateHash(ctx, tx, p, n)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO marketplace_conversion_receipt VALUES (?,?,?,?)`, p.ID, a.ID, nodeHash(n), sh); e != nil {
		return e
	}
	return tx.Commit()
}
func applyStableID(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
func cloneRow(ctx context.Context, tx *sql.Tx, n Node) error {
	rows, e := tx.QueryContext(ctx, `SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='plugins' AND EXTRA NOT LIKE '%GENERATED%' ORDER BY ORDINAL_POSITION`)
	if e != nil {
		return e
	}
	cols := []string{}
	for rows.Next() {
		var c string
		if e = rows.Scan(&c); e != nil {
			rows.Close()
			return e
		}
		cols = append(cols, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	selects := make([]string, len(cols))
	for i, rawName := range cols {
		// Range values are copies; keep raw/quoted names visibly separate so
		// identity substitution can never depend on a mutated SQL projection.
		quotedName := "`" + rawName + "`"
		selects[i] = quotedName
		switch rawName {
		case "plugin_id":
			selects[i] = "?"
		case "current_version_id", "published_version_id", "current_version":
			selects[i] = "NULL"
		}
		cols[i] = quotedName
	}
	r, e := tx.ExecContext(ctx, `INSERT INTO plugins (`+strings.Join(cols, ",")+`) SELECT `+strings.Join(selects, ",")+` FROM plugins WHERE plugin_id=?`, n.Plugin.ID, n.SourceID)
	if e != nil {
		return e
	}
	count, e := r.RowsAffected()
	if count != 1 {
		return errApply
	}
	return e
}
func writeMigrationVersion(ctx context.Context, tx *sql.Tx, n Node) error {
	a := n.Plugin
	// version is the internal save sequence; declared_version and the formal
	// release below carry the x.y.z business label, just like repository writes.
	var seq int
	if e := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(CAST(version AS UNSIGNED)),0)+1 FROM plugin_versions WHERE plugin_id=?`, a.ID).Scan(&seq); e != nil {
		return e
	}
	rels, e := model.EncodeSnapshotRelations(n.Relations)
	if e != nil {
		return e
	}
	art := n.Artifacts
	if len(art) == 0 {
		art = json.RawMessage("[]")
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_versions (version_id,plugin_id,version,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,relations_json,created_by,created_at,declared_version,snapshot_schema_version,artifacts_json) VALUES (?,?,?,?,?,?,?,?,?,'migration',?,?,?,?)`, n.VersionID, a.ID, fmt.Sprint(seq), string(a.Manifest), string(a.Package), jvalue(a.AttachmentKeys), a.ManifestHash, a.PluginHash, string(rels), a.UpdatedAt, n.VersionLabel, model.PluginSnapshotSchemaVersion, string(art)); e != nil {
		return e
	}
	if n.Release {
		if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_version_releases VALUES (?,?,?,'migration',?)`, a.ID, n.VersionID, n.VersionLabel, a.UpdatedAt); e != nil {
			return e
		}
	}
	for _, r := range n.Relations {
		if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_version_relation_refs VALUES (?,?,?,?,?,?)`, a.ID, n.VersionID, r.ID, r.Type, r.TargetPluginID, r.TargetVersionID); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO plugin_version_reference_index_state SELECT plugin_id,version_id,?,SHA2(CAST(relations_json AS CHAR CHARACTER SET utf8mb4),256),UTC_TIMESTAMP(3) FROM plugin_versions WHERE version_id=?`, len(n.Relations), n.VersionID); e != nil {
		return e
	}
	var pub any
	if n.Release {
		pub = n.VersionID
	}
	_, e = tx.ExecContext(ctx, `UPDATE plugins SET current_version_id=?,current_version=?,published_version_id=? WHERE plugin_id=?`, n.VersionID, n.VersionLabel, pub, a.ID)
	return e
}

func plannedTarget(p Plan, id string) bool {
	for _, n := range p.Nodes {
		if n.Plugin.ID == id {
			return true
		}
	}
	return false
}

// Omit volatile statistics and AUTO_INCREMENT counters. DML must not change the
// schema identity, while column defaults, index definitions and constraints do.
var schemaIdentityQueries = []struct{ name, query string }{
	{"columns", `SELECT TABLE_NAME,COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COLUMN_DEFAULT,EXTRA,GENERATION_EXPRESSION FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME,ORDINAL_POSITION`},
	{"tables", `SELECT TABLE_NAME,ENGINE,TABLE_COLLATION,ROW_FORMAT,CREATE_OPTIONS FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME`},
	{"indexes", `SELECT TABLE_NAME,INDEX_NAME,NON_UNIQUE,SEQ_IN_INDEX,COLUMN_NAME,COLLATION,SUB_PART,INDEX_TYPE,NULLABLE,IS_VISIBLE,EXPRESSION FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME,INDEX_NAME,SEQ_IN_INDEX`},
	{"constraints", `SELECT TABLE_NAME,CONSTRAINT_NAME,CONSTRAINT_TYPE,ENFORCED FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME,CONSTRAINT_NAME`},
	{"key_columns", `SELECT TABLE_NAME,CONSTRAINT_NAME,COLUMN_NAME,ORDINAL_POSITION,POSITION_IN_UNIQUE_CONSTRAINT,REFERENCED_TABLE_SCHEMA,REFERENCED_TABLE_NAME,REFERENCED_COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME,CONSTRAINT_NAME,ORDINAL_POSITION`},
	{"foreign_keys", `SELECT TABLE_NAME,CONSTRAINT_NAME,UNIQUE_CONSTRAINT_SCHEMA,UNIQUE_CONSTRAINT_NAME,REFERENCED_TABLE_NAME,MATCH_OPTION,UPDATE_RULE,DELETE_RULE FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() ORDER BY TABLE_NAME,CONSTRAINT_NAME`},
	{"checks", `SELECT CONSTRAINT_NAME,CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() ORDER BY CONSTRAINT_NAME`},
}

// Schema shape joins immutable plan content in the resume identity. Failure to
// read any projection fails closed, including insufficient metadata privileges.
func applySchemaHash(ctx context.Context, q applyQuery) (string, error) {
	h := sha256.New()
	for _, projection := range schemaIdentityQueries {
		h.Write([]byte("schema-v2:" + projection.name + "\x00"))
		err := readDatabaseRows(ctx, q, projection.query, func(row map[string][]byte) error {
			b, err := json.Marshal(row)
			if err == nil {
				_, err = h.Write(b)
			}
			return err
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Revalidate every source inventory consumed by this node, including duplicate
// rows merged into it. These row/range locks survive through its commit.
func verifyNodeSourceTables(ctx context.Context, tx *sql.Tx, p Plan, n Node) error {
	own, ok := n.Evidence[n.SourceID]
	if !ok || own.RowHash != n.SourceRowHash {
		return errApply
	}
	for _, mapping := range p.Mappings {
		if mapping.TargetID == n.Plugin.ID {
			if _, ok := n.Evidence[mapping.SourceID]; !ok {
				return errApply
			}
		}
	}
	ids := make([]string, 0, len(n.Evidence))
	for id := range n.Evidence {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		expected := n.Evidence[id]
		if expected.RowHash == "" || len(expected.Tables) != len(sourceTables) {
			return errApply
		}
		if id != n.SourceID {
			count := 0
			err := readDatabaseRows(ctx, tx, `SELECT * FROM plugins WHERE plugin_id=? FOR UPDATE`, func(row map[string][]byte) error {
				count++
				hash, err := sourceRowDigest(row)
				if err != nil || hash != expected.RowHash {
					return errApply
				}
				return nil
			}, id)
			if err != nil || count != 1 {
				return errApply
			}
		}
		actual, err := captureSourceTables(ctx, tx, id, true)
		if err != nil {
			return errApply
		}
		for _, spec := range sourceTables {
			if actual[spec.table] != expected.Tables[spec.table] {
				return errApply
			}
		}
	}
	return nil
}

func describePartialApply(ctx context.Context, db *sql.DB, p Plan, r *ApplyReport) {
	r.CommitState = "unknown"
	r.Recovery = "retry_same_checkpoint_or_restore_backup"
	ids := []string{}
	err := readDatabaseRows(ctx, db, `SELECT target_id FROM marketplace_conversion_receipt WHERE run_id=? ORDER BY target_id`, func(row map[string][]byte) error {
		ids = append(ids, string(row["target_id"]))
		return nil
	}, p.ID)
	if err != nil {
		return
	}
	r.CommittedIDs = ids
	r.CommitState = "none"
	if len(ids) > 0 {
		r.CommitState = "partial"
	}
}

func verifySourceRows(ctx context.Context, tx *sql.Tx, p Plan, n Node) error {
	// Compare the complete source row under its lock before clone/update.
	// A checkpoint from an older binary without row evidence must fail closed.
	if n.SourceRowHash == "" {
		return errApply
	}
	var published sql.NullString
	count := 0
	e := readDatabaseRows(ctx, tx, `SELECT * FROM plugins WHERE plugin_id=? FOR UPDATE`, func(row map[string][]byte) error {
		count++
		actual, err := sourceRowDigest(row)
		if err != nil || actual != n.SourceRowHash {
			return errApply
		}
		if raw := row["published_version_id"]; raw != nil {
			published = sql.NullString{String: string(raw), Valid: true}
		}
		return nil
	}, n.SourceID)
	if e != nil || count != 1 {
		return errApply
	}

	if published.Valid != (n.SourcePublishedVersionID != nil) || (published.Valid && published.String != *n.SourcePublishedVersionID) {
		return errApply
	}
	if published.Valid {
		var label, actor string
		var at time.Time
		if e = tx.QueryRowContext(ctx, `SELECT version_label,released_by,released_at FROM plugin_version_releases WHERE plugin_id=? AND version_id=? FOR UPDATE`, n.SourceID, published.String).Scan(&label, &actor, &at); e != nil {
			return errApply
		}
		r := n.SourceRelease
		if r == nil || r.PluginID != n.SourceID || r.VersionID != published.String || r.VersionLabel != label || r.ReleasedBy != actor || !r.ReleasedAt.Equal(at) {
			return errApply
		}
	} else if n.SourceRelease != nil {
		return errApply
	}

	return verifyNodeSourceTables(ctx, tx, p, n)
}

// CheckPlanSources detects existing drift before any destination object write.
// It is a preflight only: locks are released on return, so apply checks again.
func CheckPlanSources(ctx context.Context, db *sql.DB, p Plan, expected DatabaseIdentity) error {
	if len(p.Issues) > 0 {
		return ErrIssues
	}
	if err := guardApply(ctx, db, expected); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return errApply
	}
	defer tx.Rollback()
	var hasTable int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='marketplace_conversion_run'`).Scan(&hasTable); err != nil {
		return errApply
	}
	hasRun := false
	if hasTable > 0 {
		var run string
		err = tx.QueryRowContext(ctx, `SELECT run_id FROM marketplace_conversion_run WHERE slot=1`).Scan(&run)
		if err == nil {
			if err = checkRun(ctx, tx, p); err != nil {
				return err
			}
			hasRun = true
		} else if err != sql.ErrNoRows {
			return errApply
		}
	}
	if err = verifyCategorySnapshot(ctx, tx, p); err != nil {
		return err
	}
	for _, n := range p.Nodes {
		if hasRun {
			var saved string
			err = tx.QueryRowContext(ctx, `SELECT node_hash FROM marketplace_conversion_receipt WHERE run_id=? AND target_id=? FOR UPDATE`, p.ID, n.Plugin.ID).Scan(&saved)
			if err == nil {
				if err = verifyReceipt(ctx, tx, p, n); err != nil {
					return err
				}
				continue
			}
			if err != sql.ErrNoRows {
				return errApply
			}
		}
		if err = verifySourceRows(ctx, tx, p, n); err != nil {
			return err
		}
	}
	return nil
}
