-- +migrate Up
ALTER TABLE plugin_visibility_reviews
  ADD COLUMN group_id VARCHAR(64) NULL,
  ADD COLUMN expected_revision CHAR(64) NULL,
  ADD KEY idx_visibility_group (group_id);
CREATE TABLE plugin_dependency_suspensions (
  plugin_id VARCHAR(64) NOT NULL PRIMARY KEY,
  space_id VARCHAR(64) NOT NULL,
  published_version_id VARCHAR(64) NULL,
  suspended_at DATETIME(6) NOT NULL,
  KEY idx_dependency_suspension_space (space_id),
  CONSTRAINT fk_dependency_suspension_plugin FOREIGN KEY (plugin_id) REFERENCES plugins(plugin_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +migrate Down
DROP TABLE plugin_dependency_suspensions;
ALTER TABLE plugin_visibility_reviews DROP KEY idx_visibility_group, DROP COLUMN expected_revision, DROP COLUMN group_id;
