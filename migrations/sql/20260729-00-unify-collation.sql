-- +migrate Up notransaction
-- 统一所有业务表字符集/排序规则到 utf8mb4_0900_ai_ci，消除 JOIN 时的 collation conflict。
-- 执行顺序：先锁库默认值 → 碰撞预检（任何唯一键冲突直接 SIGNAL，不执行 DDL）→ ALTER 转换
--
-- 注意：gorp_migrations 是 goose 框架内部表，无 JOIN 收益，且可能触发 utf8mb3→utf8mb4
-- 隐式转换，不纳入本次修复范围。

-- 锁死库默认值，防止后续 migration 漏写 COLLATE 再次出现混合状态
ALTER DATABASE CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- =======================
-- 碰撞预检：utf8mb4_0900_ai_ci 对全角/半角、ß/ss 等视为相等，若业务数据中已有
-- 这类等值对，CONVERT TO 会触发 ERROR 1062 Duplicate entry，且 ALTER 隐式 commit
-- 导致半转换状态。预检通过前不执行任何 DDL。
-- =======================

-- skill_tags: PRIMARY KEY (space_id, name)
SET @skill_tags_dup = (
  SELECT COUNT(*) FROM (
    SELECT space_id, name
    FROM skill_tags
    GROUP BY space_id COLLATE utf8mb4_0900_ai_ci, name COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t
);
SET @skill_tags_msg = IF(@skill_tags_dup = 0, 'ok',
  CONCAT('skill_tags has ', @skill_tags_dup, ' duplicate (space_id,name) pairs under utf8mb4_0900_ai_ci; de-duplicate before running this migration'));
SET @skill_tags_signal = IF(@skill_tags_dup = 0, 'SELECT 1',
  CONCAT('SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''', @skill_tags_msg, ''''));
PREPARE skill_tags_check FROM @skill_tags_signal;
EXECUTE skill_tags_check;
DEALLOCATE PREPARE skill_tags_check;

-- categories: UNIQUE KEY uk_categories_name_live (name_live), name_live = IF(deleted_at IS NULL, name, NULL)
SET @categories_dup = (
  SELECT COUNT(*) FROM (
    SELECT name_live
    FROM categories
    WHERE name_live IS NOT NULL
    GROUP BY name_live COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t
);
SET @categories_msg = IF(@categories_dup = 0, 'ok',
  CONCAT('categories has ', @categories_dup, ' duplicate live names under utf8mb4_0900_ai_ci; de-duplicate before running this migration'));
SET @categories_signal = IF(@categories_dup = 0, 'SELECT 1',
  CONCAT('SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''', @categories_msg, ''''));
PREPARE categories_check FROM @categories_signal;
EXECUTE categories_check;
DEALLOCATE PREPARE categories_check;

-- skills: UNIQUE KEY uq_skill_owner_space_name_live (owner_id, space_id, name_live)
-- name_live is generated from name; check against live rows (name_live IS NOT NULL)
SET @skills_dup = (
  SELECT COUNT(*) FROM (
    SELECT owner_id, space_id, name_live
    FROM skills
    WHERE name_live IS NOT NULL
    GROUP BY owner_id COLLATE utf8mb4_0900_ai_ci, space_id COLLATE utf8mb4_0900_ai_ci, name_live COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t
);
SET @skills_msg = IF(@skills_dup = 0, 'ok',
  CONCAT('skills has ', @skills_dup, ' duplicate live (owner_id,space_id,name) tuples under utf8mb4_0900_ai_ci; de-duplicate before running this migration'));
SET @skills_signal = IF(@skills_dup = 0, 'SELECT 1',
  CONCAT('SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''', @skills_msg, ''''));
PREPARE skills_check FROM @skills_signal;
EXECUTE skills_check;
DEALLOCATE PREPARE skills_check;

-- skill_versions: UNIQUE KEY uk_skill_version (skill_id, version); version is version string
SET @skill_versions_dup = (
  SELECT COUNT(*) FROM (
    SELECT skill_id, version
    FROM skill_versions
    GROUP BY skill_id COLLATE utf8mb4_0900_ai_ci, version COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t
);
SET @skill_versions_msg = IF(@skill_versions_dup = 0, 'ok',
  CONCAT('skill_versions has ', @skill_versions_dup, ' duplicate (skill_id,version) pairs under utf8mb4_0900_ai_ci; de-duplicate before running this migration'));
SET @skill_versions_signal = IF(@skill_versions_dup = 0, 'SELECT 1',
  CONCAT('SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''', @skill_versions_msg, ''''));
PREPARE skill_versions_check FROM @skill_versions_signal;
EXECUTE skill_versions_check;
DEALLOCATE PREPARE skill_versions_check;

-- mcp_servers: UNIQUE KEY uq_owner_space_name_live (owner_uid, space_id, name_live)
--            UNIQUE KEY uq_space_slug_live (space_id, slug_live)
SET @mcp_name_dup = (
  SELECT COUNT(*) FROM (
    SELECT owner_uid, space_id, name_live
    FROM mcp_servers
    WHERE name_live IS NOT NULL
    GROUP BY owner_uid COLLATE utf8mb4_0900_ai_ci, space_id COLLATE utf8mb4_0900_ai_ci, name_live COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t
);
SET @mcp_name_msg = IF(@mcp_name_dup = 0, 'ok',
  CONCAT('mcp_servers has ', @mcp_name_dup, ' duplicate live (owner_uid,space_id,name) tuples under utf8mb4_0900_ai_ci; de-duplicate before running this migration'));
SET @mcp_name_signal = IF(@mcp_name_dup = 0, 'SELECT 1',
  CONCAT('SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''', @mcp_name_msg, ''''));
PREPARE mcp_name_check FROM @mcp_name_signal;
EXECUTE mcp_name_check;
DEALLOCATE PREPARE mcp_name_check;

SET @mcp_slug_dup = (
  SELECT COUNT(*) FROM (
    SELECT space_id, slug_live
    FROM mcp_servers
    WHERE slug_live IS NOT NULL
    GROUP BY space_id COLLATE utf8mb4_0900_ai_ci, slug_live COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t
);
SET @mcp_slug_msg = IF(@mcp_slug_dup = 0, 'ok',
  CONCAT('mcp_servers has ', @mcp_slug_dup, ' duplicate live (space_id,slug) tuples under utf8mb4_0900_ai_ci; de-duplicate before running this migration'));
SET @mcp_slug_signal = IF(@mcp_slug_dup = 0, 'SELECT 1',
  CONCAT('SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''', @mcp_slug_msg, ''''));
PREPARE mcp_slug_check FROM @mcp_slug_signal;
EXECUTE mcp_slug_check;
DEALLOCATE PREPARE mcp_slug_check;

-- resource_metrics: PRIMARY KEY (resource_type, resource_id)
SET @resource_metrics_dup = (
  SELECT COUNT(*) FROM (
    SELECT resource_type, resource_id
    FROM resource_metrics
    GROUP BY resource_type COLLATE utf8mb4_0900_ai_ci, resource_id COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t
);
SET @resource_metrics_msg = IF(@resource_metrics_dup = 0, 'ok',
  CONCAT('resource_metrics has ', @resource_metrics_dup, ' duplicate primary keys under utf8mb4_0900_ai_ci; de-duplicate before running this migration'));
SET @resource_metrics_signal = IF(@resource_metrics_dup = 0, 'SELECT 1',
  CONCAT('SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''', @resource_metrics_msg, ''''));
PREPARE resource_metrics_check FROM @resource_metrics_signal;
EXECUTE resource_metrics_check;
DEALLOCATE PREPARE resource_metrics_check;

-- resource_metric_flushes: PRIMARY KEY (flush_id) — flush_id is VARCHAR(64) generated, UUID/ULID style; collision check for safety
SET @flushes_dup = (
  SELECT COUNT(*) FROM (
    SELECT flush_id
    FROM resource_metric_flushes
    GROUP BY flush_id COLLATE utf8mb4_0900_ai_ci
    HAVING COUNT(*) > 1
  ) t
);
SET @flushes_msg = IF(@flushes_dup = 0, 'ok',
  CONCAT('resource_metric_flushes has ', @flushes_dup, ' duplicate primary keys under utf8mb4_0900_ai_ci; de-duplicate before running this migration'));
SET @flushes_signal = IF(@flushes_dup = 0, 'SELECT 1',
  CONCAT('SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''', @flushes_msg, ''''));
PREPARE flushes_check FROM @flushes_signal;
EXECUTE flushes_check;
DEALLOCATE PREPARE flushes_check;

-- parse_tasks: PRIMARY KEY (id) — UUID, no text unique index; still check for safety
SET @parse_tasks_dup = (
  SELECT COUNT(*) FROM (
    SELECT id FROM parse_tasks GROUP BY id COLLATE utf8mb4_0900_ai_ci HAVING COUNT(*) > 1
  ) t
);
SET @parse_tasks_msg = IF(@parse_tasks_dup = 0, 'ok',
  CONCAT('parse_tasks has ', @parse_tasks_dup, ' duplicate primary keys under utf8mb4_0900_ai_ci; de-duplicate before running this migration'));
SET @parse_tasks_signal = IF(@parse_tasks_dup = 0, 'SELECT 1',
  CONCAT('SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''', @parse_tasks_msg, ''''));
PREPARE parse_tasks_check FROM @parse_tasks_signal;
EXECUTE parse_tasks_check;
DEALLOCATE PREPARE parse_tasks_check;

-- =======================
-- DDL：所有预检通过后才执行，8 张业务表统一转换
-- =======================
ALTER TABLE categories             CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skills                 CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE parse_tasks            CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skill_tags             CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE skill_versions         CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE resource_metrics       CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE resource_metric_flushes CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
ALTER TABLE mcp_servers            CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- +migrate Down notransaction
-- collation 统一是单向操作。回滚会恢复到混合 collation 状态，不推荐执行。
-- Down 方向故意 no-op（SELECT 1）：ledger 行删除，但表保持 utf8mb4_0900_ai_ci。
-- 这是安全的——所有新建表 CREATE TABLE 均已显式声明 COLLATE=utf8mb4_0900_ai_ci，
-- 不会再出现 JOIN collation conflict，无需将已转换的表恢复到旧 collation。
-- gorp_migrations 不在本 migration 的 ALTER 范围内（见 Up 注释），Down 同样不处理。
SELECT 1;
