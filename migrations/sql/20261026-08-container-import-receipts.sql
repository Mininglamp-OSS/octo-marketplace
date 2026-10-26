-- +migrate Up
-- A25 idempotency: one frozen Detail + resolved[] response per Admin caller,
-- operation, target and Idempotency-Key. The response is never recomputed.
CREATE TABLE plugin_container_import_receipts (
  scope_uid VARCHAR(64) NOT NULL,
  operation VARCHAR(16) NOT NULL,
  plugin_id VARCHAR(64) NOT NULL DEFAULT '',
  idempotency_key VARCHAR(200) NOT NULL,
  input_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  state VARCHAR(16) NOT NULL DEFAULT 'reserved',
  response_json MEDIUMTEXT NULL,
  created_at DATETIME(3) NOT NULL,
  CONSTRAINT chk_container_import_state CHECK (state IN ('reserved','completed')),
  PRIMARY KEY (scope_uid, operation, plugin_id, idempotency_key),
  KEY idx_container_import_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE plugin_container_import_receipts;
