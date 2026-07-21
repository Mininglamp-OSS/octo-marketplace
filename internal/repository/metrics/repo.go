package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Repo handles persistence for resource_metrics.
type Repo struct {
	db *sql.DB
}

// New creates a new metrics Repo.
func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// UpsertCounts atomically upserts view/download/install deltas for a resource.
// ON DUPLICATE KEY UPDATE accumulates the deltas into the existing counts.
func (r *Repo) UpsertCounts(ctx context.Context, resourceType, resourceID string, viewDelta, downloadDelta, installDelta int64) error {
	const query = `INSERT INTO resource_metrics (resource_type, resource_id, view_count, download_count, install_count)
	VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  view_count = view_count + VALUES(view_count),
  download_count = download_count + VALUES(download_count),
  install_count = install_count + VALUES(install_count)`

	_, err := r.db.ExecContext(ctx, query, resourceType, resourceID, viewDelta, downloadDelta, installDelta)
	if err != nil {
		return fmt.Errorf("upsert resource_metrics %s/%s: %w", resourceType, resourceID, err)
	}
	return nil
}

// UpsertCountsOnce atomically applies a flush delta once.
// Replaying the same flushID is a no-op, which lets Redis pending entries be
// retried safely after a process crash between DB commit and Redis ack.
func (r *Repo) UpsertCountsOnce(ctx context.Context, flushID, resourceType, resourceID string, viewDelta, downloadDelta, installDelta int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin metrics flush %s: %w", flushID, err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `INSERT IGNORE INTO resource_metric_flushes
		(flush_id, resource_type, resource_id, view_delta, download_delta, install_delta)
		VALUES (?, ?, ?, ?, ?, ?)`,
		flushID, resourceType, resourceID, viewDelta, downloadDelta, installDelta,
	)
	if err != nil {
		return fmt.Errorf("record metrics flush %s: %w", flushID, err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect metrics flush %s: %w", flushID, err)
	}
	if inserted == 0 {
		return tx.Commit()
	}

	const query = `INSERT INTO resource_metrics (resource_type, resource_id, view_count, download_count, install_count)
	VALUES (?, ?, ?, ?, ?)
	ON DUPLICATE KEY UPDATE
	  view_count = view_count + VALUES(view_count),
	  download_count = download_count + VALUES(download_count),
	  install_count = install_count + VALUES(install_count)`

	if _, err := tx.ExecContext(ctx, query, resourceType, resourceID, viewDelta, downloadDelta, installDelta); err != nil {
		return fmt.Errorf("upsert resource_metrics %s/%s flush %s: %w", resourceType, resourceID, flushID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit metrics flush %s: %w", flushID, err)
	}
	return nil
}

// DeleteAppliedFlushesBefore removes old idempotency ledger rows.
func (r *Repo) DeleteAppliedFlushesBefore(ctx context.Context, cutoff time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM resource_metric_flushes WHERE created_at < ?`,
		cutoff,
	)
	if err != nil {
		return fmt.Errorf("delete old resource_metric_flushes before %s: %w", cutoff.Format(time.RFC3339), err)
	}
	return nil
}
