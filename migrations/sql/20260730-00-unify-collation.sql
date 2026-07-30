-- +migrate Up

-- 统一所有表和库的 CHARACTER SET/COLLATE 到 utf8mb4 / utf8mb4_0900_ai_ci（NO PAD, UCA 9.0）。
-- 历史 migration 中各表显式/隐式使用了不同 collation（general_ci / unicode_ci / 0900_ai_ci），
-- JOIN 时产生 ERROR 1267。本 migration 是终端修复，不修改历史 migration。
--
-- Preflight 护栏：在执行 ALTER CONVERT 前，对 5 张含唯一键的业务表创建
-- utf8mb4_0900_ai_ci 临时表并 INSERT…SELECT，若历史数据在目标 collation 下
-- 产生唯一键冲突（字符权重差异导致），INSERT 在临时表阶段即失败（ERROR 1062），
-- 未落任何持久 DDL，不会出现半转换。

CREATE TEMPORARY TABLE collation_guard_categories (
  name VARCHAR(64) COLLATE utf8mb4_0900_ai_ci PRIMARY KEY
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
INSERT INTO collation_guard_categories (name)
SELECT name FROM categories WHERE deleted_at IS NULL;

CREATE TEMPORARY TABLE collation_guard_skills (
  owner_id VARCHAR(64) COLLATE utf8mb4_0900_ai_ci,
  space_id VARCHAR(64) COLLATE utf8mb4_0900_ai_ci,
  name VARCHAR(128) COLLATE utf8mb4_0900_ai_ci,
  PRIMARY KEY (owner_id, space_id, name)
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
INSERT INTO collation_guard_skills (owner_id, space_id, name)
SELECT owner_id, space_id, name FROM skills WHERE is_deleted = 0;

CREATE TEMPORARY TABLE collation_guard_skill_tags (
  space_id VARCHAR(64) COLLATE utf8mb4_0900_ai_ci,
  name VARCHAR(128) COLLATE utf8mb4_0900_ai_ci,
  PRIMARY KEY (space_id, name)
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
INSERT INTO collation_guard_skill_tags (space_id, name)
SELECT space_id, name FROM skill_tags;

CREATE TEMPORARY TABLE collation_guard_skill_versions (
  skill_id VARCHAR(36) COLLATE utf8mb4_0900_ai_ci,
  version VARCHAR(32) COLLATE utf8mb4_0900_ai_ci,
  PRIMARY KEY (skill_id, version)
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
INSERT INTO collation_guard_skill_versions (skill_id, version)
SELECT skill_id, version FROM skill_versions;

CREATE TEMPORARY TABLE collation_guard_mcp_servers (
  owner_uid VARCHAR(64) COLLATE utf8mb4_0900_ai_ci,
  space_id VARCHAR(64) COLLATE utf8mb4_0900_ai_ci,
  name VARCHAR(128) COLLATE utf8mb4_0900_ai_ci,
  PRIMARY KEY (owner_uid, space_id, name)
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
INSERT INTO collation_guard_mcp_servers (owner_uid, space_id, name)
SELECT owner_uid, space_id, name FROM mcp_servers WHERE deleted_at IS NULL;

DROP TEMPORARY TABLE collation_guard_mcp_servers;
DROP TEMPORARY TABLE collation_guard_skill_versions;
DROP TEMPORARY TABLE collation_guard_skill_tags;
DROP TEMPORARY TABLE collation_guard_skills;
DROP TEMPORARY TABLE collation_guard_categories;

-- 固定库默认 collation，防止后续未显式写 COLLATE 的新表回退到
-- MySQL 8 server 默认值，再次引入混合态。
ALTER DATABASE CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

ALTER TABLE categories
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skills
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE parse_tasks
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skill_tags
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skill_versions
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE resource_metrics
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE resource_metric_flushes
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE mcp_servers
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE gorp_migrations
  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- +migrate Down

-- collation 无法安全回滚至原始混合态，保持新值。
SELECT 1;
