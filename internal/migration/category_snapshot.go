package migration

import (
	"context"
	"database/sql"
)

// Freeze the shared catalog in the same input transaction as plugin rows.
// This deliberately blocks unrelated category edits too: operators pause all
// Marketplace writers for conversion. Only this run's receipted inserts differ.
func captureCategorySnapshot(ctx context.Context, q databaseQueryer, lock bool) (map[string]string, error) {
	out := map[string]string{}
	query := `SELECT * FROM plugin_category_placements ORDER BY placement_id`
	if lock {
		query += " FOR UPDATE"
	}
	err := readDatabaseRows(ctx, q, query, func(row map[string][]byte) error {
		h, err := sourceRowDigest(row)
		if err != nil {
			return err
		}
		out[string(row["placement_id"])] = h
		return nil
	})
	return out, err
}

func verifyCategorySnapshot(ctx context.Context, tx *sql.Tx, p Plan) error {
	if !p.CategorySnapshotCaptured {
		return errApply
	}
	remaining := map[string]string{}
	for id, h := range p.CategorySnapshot {
		remaining[id] = h
	}
	extras := []map[string][]byte{}
	registrations := map[[3]string]bool{}
	err := readDatabaseRows(ctx, tx, `SELECT * FROM plugin_category_placements ORDER BY placement_id FOR UPDATE`, func(row map[string][]byte) error {
		registrations[[3]string{string(row["placement_code"]), string(row["plugin_type"]), string(row["category_id"])}] = true
		id := string(row["placement_id"])
		if expected, ok := remaining[id]; ok {
			actual, err := sourceRowDigest(row)
			if err != nil || expected != actual {
				return errApply
			}
			delete(remaining, id)
		} else {
			extras = append(extras, row)
		}
		return nil
	})
	if err != nil || len(remaining) > 0 {
		return errApply
	}
	if err = verifyCompletedCategoryPresence(ctx, tx, p, registrations); err != nil {
		return err
	}
	// A previous node may have created a missing registration. It must have the
	// deterministic identity AND a completed target whose receipt covers that row.
	for _, row := range extras {
		code, kind, category := string(row["placement_code"]), string(row["plugin_type"]), string(row["category_id"])
		if string(row["placement_id"]) != applyStableID("category:"+code+":"+kind+":"+category) {
			return errApply
		}
		found := false
		for _, n := range p.Nodes {
			if string(n.Plugin.Type) != kind {
				continue
			}
			var count int
			err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM marketplace_conversion_receipt r JOIN plugin_placements p ON p.plugin_id=r.target_id WHERE r.run_id=? AND r.target_id=? AND p.placement_code=? AND p.category_id=?`, p.ID, n.Plugin.ID, code, category).Scan(&count)
			if err != nil {
				return errApply
			}
			if count > 0 {
				// This is the full-row integrity check, not just proof that a
				// target exists: stateHash includes SELECT c.* for every category
				// registration referenced by this target's placements. Its digest
				// was written atomically with the first registration insert and
				// covers identity, type, visibility, order and both timestamps.
				// Never accept the matching ID/placement without this comparison.
				if verifyReceipt(ctx, tx, p, n) != nil {
					return errApply
				}
				found = true
				break
			}
		}
		if !found {
			return errApply
		}
	}
	return nil
}

// A deleted run-created row is no longer in extras. Check the registrations
// required by already committed placements too, before another node can recreate
// the missing row. Full-row mutation checks remain bound to receipt stateHash.
func verifyCompletedCategoryPresence(ctx context.Context, tx *sql.Tx, p Plan, registrations map[[3]string]bool) error {
	if len(p.Nodes) == 0 {
		return nil
	}
	var hasReceipts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='marketplace_conversion_receipt'`).Scan(&hasReceipts); err != nil {
		return errApply
	}
	// The initial read-only preflight runs before setupApply creates receipts.
	if hasReceipts == 0 {
		return nil
	}
	types := map[string]string{}
	for _, n := range p.Nodes {
		types[n.Plugin.ID] = string(n.Plugin.Type)
	}
	err := readDatabaseRows(ctx, tx, `SELECT p.plugin_id,p.placement_code,p.category_id FROM marketplace_conversion_receipt r JOIN plugin_placements p ON p.plugin_id=r.target_id WHERE r.run_id=? AND p.category_id IS NOT NULL ORDER BY p.plugin_id,p.placement_code FOR UPDATE`, func(row map[string][]byte) error {
		kind, ok := types[string(row["plugin_id"])]
		if !ok || !registrations[[3]string{string(row["placement_code"]), kind, string(row["category_id"])}] {
			return errApply
		}
		return nil
	}, p.ID)
	if err != nil {
		return errApply
	}
	return nil
}
