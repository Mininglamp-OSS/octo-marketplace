-- +migrate Up
ALTER TABLE plugin_versions
  ADD COLUMN restored_from_version_id VARCHAR(64) NULL,
  ADD KEY idx_plugin_version_restore_source (plugin_id, restored_from_version_id);

-- +migrate Down
ALTER TABLE plugin_versions
  DROP INDEX idx_plugin_version_restore_source,
  DROP COLUMN restored_from_version_id;
