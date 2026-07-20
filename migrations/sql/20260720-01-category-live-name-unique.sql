-- +migrate Up
-- Replace the bare category-name unique index with a live-row-only unique
-- constraint. MySQL UNIQUE indexes treat NULL values as distinct, so deleted
-- rows project NULL and no longer block recreating the same category name.
ALTER TABLE categories DROP INDEX uk_categories_name;
ALTER TABLE categories
  ADD COLUMN name_live VARCHAR(64)
    GENERATED ALWAYS AS (IF(deleted_at IS NULL, name, NULL)) STORED,
  ADD UNIQUE KEY uk_categories_name_live (name_live);

-- +migrate Down
ALTER TABLE categories DROP INDEX uk_categories_name_live;
ALTER TABLE categories DROP COLUMN name_live;
ALTER TABLE categories ADD UNIQUE INDEX uk_categories_name (name);
