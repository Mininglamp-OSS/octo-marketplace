-- +migrate Up
-- MySQL may commit DDL before sql-migrate records the migration receipt.
-- Build one ALTER from only missing fields/constraint so both complete and
-- partial schema replays converge without rewriting durable attempt evidence.
-- +migrate StatementBegin
SET @frozen_plan_ddl = CONCAT_WS(',',
 IF(EXISTS(SELECT 1 FROM information_schema.columns
   WHERE table_schema=DATABASE() AND table_name='plugin_install_attempts'
   AND column_name='frozen_plan_json'), NULL, 'ADD COLUMN frozen_plan_json JSON NULL'),
 IF(EXISTS(SELECT 1 FROM information_schema.columns
   WHERE table_schema=DATABASE() AND table_name='plugin_install_attempts'
   AND column_name='hmac_key_id'), NULL, 'ADD COLUMN hmac_key_id VARCHAR(64) NOT NULL DEFAULT '''''),
 IF(EXISTS(SELECT 1 FROM information_schema.columns
   WHERE table_schema=DATABASE() AND table_name='plugin_install_attempts'
   AND column_name='intent_digest'), NULL, 'ADD COLUMN intent_digest CHAR(64) NOT NULL DEFAULT '''''),
 IF(EXISTS(SELECT 1 FROM information_schema.columns
   WHERE table_schema=DATABASE() AND table_name='plugin_install_attempts'
   AND column_name='attempt_state'), NULL, 'ADD COLUMN attempt_state VARCHAR(16) NOT NULL DEFAULT ''UNKNOWN'''),
 IF(EXISTS(SELECT 1 FROM information_schema.columns
   WHERE table_schema=DATABASE() AND table_name='plugin_install_attempts'
   AND column_name='had_unknown'), NULL, 'ADD COLUMN had_unknown BOOLEAN NOT NULL DEFAULT TRUE'),
 IF(EXISTS(SELECT 1 FROM information_schema.columns
   WHERE table_schema=DATABASE() AND table_name='plugin_install_attempts'
   AND column_name='lease_token'), NULL, 'ADD COLUMN lease_token VARCHAR(64) NULL'),
 IF(EXISTS(SELECT 1 FROM information_schema.columns
   WHERE table_schema=DATABASE() AND table_name='plugin_install_attempts'
   AND column_name='lease_until'), NULL, 'ADD COLUMN lease_until DATETIME(3) NULL'),
 IF(EXISTS(SELECT 1 FROM information_schema.columns
   WHERE table_schema=DATABASE() AND table_name='plugin_install_attempts'
   AND column_name='retry_until'), NULL, 'ADD COLUMN retry_until DATETIME(3) NULL'),
 IF(EXISTS(SELECT 1 FROM information_schema.table_constraints
   WHERE constraint_schema=DATABASE() AND table_name='plugin_install_attempts'
   AND constraint_name='chk_install_attempt_state' AND constraint_type='CHECK'),
   NULL, 'ADD CONSTRAINT chk_install_attempt_state CHECK (attempt_state IN (''PREPARED'',''DISPATCHING'',''SUCCEEDED'',''FAILED'',''UNKNOWN'',''EXPIRED''))')
);
SET @frozen_plan_ddl = IF(@frozen_plan_ddl='', 'DO 0',
 CONCAT('ALTER TABLE plugin_install_attempts ', @frozen_plan_ddl));
PREPARE frozen_plan_stmt FROM @frozen_plan_ddl;
EXECUTE frozen_plan_stmt;
DEALLOCATE PREPARE frozen_plan_stmt;
-- +migrate StatementEnd
-- Historical rows without a frozen payload default to UNKNOWN. Never UPDATE
-- rows on replay: later terminal states, deadlines, plans and receipts survive.

-- +migrate Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Frozen installation plan downgrade refused';
