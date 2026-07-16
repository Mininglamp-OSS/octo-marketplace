-- +migrate Up
CREATE TABLE IF NOT EXISTS skill_versions (
  id VARCHAR(36) NOT NULL PRIMARY KEY,
  skill_id VARCHAR(36) NOT NULL,
  version VARCHAR(32) NOT NULL,
  changelog TEXT,
  storage JSON,
  changed_by VARCHAR(64) NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_skill_version (skill_id, version),
  INDEX idx_skill_id (skill_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- +migrate Down
DROP TABLE IF EXISTS skill_versions;
