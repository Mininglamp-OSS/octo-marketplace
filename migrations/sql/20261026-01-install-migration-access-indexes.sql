-- +migrate Up
-- Match scoped resource/time lookup, active batch discovery and reverse apply
-- order. MySQL commits each DDL separately: guards allow replay after a crash
-- between any index creation and the sql-migrate receipt write.
SET @octo_install_migration_index_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.STATISTICS
     WHERE table_schema = DATABASE() AND table_name = 'plugin_install_attempts'
       AND index_name = 'idx_plugin_install_resource_time') > 0,
  'DO 0',
  'ALTER TABLE `plugin_install_attempts` ADD INDEX `idx_plugin_install_resource_time` (space_id, plugin_id, created_at), ALGORITHM=INPLACE, LOCK=NONE');
PREPARE octo_install_migration_index FROM @octo_install_migration_index_ddl;
EXECUTE octo_install_migration_index;
DEALLOCATE PREPARE octo_install_migration_index;

SET @octo_install_migration_index_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.STATISTICS
     WHERE table_schema = DATABASE() AND table_name = 'plugin_stock_migration_batches'
       AND index_name = 'idx_stock_migration_batch_state') > 0,
  'DO 0',
  'ALTER TABLE `plugin_stock_migration_batches` ADD INDEX `idx_stock_migration_batch_state` (state, batch_id), ALGORITHM=INPLACE, LOCK=NONE');
PREPARE octo_install_migration_index FROM @octo_install_migration_index_ddl;
EXECUTE octo_install_migration_index;
DEALLOCATE PREPARE octo_install_migration_index;

SET @octo_install_migration_index_ddl := IF(
  (SELECT COUNT(*) FROM information_schema.STATISTICS
     WHERE table_schema = DATABASE() AND table_name = 'plugin_stock_migration_roots'
       AND index_name = 'idx_stock_migration_root_applied') > 0,
  'DO 0',
  'ALTER TABLE `plugin_stock_migration_roots` ADD INDEX `idx_stock_migration_root_applied` (batch_id, applied_at, root_plugin_id), ALGORITHM=INPLACE, LOCK=NONE');
PREPARE octo_install_migration_index FROM @octo_install_migration_index_ddl;
EXECUTE octo_install_migration_index;
DEALLOCATE PREPARE octo_install_migration_index;

-- +migrate Down
-- Refuse before any schema is changed, including on an already-upgraded DB.
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Stock migration audit schema downgrade refused';
