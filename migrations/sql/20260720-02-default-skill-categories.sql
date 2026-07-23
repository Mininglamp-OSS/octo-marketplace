-- +migrate Up

CREATE TEMPORARY TABLE default_skill_category_taxonomy (
  name VARCHAR(64) NOT NULL PRIMARY KEY,
  icon_key VARCHAR(64) NOT NULL,
  sort_order INT NOT NULL
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

INSERT INTO default_skill_category_taxonomy (name, icon_key, sort_order) VALUES
  ('办公效率', 'BriefcaseBusiness', 1),
  ('AI Agent', 'Bot', 2),
  ('研究分析', 'ChartColumn', 3),
  ('内容创作', 'PenLine', 4),
  ('知识管理', 'BookOpen', 5),
  ('行业专业', 'Building2', 6),
  ('运营', 'Megaphone', 7),
  ('开发编程', 'Code2', 8),
  ('IT运维', 'ShieldCheck', 9),
  ('其他', 'MoreHorizontal', 10);

CREATE TEMPORARY TABLE default_skill_category_remap (
  from_name VARCHAR(64) NOT NULL PRIMARY KEY,
  to_name VARCHAR(64) NOT NULL
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

INSERT INTO default_skill_category_remap (from_name, to_name) VALUES
  ('全部', '其他'),
  ('所有场景分类', '其他'),
  ('办公协作', '办公效率'),
  ('办公效率', '办公效率'),
  ('AI Agent', 'AI Agent'),
  ('数据分析', '研究分析'),
  ('洞察研究', '研究分析'),
  ('内容营销', '内容创作'),
  ('内容创作', '内容创作'),
  ('媒体处理', '内容创作'),
  ('设计多媒体', '内容创作'),
  ('知识管理', '知识管理'),
  ('教育学习', '知识管理'),
  ('行业专业', '行业专业'),
  ('市场推广', '运营'),
  ('商业运营', '运营'),
  ('运营', '运营'),
  ('开发工具', '开发编程'),
  ('前端开发', '开发编程'),
  ('移动开发', '开发编程'),
  ('开发编程', '开发编程'),
  ('基础设施', 'IT运维'),
  ('云效工具', 'IT运维'),
  ('代码质检', 'IT运维'),
  ('IT 运维与安全', 'IT运维'),
  ('IT运维', 'IT运维'),
  ('社交娱乐', '其他'),
  ('生活服务', '其他'),
  ('装机必备', '其他'),
  ('其他', '其他');

-- Refresh categories that already exist under the target names.
UPDATE categories c
JOIN default_skill_category_taxonomy t ON t.name = c.name
SET c.icon_key = t.icon_key,
    c.sort_order = t.sort_order
WHERE c.deleted_at IS NULL;

-- Add any target category that does not have a live row.
INSERT INTO categories (id, name, icon_key, sort_order, created_at, updated_at)
SELECT UUID(), t.name, t.icon_key, t.sort_order, NOW(), NOW()
FROM default_skill_category_taxonomy t
LEFT JOIN categories c ON c.name = t.name AND c.deleted_at IS NULL
WHERE c.id IS NULL;

-- Move Skills off retired default categories before those categories are hidden.
UPDATE skills s
JOIN categories old_category ON old_category.id = s.category_id
JOIN default_skill_category_remap r ON r.from_name = old_category.name
JOIN categories new_category ON new_category.name = r.to_name AND new_category.deleted_at IS NULL
SET s.category_id = new_category.id
WHERE old_category.id <> new_category.id;

-- Hide retired default categories only. Custom categories outside the known
-- default taxonomy remain visible.
UPDATE categories c
JOIN default_skill_category_remap r ON r.from_name = c.name
LEFT JOIN default_skill_category_taxonomy t ON t.name = c.name
SET c.deleted_at = NOW()
WHERE t.name IS NULL
  AND c.deleted_at IS NULL;

-- Final pass keeps the target taxonomy order deterministic.
UPDATE categories c
JOIN default_skill_category_taxonomy t ON t.name = c.name
SET c.icon_key = t.icon_key,
    c.sort_order = t.sort_order
WHERE c.deleted_at IS NULL;

DROP TEMPORARY TABLE default_skill_category_remap;
DROP TEMPORARY TABLE default_skill_category_taxonomy;

-- +migrate Down

UPDATE categories
SET deleted_at = NOW()
WHERE name IN ('研究分析', '运营', 'IT运维')
  AND deleted_at IS NULL;

UPDATE categories
SET deleted_at = NULL
WHERE name IN (
  '办公效率', '内容创作', '开发编程', '数据分析',
  '设计多媒体', 'AI Agent', '知识管理', '商业运营',
  '教育学习', '行业专业', 'IT 运维与安全', '生活服务', '其他'
);
