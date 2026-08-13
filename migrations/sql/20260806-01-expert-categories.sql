-- +migrate Up
-- ============================================================================
-- expert-catalog-v1 :: expert_categories (dedicated category taxonomy)
-- ============================================================================
-- A dedicated category dictionary for the Expert Marketplace, replacing the
-- earlier "reuse the shared categories table" plan (see docs/api/expert-v1.md
-- §5). experts.category_id / expert_squads.category_id (added in 20260806-00)
-- reference this table by id; the service resolves the wire `category` NAME to
-- an id on write and back to a NAME on read, and GET /expert_categories serves
-- the chips the octo-web 专家市场 tab renders.
--
-- Shape mirrors the shared `categories` table (20260714-01 + 20260720-01) and
-- the 20260806-00 conventions:
--   * id VARCHAR(36) PK; name VARCHAR(64); icon_key lucide glyph key;
--     sort_order for deterministic chip ordering.
--   * DATETIME(3) (millisecond) app-stamped timestamps, matching experts /
--     expert_squads, not the TIMESTAMP defaults the older skill tables use.
--   * Soft delete via deleted_at + a STORED generated name_live column with a
--     UNIQUE index over name_live, so a deleted row projects NULL and frees the
--     name — the same recipe as categories.uk_categories_name_live.
--   * utf8mb4 / utf8mb4_unicode_ci throughout (20260722-00 normalized collation).
-- ============================================================================

CREATE TABLE `expert_categories` (
  `id`         VARCHAR(36) NOT NULL,
  `name`       VARCHAR(64) NOT NULL,
  `icon_key`   VARCHAR(64) NOT NULL DEFAULT '',
  `sort_order` INT         NOT NULL DEFAULT 0,
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  `deleted_at` DATETIME(3) NULL DEFAULT NULL,
  `name_live`  VARCHAR(64)
    GENERATED ALWAYS AS (IF(`deleted_at` IS NULL, `name`, NULL)) STORED,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_expert_categories_name_live` (`name_live`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Expert Marketplace category taxonomy (expert-catalog-v1)';

INSERT INTO `expert_categories` (`id`, `name`, `icon_key`, `sort_order`, `created_at`, `updated_at`) VALUES
  ('marketing-planning', '营销策划', 'Megaphone',         1, NOW(3), NOW(3)),
  ('content-creation',   '内容创作', 'PenLine',           2, NOW(3), NOW(3)),
  ('ad-delivery',        '广告投放', 'Target',            3, NOW(3), NOW(3)),
  ('data-insight',       '数据洞察', 'ChartColumn',       4, NOW(3), NOW(3)),
  ('office-efficiency',  '办公提效', 'BriefcaseBusiness', 5, NOW(3), NOW(3)),
  ('dev-tools',          '研发工具', 'Code2',             6, NOW(3), NOW(3));

-- +migrate Down

DROP TABLE IF EXISTS `expert_categories`;
