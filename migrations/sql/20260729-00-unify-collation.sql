-- +migrate Up notransaction
-- 统一所有业务表字符集/排序规则到 utf8mb4_0900_ai_ci，消除 JOIN 时的 collation conflict。
-- 执行顺序：预检（存储过程，失败直接 SIGNAL，不执行 DDL）→ TRIM 唯一键列首尾空格 → ALTER 转换 → 锁库默认值
--
-- gorp_migrations 是 sql-migrate 框架内部表，无 JOIN 收益，且可能触发 utf8mb3→utf8mb4
-- 隐式转换，不纳入本次修复范围。
--
-- PAD SPACE → NO PAD 说明：utf8mb4_general_ci/unicode_ci 是 PAD SPACE（尾部空格不参与比较），
-- utf8mb4_0900_ai_ci 是 NO PAD（尾部空格有意义）。转换前必须 TRIM 唯一键列首尾空格，
-- 否则已有的 "QA" 和 "QA " 会从"相等"变"不等"，破坏唯一约束语义，且可能导致孤儿行。
-- TRIM 前先做尾空格预检，存在带首尾空格的唯一键值时拒绝执行，要求人工处理。
--
-- categories/skills/skill_versions/mcp_servers 主键 id 使用 Crockford base32 ULID（字母表 0-9A-Z 不含 ILOU），
-- 在 general_ci 与 0900_ai_ci 下无新等价对，不做重复键预检；parse_tasks/resource_metric_flushes 同为
-- UUID/ULID 主键，安全起见仍保留重复键预检。
-- 所有 SIGNAL 消息控制在 MySQL 128 字符 MESSAGE_TEXT 上限内。
-- rubenv/sql-migrate 以分号分割语句，MySQL 不允许顶层 IF，故用存储过程包裹预检逻辑。
-- 内层 SELECT 一律返回常量 1，避免 ONLY_FULL_GROUP_BY 错误（MySQL 8.0 默认 sql_mode）。
-- mcp_servers 空间唯一键预检排除 space_id IS NULL 的行：MySQL UNIQUE 索引对含 NULL 的元组允许多行。

-- +migrate StatementBegin
DROP PROCEDURE IF EXISTS collation_preflight;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE PROCEDURE collation_preflight()
BEGIN
  DECLARE v_dup BIGINT;

  -- === 重复键预检：utf8mb4_0900_ai_ci 将全角/半角、ß/ss、æ/ae 等视为相等，
  -- 若业务数据中已存在这类等值对，CONVERT TO 会触发 ERROR 1062 Duplicate entry。
  -- 预检通过前不执行任何 DDL。===

  -- skill_tags: PRIMARY KEY (space_id, name)
  SELECT COUNT(*) INTO v_dup FROM (
    SELECT 1
    FROM skill_tags
    GROUP BY space_id COLLATE utf8mb4_0900_ai_ci, name COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t;
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'skill_tags has duplicate keys under 0900_ai_ci; de-duplicate before running this migration';
  END IF;

  -- categories: UNIQUE KEY uk_categories_name_live (name_live)
  -- name_live = IF(deleted_at IS NULL, name, NULL) STORED，仅未删除行参与唯一约束
  SELECT COUNT(*) INTO v_dup FROM (
    SELECT 1
    FROM categories
    WHERE name_live IS NOT NULL
    GROUP BY name_live COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t;
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'categories has duplicate live names under 0900_ai_ci; de-duplicate before running this migration';
  END IF;

  -- skills: UNIQUE KEY uq_skill_owner_space_name_live (owner_id, space_id, name_live)
  -- name_live = IF(is_deleted = 0, name, NULL) STORED，仅 is_deleted=0 行参与唯一约束
  SELECT COUNT(*) INTO v_dup FROM (
    SELECT 1
    FROM skills
    WHERE is_deleted = 0
    GROUP BY owner_id COLLATE utf8mb4_0900_ai_ci, space_id COLLATE utf8mb4_0900_ai_ci, name_live COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t;
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'skills has duplicate live owner/space/name keys under 0900_ai_ci; de-duplicate first';
  END IF;

  -- skill_versions: UNIQUE KEY uk_skill_version (skill_id, version)
  SELECT COUNT(*) INTO v_dup FROM (
    SELECT 1
    FROM skill_versions
    GROUP BY skill_id COLLATE utf8mb4_0900_ai_ci, version COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t;
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'skill_versions has duplicate (skill_id,version) under 0900_ai_ci; de-duplicate before running';
  END IF;

  -- mcp_servers: UNIQUE KEY uq_owner_space_name_live (owner_uid, space_id, name_live)
  SELECT COUNT(*) INTO v_dup FROM (
    SELECT 1
    FROM mcp_servers
    WHERE name_live IS NOT NULL AND space_id IS NOT NULL
    GROUP BY owner_uid COLLATE utf8mb4_0900_ai_ci, space_id COLLATE utf8mb4_0900_ai_ci, name_live COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t;
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'mcp_servers has duplicate owner/space/name keys under 0900_ai_ci; de-duplicate first';
  END IF;

  -- mcp_servers: UNIQUE KEY uq_space_slug_live (space_id, slug_live)
  SELECT COUNT(*) INTO v_dup FROM (
    SELECT 1
    FROM mcp_servers
    WHERE slug_live IS NOT NULL AND space_id IS NOT NULL
    GROUP BY space_id COLLATE utf8mb4_0900_ai_ci, slug_live COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t;
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'mcp_servers has duplicate (space_id,slug) under 0900_ai_ci; de-duplicate before running';
  END IF;

  -- resource_metrics: PRIMARY KEY (resource_type, resource_id)
  SELECT COUNT(*) INTO v_dup FROM (
    SELECT 1
    FROM resource_metrics
    GROUP BY resource_type COLLATE utf8mb4_0900_ai_ci, resource_id COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t;
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'resource_metrics has duplicate primary keys under 0900_ai_ci; de-duplicate before running';
  END IF;

  -- resource_metric_flushes: PRIMARY KEY (flush_id) — UUID/ULID，安全起见保留预检
  SELECT COUNT(*) INTO v_dup FROM (
    SELECT 1
    FROM resource_metric_flushes
    GROUP BY flush_id COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t;
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'resource_metric_flushes has duplicate PK under 0900_ai_ci; de-duplicate before running this migration';
  END IF;

  -- parse_tasks: PRIMARY KEY (id) — UUID，安全起见保留预检
  SELECT COUNT(*) INTO v_dup FROM (
    SELECT 1 FROM parse_tasks GROUP BY id COLLATE utf8mb4_0900_ai_ci HAVING COUNT(*) > 1
  ) t;
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'parse_tasks has duplicate primary keys under 0900_ai_ci; de-duplicate before running this migration';
  END IF;

  -- === 尾空格预检：PAD SPACE → NO PAD 后首尾空格变为有意义，
  -- TRIM 后可能产生唯一键冲突，必须人工处理。
  -- 覆盖所有参与 UNIQUE 索引的字符串列（含生成列 name_live/slug_live 的基列 name/slug）。===

  -- categories.name（name_live 生成列依赖此列）
  SELECT COUNT(*) INTO v_dup FROM categories WHERE name <> TRIM(name);
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'categories.name has leading/trailing whitespace; trim manually before running this migration';
  END IF;

  -- skills.name（name_live 生成列依赖此列）
  SELECT COUNT(*) INTO v_dup FROM skills WHERE name <> TRIM(name);
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'skills.name has leading/trailing whitespace; trim manually before running this migration';
  END IF;

  -- skill_tags.name（PRIMARY KEY 成员）
  SELECT COUNT(*) INTO v_dup FROM skill_tags WHERE name <> TRIM(name);
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'skill_tags.name has leading/trailing whitespace; trim manually before running this migration';
  END IF;

  -- skill_tags.space_id（PRIMARY KEY 成员）
  SELECT COUNT(*) INTO v_dup FROM skill_tags WHERE space_id <> TRIM(space_id);
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'skill_tags.space_id has leading/trailing whitespace; trim manually before running migration';
  END IF;

  -- skill_versions.version（UNIQUE KEY 成员）
  SELECT COUNT(*) INTO v_dup FROM skill_versions WHERE version <> TRIM(version);
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'skill_versions.version has leading/trailing whitespace; trim manually before running migration';
  END IF;

  -- mcp_servers.name（name_live 生成列依赖此列）
  SELECT COUNT(*) INTO v_dup FROM mcp_servers WHERE name <> TRIM(name);
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'mcp_servers.name has leading/trailing whitespace; trim manually before running this migration';
  END IF;

  -- mcp_servers.slug（slug_live 生成列依赖此列）
  SELECT COUNT(*) INTO v_dup FROM mcp_servers WHERE slug <> TRIM(slug);
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'mcp_servers.slug has leading/trailing whitespace; trim manually before running this migration';
  END IF;
END;
-- +migrate StatementEnd

CALL collation_preflight();
DROP PROCEDURE IF EXISTS collation_preflight;

-- =======================
-- TRIM 唯一键列首尾空格，防止 PAD SPACE → NO PAD 语义变化
-- name_live/slug_live 是 STORED 生成列，TRIM 基列后自动重算
-- =======================
UPDATE categories     SET name    = TRIM(name)    WHERE name    <> TRIM(name);
UPDATE skills         SET name    = TRIM(name)    WHERE name    <> TRIM(name);
UPDATE skill_tags     SET name    = TRIM(name),
                          space_id = TRIM(space_id)
                        WHERE name <> TRIM(name) OR space_id <> TRIM(space_id);
UPDATE skill_versions SET version = TRIM(version) WHERE version <> TRIM(version);
UPDATE mcp_servers    SET name    = TRIM(name),
                          slug    = TRIM(slug)
                        WHERE name <> TRIM(name) OR slug <> TRIM(slug);

-- =======================
-- DDL：所有预检通过、TRIM 完成后才执行，8 张业务表统一转换
-- =======================
ALTER TABLE categories              CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skills                  CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE parse_tasks             CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skill_tags              CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skill_versions          CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE resource_metrics        CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE resource_metric_flushes CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE mcp_servers             CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- 锁死库默认值，防止后续 migration 漏写 COLLATE 再次出现混合状态
-- 放在 ALTER 之后：预检失败时不修改库默认值，避免半转换状态
ALTER DATABASE CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- +migrate Down notransaction
-- collation 统一是单向操作。回滚会恢复到混合 collation 状态，不推荐执行。
-- Down 方向故意 no-op（SELECT 1）：ledger 行删除，但表保持 utf8mb4_0900_ai_ci。
-- 这是安全的——所有新建表 CREATE TABLE 均已显式声明 COLLATE=utf8mb4_0900_ai_ci，
-- 不会再出现 JOIN collation conflict，无需将已转换的表恢复到旧 collation。
SELECT 1;
