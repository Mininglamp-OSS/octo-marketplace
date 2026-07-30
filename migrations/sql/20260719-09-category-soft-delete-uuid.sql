-- +migrate Up
-- 1. 分类软删除：加 deleted_at 字段
ALTER TABLE categories ADD COLUMN deleted_at TIMESTAMP NULL DEFAULT NULL AFTER updated_at;
ALTER TABLE categories ADD INDEX idx_categories_deleted (deleted_at);

-- 2. 将seed数据的slug id替换为UUID（保持名称不变）
--    先加临时列存映射
CREATE TEMPORARY TABLE category_id_map (
  old_id VARCHAR(36) PRIMARY KEY,
  new_id VARCHAR(36) NOT NULL
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
-- 临时表固定为 utf8mb4_unicode_ci，JOIN 谓词持久列侧显式 COLLATE：
-- legacy 部署跑到此处时 categories.id/skills.category_id 为 utf8mb4_unicode_ci（PAD SPACE），
-- fresh install 下则为 utf8mb4_0900_ai_ci（NO PAD），两侧 collation coercibility 相同，
-- MySQL 不会自动 coerce，必须在持久列侧显式 COLLATE 以避免 ERROR 1267；
-- COLLATE 加在持久列侧会使索引无法用于 eq_ref；legacy 升级时列与临时表均为 unicode_ci，索引仍可使用，
-- fresh install 时两表均为空/极小，hash join 即可，无 full-scan 风险；
-- 20260730-00 跑完后所有表统一为 utf8mb4_0900_ai_ci。

INSERT INTO category_id_map (old_id, new_id) VALUES
  ('starter',               UUID()),
  ('dev-tools',             UUID()),
  ('infra',                 UUID()),
  ('office',                UUID()),
  ('marketing',             UUID()),
  ('frontend',              UUID()),
  ('media',                 UUID()),
  ('quality',               UUID()),
  ('research',              UUID()),
  ('analytics',             UUID()),
  ('content',               UUID()),
  ('mobile',                UUID()),
  ('cloud',                 UUID()),
  ('social',                UUID()),
  ('all',                   UUID()),
  ('office-efficiency',     UUID()),
  ('content-creation',      UUID()),
  ('dev-programming',       UUID()),
  ('data-analysis',         UUID()),
  ('design-media',          UUID()),
  ('ai-agent',              UUID()),
  ('knowledge-management',  UUID()),
  ('business-operations',   UUID()),
  ('education-learning',    UUID()),
  ('industry-professional', UUID()),
  ('it-ops-security',       UUID()),
  ('life-services',         UUID()),
  ('other',                 UUID());

-- 更新skills表的category_id
UPDATE skills s
JOIN category_id_map m ON s.category_id COLLATE utf8mb4_unicode_ci = m.old_id
SET s.category_id = m.new_id;

-- 更新categories表的id
UPDATE categories c
JOIN category_id_map m ON c.id COLLATE utf8mb4_unicode_ci = m.old_id
SET c.id = m.new_id;

DROP TEMPORARY TABLE category_id_map;

-- +migrate Down
-- 注意：Down回滚无法还原原始UUID，因为是随机生成的。
-- 如需回滚需要手动处理或重新seed。
ALTER TABLE categories DROP INDEX idx_categories_deleted;
ALTER TABLE categories DROP COLUMN deleted_at;
