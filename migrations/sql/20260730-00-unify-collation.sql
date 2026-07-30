-- +migrate Up
-- 统一所有表到 utf8mb4_0900_ai_ci（MySQL 8 默认），修复跨表 JOIN 时 collation 不兼容导致的 500 错误。
-- CONVERT 前对含文本唯一键的表做碰撞预检：utf8mb4_unicode_ci (UCA 5.2.0/PAD SPACE) 与
-- utf8mb4_0900_ai_ci (UCA 9.0.0/NO PAD) 不仅 PAD 属性不同，权重可忽略字符集也不同
-- （如 U+00AD SOFT HYPHEN 在 unicode_ci 下区分、0900_ai_ci 下等价），可能触发唯一键冲突；
-- 预检在临时表阶段即失败，不会留下半转换状态。

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
SELECT owner_id, space_id, name
FROM skills
WHERE is_deleted = 0;

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

-- 其余表的唯一键均为生成标识符（UUID/enum/主键），不会产生 collation 冲突，无需预检
DROP TEMPORARY TABLE collation_guard_skill_versions;
DROP TEMPORARY TABLE collation_guard_skill_tags;
DROP TEMPORARY TABLE collation_guard_skills;
DROP TEMPORARY TABLE collation_guard_categories;

ALTER DATABASE CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

ALTER TABLE categories CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skills CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE parse_tasks CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skill_tags CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skill_versions CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE resource_metrics CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE resource_metric_flushes CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE mcp_servers CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE gorp_migrations CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- +migrate Down
-- 前向修复迁移，回滚不还原 collation（已修复状态下回退会重新引入 JOIN 冲突）
SELECT 1;
