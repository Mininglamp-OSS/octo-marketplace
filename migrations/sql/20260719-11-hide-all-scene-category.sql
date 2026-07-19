-- +migrate Up

UPDATE skills s
JOIN categories old_category ON old_category.id = s.category_id
JOIN categories other_category ON other_category.name = '其他'
SET s.category_id = other_category.id
WHERE old_category.name IN ('全部', '所有场景分类')
  AND old_category.id <> other_category.id;

UPDATE categories
SET deleted_at = NOW()
WHERE name IN ('全部', '所有场景分类')
  AND deleted_at IS NULL;

-- +migrate Down

UPDATE categories
SET deleted_at = NULL
WHERE name = '所有场景分类';
