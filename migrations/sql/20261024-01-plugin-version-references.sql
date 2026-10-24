-- +migrate Up
-- Nullable for legacy edges; never infer historical versions from current heads.
ALTER TABLE plugin_relations ADD COLUMN target_version_id VARCHAR(64) NULL;
CREATE TABLE plugin_version_relation_refs (
  source_plugin_id VARCHAR(64) NOT NULL,
  source_version_id VARCHAR(64) NOT NULL,
  relation_id VARCHAR(64) NOT NULL,
  relation_type VARCHAR(64) NOT NULL,
  target_plugin_id VARCHAR(64) NOT NULL,
  target_version_id VARCHAR(64) NULL,
  PRIMARY KEY (source_version_id, relation_id),
  KEY idx_plugin_version_ref_target (target_plugin_id, target_version_id),
  CONSTRAINT fk_plugin_version_ref_source FOREIGN KEY (source_plugin_id, source_version_id)
    REFERENCES plugin_versions (plugin_id, version_id),
  CONSTRAINT fk_plugin_version_ref_target FOREIGN KEY (target_plugin_id, target_version_id)
    REFERENCES plugin_version_releases (plugin_id, version_id),
  CONSTRAINT chk_plugin_version_ref_type CHECK (
    (relation_type IN ('expert_skill','expert_team_expert') AND target_version_id IS NOT NULL)
    OR (relation_type='expert_connector' AND target_version_id IS NULL)
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
-- Isolated verification only; production rollback retains immutable history.
DROP TABLE plugin_version_relation_refs;
ALTER TABLE plugin_relations DROP COLUMN target_version_id;
