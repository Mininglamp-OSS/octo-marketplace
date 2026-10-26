-- +migrate Up
-- MP-04 offline-authorized dependency migration ledger.
--
-- plugin_stock_migration_batches/roots remain the batch/root audit base. The
-- columns below bind that audit to the authoritative run and authorization;
-- there is no second run ledger and no pointer-rollback state.
ALTER TABLE plugin_stock_migration_batches
 ADD COLUMN run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD COLUMN plan_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD COLUMN environment_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD COLUMN authorization_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD KEY idx_stock_migration_batch_run (run_id);

CREATE TABLE plugin_dependency_migration_runs (
 run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 batch_id VARCHAR(64) NOT NULL,
 environment_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_scope CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_identity VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_plugin_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_version_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 expected_edit_revision BIGINT UNSIGNED NOT NULL,
 input_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 plan_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 root_node_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'root',
 plan_json JSON NOT NULL,
 frozen_plan_object_key VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NULL,
 status ENUM('completed','waiting_review','blocked') NOT NULL,
 blocked_reason VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 last_authorization_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY(run_id),
 UNIQUE KEY uq_dependency_migration_run_identity (source_scope,source_identity,plan_digest,root_node_key),
 KEY idx_dependency_migration_run_batch (batch_id),
 KEY idx_dependency_migration_run_status (status),
 CONSTRAINT fk_dependency_migration_run_batch FOREIGN KEY(batch_id) REFERENCES plugin_stock_migration_batches(batch_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE plugin_dependency_migration_map (
 run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_scope CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_identity VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 plan_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_node_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 origin_node_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_plugin_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 source_version_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 source_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 action VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 target_plugin_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 target_version_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 target_effective_revision BIGINT UNSIGNED NULL,
 target_effective_digest VARCHAR(96) CHARACTER SET ascii COLLATE ascii_bin NULL,
 step_state ENUM('reserved','created','waiting_review','published','source_switched','blocked') NOT NULL,
 review_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 source_commit_json JSON NULL,
 error_reason VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY(run_id,source_node_key),
 UNIQUE KEY uq_dependency_migration_map_identity (source_scope,source_identity,plan_digest,source_node_key),
 CONSTRAINT fk_dependency_migration_map_run FOREIGN KEY(run_id) REFERENCES plugin_dependency_migration_runs(run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE plugin_migration_authorizations (
 authorization_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 batch_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 plan_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 environment_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 key_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 issuer_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 actor_id VARCHAR(200) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_scope CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_identity VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 issued_at DATETIME(3) NOT NULL,
 expires_at DATETIME(3) NOT NULL,
 payload_json JSON NOT NULL,
 signature VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 replaced_authorization_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 revoked_at DATETIME(3) NULL,
 created_at DATETIME(3) NOT NULL,
 PRIMARY KEY(authorization_id),
 KEY idx_migration_authorization_run (run_id),
 KEY idx_migration_authorization_batch (batch_id),
 CONSTRAINT fk_migration_authorization_run FOREIGN KEY(run_id) REFERENCES plugin_dependency_migration_runs(run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE plugin_migration_authorization_audit (
 event_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
 authorization_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 event ENUM('bound','renewed') NOT NULL,
 actor_id VARCHAR(200) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 environment_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 replaced_authorization_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 created_at DATETIME(3) NOT NULL,
 PRIMARY KEY(event_id),
 KEY idx_migration_authorization_audit_run (run_id),
 CONSTRAINT fk_migration_authorization_audit_run FOREIGN KEY(run_id) REFERENCES plugin_dependency_migration_runs(run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Dependency migration audit schema downgrade refused';
