-- +migrate Up notransaction
-- 统一所有业务表字符集/排序规则到 utf8mb4_0900_ai_ci，消除 JOIN 时的 collation conflict。
-- 执行顺序：碰撞预检（存储过程，失败直接 SIGNAL，不执行 DDL）→ ALTER 转换 → 锁库默认值
--
-- gorp_migrations 是 sql-migrate 框架内部表，无 JOIN 收益，且可能触发 utf8mb3→utf8mb4
-- 隐式转换，不纳入本次修复范围。

-- 碰撞预检：utf8mb4_0900_ai_ci 将全角/半角、ß/ss、æ/ae 等视为相等，若业务数据中已存在
-- 这类等值对，CONVERT TO 会触发 ERROR 1062 Duplicate entry，且 ALTER 隐式 commit
-- 导致半转换状态。预检在存储过程内完成，通过前不执行任何 DDL。
-- rubenv/sql-migrate 以分号分割语句，MySQL 不允许顶层 IF，故用存储过程包裹。
-- 内层 SELECT 一律返回常量 1，避免 ONLY_FULL_GROUP_BY 错误（MySQL 8.0 默认 sql_mode）。
-- mcp_servers 预检排除 space_id IS NULL 的行：MySQL UNIQUE 索引对含 NULL 的元组允许多行。
-- 所有 SIGNAL 消息控制在 MySQL 128 字符 MESSAGE_TEXT 上限内。

-- +migrate StatementBegin
DROP PROCEDURE IF EXISTS collation_preflight;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE PROCEDURE collation_preflight()
BEGIN
  DECLARE v_dup BIGINT;

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
  -- space_id 为 NULL 时 MySQL UNIQUE 允许多行，排除
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
  -- space_id 为 NULL 时 MySQL UNIQUE 允许多行，排除
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

  -- resource_metric_flushes: PRIMARY KEY (flush_id) — UUID/ULID 风格，理论无冲突，安全起见预检
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

  -- parse_tasks: PRIMARY KEY (id) — UUID，理论无冲突，安全起见预检
  SELECT COUNT(*) INTO v_dup FROM (
    SELECT 1 FROM parse_tasks GROUP BY id COLLATE utf8mb4_0900_ai_ci HAVING COUNT(*) > 1
  ) t;
  IF v_dup > 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'parse_tasks has duplicate primary keys under 0900_ai_ci; de-duplicate before running this migration';
  END IF;
END;
-- +migrate StatementEnd

CALL collation_preflight();
DROP PROCEDURE IF EXISTS collation_preflight;

-- =======================
-- DDL：所有预检通过后才执行，8 张业务表统一转换
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
