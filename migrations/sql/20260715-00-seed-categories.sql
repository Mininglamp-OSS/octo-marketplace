-- +migrate Up

INSERT INTO categories (id, name, icon_key, sort_order) VALUES
  ('office-efficiency',     '办公效率',       'BriefcaseBusiness', 1),
  ('content-creation',      '内容创作',       'PenLine',           2),
  ('dev-programming',       '开发编程',       'Code2',             3),
  ('data-analysis',         '数据分析',       'ChartColumn',       4),
  ('design-media',          '设计多媒体',     'Palette',           5),
  ('ai-agent',              'AI Agent',       'Bot',               6),
  ('knowledge-management',  '知识管理',       'BookOpen',          7),
  ('business-operations',   '商业运营',       'Megaphone',         8),
  ('education-learning',    '教育学习',       'GraduationCap',     9),
  ('industry-professional', '行业专业',       'Building2',        10),
  ('it-ops-security',       'IT 运维与安全',  'ShieldCheck',      11),
  ('life-services',         '生活服务',       'HeartHandshake',   12),
  ('other',                 '其他',           'MoreHorizontal',   13);

-- +migrate Down

DELETE FROM categories WHERE id IN (
  'office-efficiency', 'content-creation', 'dev-programming',
  'data-analysis', 'design-media', 'ai-agent', 'knowledge-management',
  'business-operations', 'education-learning', 'industry-professional',
  'it-ops-security', 'life-services', 'other'
);
