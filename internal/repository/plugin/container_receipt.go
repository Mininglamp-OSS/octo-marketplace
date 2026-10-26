package plugin

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ContainerImportReceipt freezes one A25 response for an Admin Idempotency-Key.
type ContainerImportReceipt struct {
	ScopeUID       string
	Operation      string
	PluginID       string
	IdempotencyKey string
	InputDigest    string
	ResponseJSON   string
	CreatedAt      time.Time
}

func (r *Repo) GetContainerImportReceipt(ctx context.Context, scopeUID, operation, pluginID, key string) (*ContainerImportReceipt, error) {
	var rec ContainerImportReceipt
	err := r.db.QueryRowContext(ctx, `SELECT scope_uid,operation,plugin_id,idempotency_key,input_digest,response_json,created_at FROM plugin_container_import_receipts WHERE scope_uid=? AND operation=? AND plugin_id=? AND idempotency_key=?`, scopeUID, operation, pluginID, key).Scan(&rec.ScopeUID, &rec.Operation, &rec.PluginID, &rec.IdempotencyKey, &rec.InputDigest, &rec.ResponseJSON, &rec.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (r *Repo) InsertContainerImportReceipt(ctx context.Context, rec ContainerImportReceipt, now time.Time) (bool, error) {
	result, err := r.db.ExecContext(ctx, `INSERT INTO plugin_container_import_receipts(scope_uid,operation,plugin_id,idempotency_key,input_digest,response_json,created_at) VALUES (?,?,?,?,?,?,?)`, rec.ScopeUID, rec.Operation, rec.PluginID, rec.IdempotencyKey, rec.InputDigest, rec.ResponseJSON, now)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
