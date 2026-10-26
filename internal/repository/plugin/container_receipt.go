package plugin

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ContainerImportReceipt freezes one A25 response for an Admin Idempotency-Key.
// A reserved row is a claim: no second import may run under the same key.
type ContainerImportReceipt struct {
	ScopeUID       string
	Operation      string
	PluginID       string
	IdempotencyKey string
	InputDigest    string
	State          string
	ResponseJSON   string
	CreatedAt      time.Time
}

func (r *Repo) GetContainerImportReceipt(ctx context.Context, scopeUID, operation, pluginID, key string) (*ContainerImportReceipt, error) {
	var rec ContainerImportReceipt
	var response sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT scope_uid,operation,plugin_id,idempotency_key,input_digest,state,response_json,created_at FROM plugin_container_import_receipts WHERE scope_uid=? AND operation=? AND plugin_id=? AND idempotency_key=?`, scopeUID, operation, pluginID, key).Scan(&rec.ScopeUID, &rec.Operation, &rec.PluginID, &rec.IdempotencyKey, &rec.InputDigest, &rec.State, &response, &rec.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rec.ResponseJSON = response.String
	return &rec, nil
}

// ClaimContainerImportReceipt inserts a reserved claim. It returns the existing
// row when another request already owns the key, and false when this call won.
func (r *Repo) ClaimContainerImportReceipt(ctx context.Context, rec ContainerImportReceipt, now time.Time) (*ContainerImportReceipt, bool, error) {
	result, err := r.db.ExecContext(ctx, `INSERT IGNORE INTO plugin_container_import_receipts(scope_uid,operation,plugin_id,idempotency_key,input_digest,state,response_json,created_at) VALUES (?,?,?,?,?,'reserved',NULL,?)`, rec.ScopeUID, rec.Operation, rec.PluginID, rec.IdempotencyKey, rec.InputDigest, now)
	if err != nil {
		return nil, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if n == 1 {
		return &rec, true, nil
	}
	existing, err := r.GetContainerImportReceipt(ctx, rec.ScopeUID, rec.Operation, rec.PluginID, rec.IdempotencyKey)
	return existing, false, err
}

func (r *Repo) CompleteContainerImportReceipt(ctx context.Context, rec ContainerImportReceipt) error {
	result, err := r.db.ExecContext(ctx, `UPDATE plugin_container_import_receipts SET state='completed',response_json=? WHERE scope_uid=? AND operation=? AND plugin_id=? AND idempotency_key=? AND input_digest=?`, rec.ResponseJSON, rec.ScopeUID, rec.Operation, rec.PluginID, rec.IdempotencyKey, rec.InputDigest)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	return nil
}

func (r *Repo) ReleaseContainerImportReceipt(ctx context.Context, scopeUID, operation, pluginID, key, digest string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM plugin_container_import_receipts WHERE scope_uid=? AND operation=? AND plugin_id=? AND idempotency_key=? AND input_digest=? AND state='reserved'`, scopeUID, operation, pluginID, key, digest)
	return err
}
