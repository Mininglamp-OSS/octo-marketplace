package snapshot

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
)

// Restore only writes a pre-created dedicated rehearsal database. Table receipts
// commit with rows; a pre-DDL ownership marker makes an interrupted CREATE safe
// to resume without adopting unrelated target tables.
func Restore(ctx context.Context, db *sql.DB, b Bundle) error {
	v, e := Load(b.Dir)
	if e != nil {
		return e
	}
	if b.ID != v.ID {
		return fail("bundle identity changed")
	}
	b = v
	c, e := db.Conn(ctx)
	if e != nil {
		return fail("cannot connect target")
	}
	defer c.Close()
	// Never return session settings or a failed cleanup to the connection pool.
	defer c.Raw(func(any) error { return driver.ErrBadConn })
	if _, e = c.ExecContext(ctx, "SET SESSION time_zone = ?", "+00:00"); e != nil {
		return fail("cannot set target UTC session")
	}
	var name, server string
	if c.QueryRowContext(ctx, "SELECT DATABASE(), @@server_uuid").Scan(&name, &server) != nil {
		return fail("cannot identify target")
	}
	if !validName(name) || !strings.HasPrefix(name, "octo_marketplace_rehearsal_") || name == b.Manifest.Source.DBName && server == b.Manifest.Source.ServerUUID {
		return fail("target is not an isolated rehearsal database")
	}
	var version string
	if c.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version) != nil || !supportsAtomicIdentity(version) {
		return fail("restore requires MySQL 8.0.21 or newer")
	}
	var locked int
	if c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", "marketplace-snapshot:"+name).Scan(&locked) != nil || locked != 1 {
		return fail("target restore already running")
	}
	defer c.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", "marketplace-snapshot:"+name)
	rows, e := c.QueryContext(ctx, "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()")
	if e != nil {
		return fail("cannot inspect target")
	}
	existing := map[string]bool{}
	for rows.Next() {
		var n string
		if rows.Scan(&n) != nil {
			rows.Close()
			return fail("cannot inspect target")
		}
		existing[n] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return fail("cannot inspect target")
	}
	if len(existing) > 0 && !existing[receiptTable] {
		return fail("unrelated populated target")
	}
	if !existing[receiptTable] {
		// MySQL 8.0.21+ atomically commits the InnoDB table and root identity.
		// A failed/interrupted statement leaves no unowned empty receipt table.
		if _, e = c.ExecContext(ctx, "CREATE TABLE `"+receiptTable+"` (table_name varchar(64) PRIMARY KEY, snapshot_id char(64) NOT NULL, schema_digest char(64) NOT NULL, rows_digest char(64) NOT NULL, complete boolean NOT NULL) ENGINE=InnoDB AS SELECT '' AS table_name, ? AS snapshot_id, '' AS schema_digest, '' AS rows_digest, 1 AS complete", b.ID); e != nil {
			return fail("cannot create restore identity")
		}
	}
	type receipt struct {
		snapshot, schema, data string
		complete               bool
	}
	receipts := map[string]receipt{}
	rows, e = c.QueryContext(ctx, "SELECT table_name, snapshot_id, schema_digest, rows_digest, complete FROM `"+receiptTable+"`")
	if e != nil {
		return fail("cannot read restore receipts")
	}
	for rows.Next() {
		var n string
		var r receipt
		if rows.Scan(&n, &r.snapshot, &r.schema, &r.data, &r.complete) != nil {
			rows.Close()
			return fail("invalid restore receipt")
		}
		if r.snapshot != b.ID {
			rows.Close()
			return fail("foreign restore identity")
		}
		receipts[n] = r
	}
	e = rows.Err()
	rows.Close()
	if e != nil || receipts[""].snapshot != b.ID {
		return fail("missing restore identity")
	}
	allowed := map[string]Table{}
	for _, t := range b.Manifest.Tables {
		allowed[t.Name] = t
	}
	for n := range existing {
		if n == receiptTable {
			continue
		}
		t, ok := allowed[n]
		r, own := receipts[n]
		if !ok || !own || r.schema != t.Schema.SHA256 || r.data != t.Data.SHA256 {
			return fail("unrelated target table")
		}
	}
	// Use one reserved connection: foreign_key_checks is session-scoped. Every
	// constraint is explicitly checked before returning success (reenabling alone
	// does not validate existing rows in MySQL).
	if _, e = c.ExecContext(ctx, "SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',NO_AUTO_VALUE_ON_ZERO')"); e != nil {
		return fail("cannot preserve zero identity values")
	}
	if _, e = c.ExecContext(ctx, "SET SESSION FOREIGN_KEY_CHECKS = 0"); e != nil {
		return fail("cannot suspend target foreign keys")
	}
	defer func() {
		if _, err := c.ExecContext(context.Background(), "SET SESSION FOREIGN_KEY_CHECKS = 1"); err != nil {
			c.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	for _, t := range b.Manifest.Tables {
		r, owned := receipts[t.Name]
		if owned && (r.schema != t.Schema.SHA256 || r.data != t.Data.SHA256) {
			return fail("restore receipt checksum changed")
		}
		if !owned {
			if _, e = c.ExecContext(ctx, "INSERT INTO `"+receiptTable+"` VALUES (?, ?, ?, ?, 0)", t.Name, b.ID, t.Schema.SHA256, t.Data.SHA256); e != nil {
				return fail("cannot record schema ownership")
			}
		}
		if !existing[t.Name] {
			ddl, e := readSchema(b.Dir, t)
			if e != nil {
				return e
			}
			if _, e = c.ExecContext(ctx, ddl); e != nil {
				return fail("cannot create target schema")
			}
		}
		var actual, ddl string
		if c.QueryRowContext(ctx, "SHOW CREATE TABLE "+quote(t.Name)).Scan(&actual, &ddl) != nil {
			return fail("cannot verify target schema")
		}
		expected, e := readSchema(b.Dir, t)
		if e != nil {
			return e
		}
		if actual != t.Name || ddl != expected {
			return fail("target schema differs from snapshot")
		}
		if owned && r.complete {
			if e = verifyTargetRows(ctx, c, b, t); e != nil {
				return e
			}
			continue
		}
		tx, e := c.BeginTx(ctx, nil)
		if e != nil {
			return fail("cannot begin table restore")
		}
		var count int64
		if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quote(t.Name)).Scan(&count) != nil || count != 0 {
			tx.Rollback()
			return fail("unreceipted target rows")
		}
		cols := make([]string, len(t.Columns))
		marks := make([]string, len(cols))
		for i, col := range t.Columns {
			cols[i] = quote(col)
			marks[i] = "?"
		}
		query := "INSERT INTO " + quote(t.Name) + " (" + strings.Join(cols, ",") + ") VALUES (" + strings.Join(marks, ",") + ")"
		e = readTable(b, t, func(cells []*[]byte) error {
			args := make([]any, len(cells))
			for i, cell := range cells {
				if cell != nil {
					args[i] = []byte(*cell)
				}
			}
			if _, e := tx.ExecContext(ctx, query, args...); e != nil {
				return fail("cannot restore row")
			}
			return nil
		})
		if e != nil {
			tx.Rollback()
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE `"+receiptTable+"` SET complete = 1 WHERE table_name = ? AND snapshot_id = ?", t.Name, b.ID); e != nil {
			tx.Rollback()
			return fail("cannot commit table receipt")
		}
		if tx.Commit() != nil {
			return fail("cannot commit table restore")
		}
		if e = verifyTargetRows(ctx, c, b, t); e != nil {
			return e
		}
	}
	if e = checkForeignKeys(ctx, c); e != nil {
		return e
	}
	if _, e = c.ExecContext(ctx, "SET SESSION FOREIGN_KEY_CHECKS = 1"); e != nil {
		return fail("cannot restore foreign key checks")
	}
	return nil
}

// compare target rows as a multiset of hashes, preserving duplicates without
// depending on storage-engine scan ordering. Memory is bounded by row count of
// hashes, never by raw content size.
func verifyTargetRows(ctx context.Context, c *sql.Conn, b Bundle, t Table) error {
	hashes := map[string]int{}
	e := readTable(b, t, func(cells []*[]byte) error { hashes[cellHash(cells)]++; return nil })
	if e != nil {
		return e
	}
	cols := make([]string, len(t.Columns))
	for i, v := range t.Columns {
		cols[i] = quote(v)
	}
	rows, e := c.QueryContext(ctx, "SELECT "+strings.Join(cols, ",")+" FROM "+quote(t.Name))
	if e != nil {
		return fail("cannot verify restored rows")
	}
	defer rows.Close()
	for rows.Next() {
		cells := make([]sql.RawBytes, len(cols))
		dest := make([]any, len(cols))
		for i := range cells {
			dest[i] = &cells[i]
		}
		if rows.Scan(dest...) != nil {
			return fail("cannot verify restored row")
		}
		values := make([]*[]byte, len(cells))
		for i, v := range cells {
			if v != nil {
				v := []byte(v)
				values[i] = &v
			}
		}
		h := cellHash(values)
		if hashes[h] == 0 {
			return fail("restored row mismatch")
		}
		hashes[h]--
	}
	if rows.Err() != nil {
		return fail("cannot verify restored rows")
	}
	for _, n := range hashes {
		if n != 0 {
			return fail("restored row missing")
		}
	}
	return nil
}
func checkForeignKeys(ctx context.Context, c *sql.Conn) error {
	rows, e := c.QueryContext(ctx, "SELECT TABLE_NAME, CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA = DATABASE() AND REFERENCED_TABLE_NAME IS NOT NULL ORDER BY TABLE_NAME, CONSTRAINT_NAME, ORDINAL_POSITION")
	if e != nil {
		return fail("cannot inspect foreign keys")
	}
	type fk struct {
		table, parent string
		columns, refs []string
	}
	var keys []fk
	last := ""
	for rows.Next() {
		var t, n, col, p, ref string
		if rows.Scan(&t, &n, &col, &p, &ref) != nil || !validName(t) || !validName(n) || !validName(col) || !validName(p) || !validName(ref) {
			rows.Close()
			return fail("invalid foreign key metadata")
		}
		key := t + "/" + n
		if key != last {
			keys = append(keys, fk{table: t, parent: p})
			last = key
		}
		k := &keys[len(keys)-1]
		k.columns = append(k.columns, col)
		k.refs = append(k.refs, ref)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return fail("cannot inspect foreign keys")
	}
	for _, k := range keys {
		var present, join []string
		for i, col := range k.columns {
			present = append(present, "c."+quote(col)+" IS NOT NULL")
			join = append(join, "p."+quote(k.refs[i])+" = c."+quote(col))
		}
		q := fmt.Sprintf("SELECT COUNT(*) FROM %s c WHERE %s AND NOT EXISTS (SELECT 1 FROM %s p WHERE %s)", quote(k.table), strings.Join(present, " AND "), quote(k.parent), strings.Join(join, " AND "))
		var n int64
		if c.QueryRowContext(ctx, q).Scan(&n) != nil || n != 0 {
			return fail("restored foreign key violation")
		}
	}
	return nil
}

func supportsAtomicIdentity(version string) bool {
	if strings.Contains(strings.ToLower(version), "mariadb") {
		return false
	}
	var major, minor, patch int
	n, e := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch)
	return e == nil && n == 3 && (major > 8 || major == 8 && (minor > 0 || minor == 0 && patch >= 21))
}
