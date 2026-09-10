-- +migrate Up
-- MySQL DDL commits independently of sql-migrate's bookkeeping transaction. If
-- the process exits after ALTER commits but before gorp_migrations is updated,
-- replay must converge instead of failing with duplicate-column error 1060.
-- Do not position this column with AFTER/FIRST or add another table alteration:
-- the explicit algorithm makes unsupported metadata-only DDL fail rather than
-- silently rebuilding the plugins table.
SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
     WHERE table_schema = DATABASE()
       AND table_name = 'plugins'
       AND column_name = 'rating') > 0,
  'DO 0',
  'ALTER TABLE `plugins` ADD COLUMN `rating` TINYINT UNSIGNED NULL, ALGORITHM=INSTANT');
PREPARE add_plugin_rating FROM @ddl;
EXECUTE add_plugin_rating;
DEALLOCATE PREPARE add_plugin_rating;

-- +migrate Down
ALTER TABLE `plugins`
  DROP COLUMN `rating`;
