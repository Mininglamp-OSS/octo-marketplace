-- +migrate Up

CREATE TABLE skill_tags (
  space_id VARCHAR(64) NOT NULL,
  name VARCHAR(128) NOT NULL,
  created_by VARCHAR(64) NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (space_id, name),
  INDEX idx_skill_tags_space_updated (space_id, updated_at)
);

-- Backfill Space tag indexes from existing skill JSON tags.
INSERT INTO skill_tags (space_id, name, created_by, created_at, updated_at)
SELECT DISTINCT s.space_id, jt.name, s.owner_id, s.created_at, s.updated_at
FROM skills s
JOIN JSON_TABLE(
  s.tags,
  '$[*]' COLUMNS (name VARCHAR(128) PATH '$')
) AS jt
WHERE jt.name IS NOT NULL AND jt.name <> ''
ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at);

-- +migrate Down

DROP TABLE IF EXISTS skill_tags;
