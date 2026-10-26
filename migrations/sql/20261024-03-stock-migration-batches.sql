-- +migrate Up
CREATE TABLE plugin_stock_migration_batches (
 batch_id VARCHAR(64) NOT NULL,
 state ENUM('active','rolled_back') NOT NULL DEFAULT 'active',
 created_at DATETIME(3) NOT NULL,
 PRIMARY KEY(batch_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE plugin_stock_migration_roots (
 batch_id VARCHAR(64) NOT NULL,
 root_plugin_id VARCHAR(64) NOT NULL,
 source_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 mappings_json JSON NOT NULL,
 before_json JSON NOT NULL,
 after_json JSON NOT NULL,
 node_states_json JSON NOT NULL,
 applied_at DATETIME(3) NOT NULL,
 PRIMARY KEY(batch_id,root_plugin_id),
 CONSTRAINT fk_stock_migration_batch FOREIGN KEY(batch_id) REFERENCES plugin_stock_migration_batches(batch_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +migrate Down
-- Audit history and its migration receipt must survive every rollback path.
-- Use the stock migration rollback command to restore data pointers instead.
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Stock migration audit schema downgrade refused';
