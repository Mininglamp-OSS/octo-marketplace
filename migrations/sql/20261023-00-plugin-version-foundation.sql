-- +migrate Up
-- No backfill: existing saves are not evidence of formal publication.
ALTER TABLE plugin_versions
  ADD COLUMN declared_version VARCHAR(64) NULL,
  ADD COLUMN snapshot_schema_version SMALLINT UNSIGNED NULL,
  ADD COLUMN artifacts_json JSON NULL,
  ADD UNIQUE KEY uq_plugin_versions_owner (plugin_id, version_id),
  ADD CONSTRAINT chk_plugin_version_artifacts
    CHECK (artifacts_json IS NULL OR JSON_TYPE(artifacts_json) = 'ARRAY');

CREATE TABLE plugin_version_releases (
  plugin_id VARCHAR(64) NOT NULL,
  version_id VARCHAR(64) NOT NULL,
  version_label VARCHAR(64) NOT NULL,
  released_by VARCHAR(64) NOT NULL,
  released_at DATETIME(3) NOT NULL,
  PRIMARY KEY (plugin_id, version_id),
  UNIQUE KEY uq_plugin_release_version (version_id),
  UNIQUE KEY uq_plugin_release_label (plugin_id, version_label),
  CONSTRAINT fk_plugin_release_snapshot FOREIGN KEY (plugin_id, version_id)
    REFERENCES plugin_versions (plugin_id, version_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Append-only formal release identities; not every save is a release';

ALTER TABLE plugins
  ADD COLUMN published_version_id VARCHAR(64) NULL,
  ADD KEY idx_plugin_published_version (plugin_id, published_version_id),
  ADD CONSTRAINT fk_plugin_published_release FOREIGN KEY (plugin_id, published_version_id)
    REFERENCES plugin_version_releases (plugin_id, version_id);

-- +migrate Down
-- Isolated verification only: production rollback must retain version data.
ALTER TABLE plugins DROP FOREIGN KEY fk_plugin_published_release,
  DROP INDEX idx_plugin_published_version, DROP COLUMN published_version_id;
DROP TABLE plugin_version_releases;
ALTER TABLE plugin_versions DROP CONSTRAINT chk_plugin_version_artifacts,
  DROP INDEX uq_plugin_versions_owner,
  DROP COLUMN artifacts_json, DROP COLUMN snapshot_schema_version,
  DROP COLUMN declared_version;
