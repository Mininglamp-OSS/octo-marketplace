-- +migrate Up
ALTER TABLE plugin_install_attempts
 ADD COLUMN frozen_plan_json JSON NULL,
 ADD COLUMN hmac_key_id VARCHAR(64) NOT NULL DEFAULT '',
 ADD COLUMN intent_digest CHAR(64) NOT NULL DEFAULT '',
 ADD COLUMN attempt_state VARCHAR(16) NOT NULL DEFAULT 'UNKNOWN',
 ADD COLUMN had_unknown BOOLEAN NOT NULL DEFAULT TRUE,
 ADD COLUMN lease_token VARCHAR(64) NULL,
 ADD COLUMN lease_until DATETIME(3) NULL,
 ADD COLUMN retry_until DATETIME(3) NULL,
 ADD CONSTRAINT chk_install_attempt_state CHECK (attempt_state IN ('PREPARED','DISPATCHING','SUCCEEDED','FAILED','UNKNOWN','EXPIRED'));
-- Existing attempts have no frozen payload. The new column default is UNKNOWN,
-- so historical rows keep uncertainty without an UPDATE that could overwrite a
-- later terminal state on a partial/re-run migration.

-- +migrate Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Frozen installation plan downgrade refused';
