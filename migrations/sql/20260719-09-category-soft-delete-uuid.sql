-- +migrate Up
-- 1. 分类软删除：加 deleted_at 字段
ALTER TABLE categories ADD COLUMN deleted_at TIMESTAMP NULL DEFAULT NULL AFTER updated_at;
ALTER TABLE categories ADD INDEX idx_categories_deleted (deleted_at);

-- 2. 将seed数据的slug id替换为UUID（保持名称不变）
--    先加临时列存映射
CREATE TEMPORARY TABLE category_id_map (
  old_id VARCHAR(36) PRIMARY KEY,
  new_id VARCHAR(36) NOT NULL
);

INSERT INTO category_id_map (old_id, new_id) VALUES
  ('starter',   UUID()),
  ('dev-tools', UUID()),
  ('infra',     UUID()),
  ('office',    UUID()),
  ('marketing', UUID()),
  ('frontend',  UUID()),
  ('media',     UUID()),
  ('quality',   UUID()),
  ('research',  UUID()),
  ('analytics', UUID()),
  ('content',   UUID()),
  ('mobile',    UUID()),
  ('cloud',     UUID()),
  ('social',    UUID()),
  ('other',     UUID()),
  ('all',       UUID());

-- 更新skills表的category_id
UPDATE skills s
JOIN category_id_map m ON s.category_id = m.old_id
SET s.category_id = m.new_id;

-- 更新categories表的id
UPDATE categories c
JOIN category_id_map m ON c.id = m.old_id
SET c.id = m.new_id;

DROP TEMPORARY TABLE category_id_map;

-- +migrate Down
-- 注意：Down回滚无法还原原始UUID，因为是随机生成的。
-- 如需回滚需要手动处理或重新seed。
ALTER TABLE categories DROP INDEX idx_categories_deleted;
ALTER TABLE categories DROP COLUMN deleted_at;
