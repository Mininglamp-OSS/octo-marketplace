-- +migrate Up

-- MySQL DDL commits independently from the migration bookkeeping transaction.
-- A retry after a later statement fails must therefore tolerate the column and
-- index already existing.
SET @add_skill_tag_id = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE skill_tags ADD COLUMN id BIGINT NOT NULL AUTO_INCREMENT FIRST, ADD UNIQUE KEY uk_skill_tags_id (id)',
    'SELECT 1'
  )
  FROM information_schema.columns
  WHERE table_schema = DATABASE()
    AND table_name = 'skill_tags'
    AND column_name = 'id'
);
PREPARE add_skill_tag_id_stmt FROM @add_skill_tag_id;
EXECUTE add_skill_tag_id_stmt;
DEALLOCATE PREPARE add_skill_tag_id_stmt;

SET @add_skill_tag_id_index = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE skill_tags ADD UNIQUE KEY uk_skill_tags_id (id)',
    'SELECT 1'
  )
  FROM information_schema.statistics
  WHERE table_schema = DATABASE()
    AND table_name = 'skill_tags'
    AND index_name = 'uk_skill_tags_id'
);
PREPARE add_skill_tag_id_index_stmt FROM @add_skill_tag_id_index;
EXECUTE add_skill_tag_id_index_stmt;
DEALLOCATE PREPARE add_skill_tag_id_index_stmt;

-- Ensure every historical string tag in skills.tags has a tag row.
INSERT INTO skill_tags (space_id, name, created_by, created_at, updated_at)
SELECT DISTINCT
  s.space_id,
  jt.name,
  s.owner_id,
  s.created_at,
  s.updated_at
FROM skills s
JOIN JSON_TABLE(
  s.tags,
  '$[*]' COLUMNS (name VARCHAR(128) PATH '$')
) AS jt
WHERE JSON_TYPE(s.tags) = 'ARRAY'
  AND JSON_LENGTH(s.tags) > 0
  AND JSON_TYPE(JSON_EXTRACT(s.tags, '$[0]')) = 'STRING'
  AND jt.name IS NOT NULL
  AND jt.name <> ''
ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at);

CREATE TEMPORARY TABLE skill_tag_id_json (
  skill_id VARCHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PRIMARY KEY,
  tag_ids JSON NOT NULL
);

INSERT INTO skill_tag_id_json (skill_id, tag_ids)
SELECT
  skill_id,
  COALESCE(JSON_ARRAYAGG(tag_id), JSON_ARRAY()) AS tag_ids
FROM (
  SELECT skill_id, MIN(ord) AS ord, tag_id
  FROM (
    SELECT
      s.id AS skill_id,
      jt.ord,
      COALESCE(global_tag.id, space_tag.id) AS tag_id
    FROM skills s
    JOIN JSON_TABLE(
      s.tags,
      '$[*]' COLUMNS (
        ord FOR ORDINALITY,
        name VARCHAR(128) PATH '$'
      )
    ) AS jt
    LEFT JOIN skill_tags global_tag
      ON global_tag.space_id = ''
     AND global_tag.name COLLATE utf8mb4_unicode_ci =
         jt.name COLLATE utf8mb4_unicode_ci
    LEFT JOIN skill_tags space_tag
      ON space_tag.space_id COLLATE utf8mb4_unicode_ci =
         s.space_id COLLATE utf8mb4_unicode_ci
     AND space_tag.name COLLATE utf8mb4_unicode_ci =
         jt.name COLLATE utf8mb4_unicode_ci
    WHERE JSON_TYPE(s.tags) = 'ARRAY'
      AND JSON_LENGTH(s.tags) > 0
      AND JSON_TYPE(JSON_EXTRACT(s.tags, '$[0]')) = 'STRING'
      AND jt.name IS NOT NULL
      AND jt.name <> ''
      AND COALESCE(global_tag.id, space_tag.id) IS NOT NULL
  ) AS raw_resolved
  GROUP BY skill_id, tag_id
) AS resolved
GROUP BY skill_id;

UPDATE skills s
LEFT JOIN skill_tag_id_json m
  ON m.skill_id COLLATE utf8mb4_unicode_ci =
     s.id COLLATE utf8mb4_unicode_ci
SET s.tags = COALESCE(m.tag_ids, JSON_ARRAY())
WHERE JSON_TYPE(s.tags) = 'ARRAY'
  AND JSON_LENGTH(s.tags) > 0
  AND JSON_TYPE(JSON_EXTRACT(s.tags, '$[0]')) = 'STRING';

DROP TEMPORARY TABLE skill_tag_id_json;

-- +migrate Down

CREATE TEMPORARY TABLE skill_tag_name_json (
  skill_id VARCHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PRIMARY KEY,
  tag_names JSON NOT NULL
);

INSERT INTO skill_tag_name_json (skill_id, tag_names)
SELECT
  skill_id,
  COALESCE(JSON_ARRAYAGG(tag_name), JSON_ARRAY()) AS tag_names
FROM (
  SELECT skill_id, MIN(ord) AS ord, tag_name
  FROM (
    SELECT
      s.id AS skill_id,
      jt.ord,
      st.name AS tag_name
    FROM skills s
    JOIN JSON_TABLE(
      s.tags,
      '$[*]' COLUMNS (
        ord FOR ORDINALITY,
        tag_id BIGINT PATH '$'
      )
    ) AS jt
    JOIN skill_tags st ON st.id = jt.tag_id
    WHERE JSON_TYPE(s.tags) = 'ARRAY'
      AND JSON_LENGTH(s.tags) > 0
      AND JSON_TYPE(JSON_EXTRACT(s.tags, '$[0]')) IN ('INTEGER', 'DOUBLE')
  ) AS raw_resolved
  GROUP BY skill_id, tag_name
) AS resolved
GROUP BY skill_id;

UPDATE skills s
LEFT JOIN skill_tag_name_json m
  ON m.skill_id COLLATE utf8mb4_unicode_ci =
     s.id COLLATE utf8mb4_unicode_ci
SET s.tags = COALESCE(m.tag_names, JSON_ARRAY())
WHERE JSON_TYPE(s.tags) = 'ARRAY'
  AND JSON_LENGTH(s.tags) > 0
  AND JSON_TYPE(JSON_EXTRACT(s.tags, '$[0]')) IN ('INTEGER', 'DOUBLE');

DROP TEMPORARY TABLE skill_tag_name_json;

ALTER TABLE skill_tags
  DROP KEY uk_skill_tags_id,
  DROP COLUMN id;
