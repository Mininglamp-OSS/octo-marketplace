-- +migrate Up
-- 统一所有表到 utf8mb4_0900_ai_ci（MySQL 8 默认），修复跨表 JOIN 时 collation 不兼容导致的 500 错误。
-- CONVERT 前对含用户输入文本唯一键的表做碰撞预检：
--   - PAD SPACE → NO PAD：尾空格从等价变为区分（'QA' 与 'QA ' 不再冲突，唯一性放松，不会触发错误）；
--     反向（NO PAD → PAD SPACE）才会产生冲突，本迁移方向不存在该问题
--   - UCA 版本差异：utf8mb4_unicode_ci = UCA 5.2.0，utf8mb4_0900_ai_ci = UCA 9.0.0；
--     U+00AD SOFT HYPHEN 等字符在 unicode_ci 下区分、0900_ai_ci 下权重可忽略，可能产生唯一键冲突
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

-- mcp_servers.name 为用户自由输入（可含 CJK/emoji，无 Unicode normalization），UCA-ignorable 字符存在冲突风险；
-- space_id IS NOT NULL 过滤系统 MCP（system 行 space_id=NULL，多 NULL 在 MySQL 唯一索引中互不等价，不会冲突）；
-- slug_live 有 ^[a-z0-9-]{1,64}$ ASCII 白名单校验，无需 guard
CREATE TEMPORARY TABLE collation_guard_mcp_servers (
  owner_uid VARCHAR(64) COLLATE utf8mb4_0900_ai_ci,
  space_id VARCHAR(64) COLLATE utf8mb4_0900_ai_ci,
  name VARCHAR(128) COLLATE utf8mb4_0900_ai_ci,
  PRIMARY KEY (owner_uid, space_id, name)
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
INSERT INTO collation_guard_mcp_servers (owner_uid, space_id, name)
SELECT owner_uid, space_id, name
FROM mcp_servers
WHERE deleted_at IS NULL AND space_id IS NOT NULL;

-- 其余表（parse_tasks/resource_metrics/resource_metric_flushes）唯一键均为 ASCII 标识符或外键，
-- gorp_migrations.id 是文件名，不存在 UCA/PAD 冲突风险，无需预检
DROP TEMPORARY TABLE collation_guard_mcp_servers;
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
