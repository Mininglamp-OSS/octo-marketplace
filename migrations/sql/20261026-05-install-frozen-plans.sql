-- +migrate Up
-- MySQL commits DDL independently of sql-migrate's receipt. Guard every column
-- and the CHECK separately so a complete or interrupted upgrade can replay.
-- Never drop/recreate columns or reset existing plans, results, states or leases.
SET @octo_install_frozen_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
     WHERE table_schema = DATABASE() AND table_name = 'plugin_install_attempts'
       AND column_name = 'frozen_plan_json') > 0,
  'DO 0',
  'ALTER TABLE `plugin_install_attempts` ADD COLUMN `frozen_plan_json` JSON NULL');
PREPARE octo_install_frozen FROM @octo_install_frozen_ddl;
EXECUTE octo_install_frozen;
DEALLOCATE PREPARE octo_install_frozen;

SET @octo_install_frozen_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
     WHERE table_schema = DATABASE() AND table_name = 'plugin_install_attempts'
       AND column_name = 'hmac_key_id') > 0,
  'DO 0',
  'ALTER TABLE `plugin_install_attempts` ADD COLUMN `hmac_key_id` VARCHAR(64) NOT NULL DEFAULT ''''');
PREPARE octo_install_frozen FROM @octo_install_frozen_ddl;
EXECUTE octo_install_frozen;
DEALLOCATE PREPARE octo_install_frozen;

SET @octo_install_frozen_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
     WHERE table_schema = DATABASE() AND table_name = 'plugin_install_attempts'
       AND column_name = 'intent_digest') > 0,
  'DO 0',
  'ALTER TABLE `plugin_install_attempts` ADD COLUMN `intent_digest` CHAR(64) NOT NULL DEFAULT ''''');
PREPARE octo_install_frozen FROM @octo_install_frozen_ddl;
EXECUTE octo_install_frozen;
DEALLOCATE PREPARE octo_install_frozen;

SET @octo_install_frozen_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
     WHERE table_schema = DATABASE() AND table_name = 'plugin_install_attempts'
       AND column_name = 'attempt_state') > 0,
  'DO 0',
  'ALTER TABLE `plugin_install_attempts` ADD COLUMN `attempt_state` VARCHAR(16) NOT NULL DEFAULT ''UNKNOWN''');
PREPARE octo_install_frozen FROM @octo_install_frozen_ddl;
EXECUTE octo_install_frozen;
DEALLOCATE PREPARE octo_install_frozen;

SET @octo_install_frozen_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
     WHERE table_schema = DATABASE() AND table_name = 'plugin_install_attempts'
       AND column_name = 'had_unknown') > 0,
  'DO 0',
  'ALTER TABLE `plugin_install_attempts` ADD COLUMN `had_unknown` BOOLEAN NOT NULL DEFAULT TRUE');
PREPARE octo_install_frozen FROM @octo_install_frozen_ddl;
EXECUTE octo_install_frozen;
DEALLOCATE PREPARE octo_install_frozen;

SET @octo_install_frozen_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
     WHERE table_schema = DATABASE() AND table_name = 'plugin_install_attempts'
       AND column_name = 'lease_token') > 0,
  'DO 0',
  'ALTER TABLE `plugin_install_attempts` ADD COLUMN `lease_token` VARCHAR(64) NULL');
PREPARE octo_install_frozen FROM @octo_install_frozen_ddl;
EXECUTE octo_install_frozen;
DEALLOCATE PREPARE octo_install_frozen;

SET @octo_install_frozen_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
     WHERE table_schema = DATABASE() AND table_name = 'plugin_install_attempts'
       AND column_name = 'lease_until') > 0,
  'DO 0',
  'ALTER TABLE `plugin_install_attempts` ADD COLUMN `lease_until` DATETIME(3) NULL');
PREPARE octo_install_frozen FROM @octo_install_frozen_ddl;
EXECUTE octo_install_frozen;
DEALLOCATE PREPARE octo_install_frozen;

SET @octo_install_frozen_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
     WHERE table_schema = DATABASE() AND table_name = 'plugin_install_attempts'
       AND column_name = 'retry_until') > 0,
  'DO 0',
  'ALTER TABLE `plugin_install_attempts` ADD COLUMN `retry_until` DATETIME(3) NULL');
PREPARE octo_install_frozen FROM @octo_install_frozen_ddl;
EXECUTE octo_install_frozen;
DEALLOCATE PREPARE octo_install_frozen;

SET @octo_install_frozen_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
     WHERE constraint_schema = DATABASE() AND table_name = 'plugin_install_attempts'
       AND constraint_name = 'chk_install_attempt_state' AND constraint_type = 'CHECK') > 0,
  'DO 0',
  'ALTER TABLE `plugin_install_attempts` ADD CONSTRAINT `chk_install_attempt_state` CHECK (attempt_state IN (''PREPARED'',''DISPATCHING'',''SUCCEEDED'',''FAILED'',''UNKNOWN'',''EXPIRED''))');
PREPARE octo_install_frozen FROM @octo_install_frozen_ddl;
EXECUTE octo_install_frozen;
DEALLOCATE PREPARE octo_install_frozen;

-- Existing attempts have no frozen payload. The new column default is UNKNOWN,
-- so historical rows keep uncertainty without an UPDATE that could overwrite a
-- later terminal state on a partial/re-run migration.

-- +migrate Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Frozen installation plan downgrade refused';
