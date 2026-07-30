-- +migrate Up
-- 统一所有表到 utf8mb4_0900_ai_ci（MySQL 8 默认），修复跨表 JOIN 时 collation 不兼容导致的 500 错误。
-- 注意：本迁移方向为 utf8mb4_unicode_ci/general_ci (PAD SPACE) → utf8mb4_0900_ai_ci (NO PAD)；
-- NO PAD 更严格，PAD SPACE 下等价的尾空格变体在 NO PAD 下依然区分，不会引入唯一键冲突，故无需 preflight guard。
-- 参考 20260722-00 反方向（NO PAD→PAD SPACE）才需要 preflight。

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
