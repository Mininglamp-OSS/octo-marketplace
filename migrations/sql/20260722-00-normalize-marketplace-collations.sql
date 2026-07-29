-- +migrate Up

-- Preflight the unique text keys under the target PAD SPACE collation before
-- changing any persistent table. A collision fails while only temporary
-- tables exist, so MySQL's per-DDL implicit commits cannot leave the schema
-- half converted.
CREATE TEMPORARY TABLE collation_guard_categories (
  name VARCHAR(64) COLLATE utf8mb4_unicode_ci PRIMARY KEY
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
INSERT INTO collation_guard_categories (name)
SELECT TRIM(TRAILING ' ' FROM name) FROM categories WHERE deleted_at IS NULL;

CREATE TEMPORARY TABLE collation_guard_skills (
  owner_id VARCHAR(64) COLLATE utf8mb4_unicode_ci,
  space_id VARCHAR(64) COLLATE utf8mb4_unicode_ci,
  name VARCHAR(128) COLLATE utf8mb4_unicode_ci,
  PRIMARY KEY (owner_id, space_id, name)
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
INSERT INTO collation_guard_skills (owner_id, space_id, name)
SELECT owner_id, space_id, TRIM(TRAILING ' ' FROM name)
FROM skills
WHERE is_deleted = 0;

CREATE TEMPORARY TABLE collation_guard_skill_tags (
  space_id VARCHAR(64) COLLATE utf8mb4_unicode_ci,
  name VARCHAR(128) COLLATE utf8mb4_unicode_ci,
  PRIMARY KEY (space_id, name)
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
INSERT INTO collation_guard_skill_tags (space_id, name)
SELECT space_id, TRIM(TRAILING ' ' FROM name) FROM skill_tags;

CREATE TEMPORARY TABLE collation_guard_skill_versions (
  skill_id VARCHAR(36) COLLATE utf8mb4_unicode_ci,
  version VARCHAR(32) COLLATE utf8mb4_unicode_ci,
  PRIMARY KEY (skill_id, version)
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
INSERT INTO collation_guard_skill_versions (skill_id, version)
SELECT skill_id, TRIM(TRAILING ' ' FROM version) FROM skill_versions;

-- The remaining converted text keys are generated identifiers: UUID primary
-- keys, resource_type enum values, resource IDs, and flush UUIDs. Application
-- writes cannot produce PAD-SPACE variants for those keys.
DROP TEMPORARY TABLE collation_guard_skill_versions;
DROP TEMPORARY TABLE collation_guard_skill_tags;
DROP TEMPORARY TABLE collation_guard_skills;
DROP TEMPORARY TABLE collation_guard_categories;

ALTER DATABASE CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

ALTER TABLE categories
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

ALTER TABLE skills
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

ALTER TABLE parse_tasks
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

ALTER TABLE skill_tags
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

ALTER TABLE skill_versions
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

ALTER TABLE resource_metrics
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

ALTER TABLE resource_metric_flushes
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- +migrate Down

-- Keep normalized collations on rollback. Restoring MySQL 8's server default
-- would reintroduce mixed-collation failures in marketplace joins, and the
-- original per-table collations cannot be reconstructed safely.
SELECT 1;
