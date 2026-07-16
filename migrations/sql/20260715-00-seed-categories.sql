-- +migrate Up

INSERT INTO categories (id, name, icon_key, sort_order) VALUES
  ('starter',    '装机必备',   'Box',            1),
  ('dev-tools',  '开发工具',   'Terminal',        2),
  ('infra',      '基础设施',   'Server',          3),
  ('office',     '办公协作',   'FolderKanban',    4),
  ('marketing',  '市场推广',   'Megaphone',       5),
  ('frontend',   '前端开发',   'Monitor',         6),
  ('media',      '媒体处理',   'Film',            7),
  ('quality',    '代码质检',   'ShieldCheck',     8),
  ('research',   '洞察研究',   'Eye',             9),
  ('analytics',  '数据分析',   'ChartColumn',    10),
  ('content',    '内容营销',   'PenLine',        11),
  ('mobile',     '移动开发',   'Smartphone',     12),
  ('cloud',      '云效工具',   'Cloud',          13),
  ('social',     '社交娱乐',   'Gamepad2',       14),
  ('other',      '其他',       'MoreHorizontal', 15),
  ('all',        '全部',       'LayoutGrid',     16);

-- +migrate Down

DELETE FROM categories WHERE id IN (
  'starter', 'dev-tools', 'infra', 'office', 'marketing', 'frontend',
  'media', 'quality', 'research', 'analytics', 'content', 'mobile',
  'cloud', 'social', 'other', 'all'
);
