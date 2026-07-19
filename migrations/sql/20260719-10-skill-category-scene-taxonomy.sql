-- +migrate Up

CREATE TEMPORARY TABLE category_taxonomy (
  legacy_name VARCHAR(64) NULL,
  name VARCHAR(64) NOT NULL PRIMARY KEY,
  icon_key VARCHAR(64) NOT NULL,
  sort_order INT NOT NULL
);

INSERT INTO category_taxonomy (legacy_name, name, icon_key, sort_order) VALUES
  ('办公协作', '办公效率',      'BriefcaseBusiness', 1),
  ('内容营销', '内容创作',      'PenLine',           2),
  ('开发工具', '开发编程',      'Code2',             3),
  ('数据分析', '数据分析',      'ChartColumn',       4),
  ('媒体处理', '设计多媒体',    'Palette',           5),
  (NULL,       'AI Agent',      'Bot',               6),
  ('洞察研究', '知识管理',      'BookOpen',          7),
  ('市场推广', '商业运营',      'Megaphone',         8),
  (NULL,       '教育学习',      'GraduationCap',     9),
  (NULL,       '行业专业',      'Building2',        10),
  ('代码质检', 'IT 运维与安全', 'ShieldCheck',      11),
  ('社交娱乐', '生活服务',      'HeartHandshake',   12),
  ('其他',     '其他',          'MoreHorizontal',   13);

CREATE TEMPORARY TABLE category_remap (
  from_name VARCHAR(64) NOT NULL PRIMARY KEY,
  to_name VARCHAR(64) NOT NULL
);

INSERT INTO category_remap (from_name, to_name) VALUES
  ('全部',     '其他'),
  ('所有场景分类', '其他'),
  ('办公协作', '办公效率'),
  ('内容营销', '内容创作'),
  ('开发工具', '开发编程'),
  ('前端开发', '开发编程'),
  ('移动开发', '开发编程'),
  ('数据分析', '数据分析'),
  ('媒体处理', '设计多媒体'),
  ('洞察研究', '知识管理'),
  ('市场推广', '商业运营'),
  ('基础设施', 'IT 运维与安全'),
  ('云效工具', 'IT 运维与安全'),
  ('代码质检', 'IT 运维与安全'),
  ('社交娱乐', '生活服务'),
  ('装机必备', '其他'),
  ('其他',     '其他');

-- Refresh categories that already exist under the target name.
UPDATE categories c
JOIN category_taxonomy t ON t.name = c.name
SET c.icon_key = t.icon_key,
    c.sort_order = t.sort_order,
    c.deleted_at = NULL;

-- Rename legacy seed categories to the new taxonomy when the target name is free.
UPDATE categories c
JOIN category_taxonomy t ON t.legacy_name = c.name
LEFT JOIN categories existing ON existing.name = t.name AND existing.id <> c.id
SET c.name = t.name,
    c.icon_key = t.icon_key,
    c.sort_order = t.sort_order,
    c.deleted_at = NULL
WHERE existing.id IS NULL;

-- Add new taxonomy categories that had no legacy row.
INSERT INTO categories (id, name, icon_key, sort_order, created_at, updated_at)
SELECT UUID(), t.name, t.icon_key, t.sort_order, NOW(), NOW()
FROM category_taxonomy t
LEFT JOIN categories c ON c.name = t.name
WHERE c.id IS NULL;

-- Move Skills off retired legacy categories before those categories are hidden.
UPDATE skills s
JOIN categories old_category ON old_category.id = s.category_id
JOIN category_remap r ON r.from_name = old_category.name
JOIN categories new_category ON new_category.name = r.to_name
SET s.category_id = new_category.id
WHERE old_category.id <> new_category.id;

-- Hide retired seed categories. Custom categories are intentionally preserved.
UPDATE categories c
JOIN category_remap r ON r.from_name = c.name
LEFT JOIN category_taxonomy t ON t.name = c.name
SET c.deleted_at = NOW()
WHERE t.name IS NULL
  AND c.deleted_at IS NULL;

-- Final pass keeps the visible taxonomy order deterministic.
UPDATE categories c
JOIN category_taxonomy t ON t.name = c.name
SET c.icon_key = t.icon_key,
    c.sort_order = t.sort_order,
    c.deleted_at = NULL;

DROP TEMPORARY TABLE category_remap;
DROP TEMPORARY TABLE category_taxonomy;

-- +migrate Down

UPDATE categories
SET deleted_at = NOW()
WHERE name IN (
  '所有场景分类', '办公效率', '内容创作', '开发编程',
  '设计多媒体', 'AI Agent', '知识管理', '商业运营',
  '教育学习', '行业专业', 'IT 运维与安全', '生活服务'
)
AND deleted_at IS NULL;
