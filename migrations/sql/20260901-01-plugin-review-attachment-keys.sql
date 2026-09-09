-- +migrate Up
-- Freeze the storage-attachment sidecar on a review request so a zip-submitted
-- skill upgrade (which spills binary/oversize files to the managed prefix at
-- submit time) can carry its complete snapshot to approval. The column mirrors
-- plugins.attachment_keys_json exactly: JSON NULL, a path -> managed-object-key
-- map for any storage attachments the frozen package references. Without it,
-- approving a zip-submitted review would apply a package whose storage_uri
-- paths the live row's sidecar had no entry for.
-- Guarded against self-replay: MySQL implicitly commits the ALTER, so a crash
-- between it and the gorp_migrations insert replays this file and a bare ADD COLUMN
-- dies on ERROR 1060, leaving a boot that cannot recover without hand-editing
-- gorp_migrations. MySQL has no ADD COLUMN IF NOT EXISTS, so the existence check is
-- explicit. Same shape as 20260902-00 and 20260902-03; see -03's header for why the
-- DROP+ADD trick that makes 20260902-02 self-idempotent does not transfer to a
-- statement that CREATES rather than rebuilds.
SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
     WHERE table_schema = DATABASE()
       AND table_name = 'plugin_review_requests'
       AND column_name = 'attachment_keys_json') > 0,
  'DO 0',
  'ALTER TABLE `plugin_review_requests` ADD COLUMN `attachment_keys_json` JSON NULL AFTER `plugin_json`');
PREPARE add_attachment_keys FROM @ddl;
EXECUTE add_attachment_keys;
DEALLOCATE PREPARE add_attachment_keys;

-- +migrate Down
ALTER TABLE `plugin_review_requests` DROP COLUMN `attachment_keys_json`;
