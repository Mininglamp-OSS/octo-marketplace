-- +migrate Up
-- A zero-edge version needs explicit verification just as a nonempty one does.
CREATE TABLE plugin_version_reference_index_state (
  source_plugin_id VARCHAR(64) NOT NULL,
  source_version_id VARCHAR(64) NOT NULL,
  relation_count INT UNSIGNED NOT NULL,
  snapshot_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  verified_at DATETIME(3) NOT NULL,
  PRIMARY KEY (source_version_id),
  CONSTRAINT fk_plugin_reference_index_state_source FOREIGN KEY (source_plugin_id,source_version_id)
    REFERENCES plugin_versions (plugin_id,version_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE plugin_version_reference_index_state;
