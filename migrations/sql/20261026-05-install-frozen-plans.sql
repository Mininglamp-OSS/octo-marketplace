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
-- Existing attempts have no frozen payload. Retain uncertainty; never infer
-- a replacement plan from today's mutable catalog.
UPDATE plugin_install_attempts SET attempt_state='SUCCEEDED' WHERE completed=TRUE;

-- +migrate Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Frozen installation plan downgrade refused';
