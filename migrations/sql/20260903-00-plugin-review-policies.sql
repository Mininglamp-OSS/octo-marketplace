-- +migrate Up
CREATE TABLE IF NOT EXISTS `plugin_review_policies` (
  `space_id` VARCHAR(64) NOT NULL,
  `is_auto_approve_enabled` TINYINT(1) NOT NULL DEFAULT 1,
  `updated_by` VARCHAR(64) NOT NULL,
  `updated_by_name` VARCHAR(128) NOT NULL DEFAULT '',
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`space_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-Space plugin review policy; an absent row means auto approval enabled';

-- +migrate Down
DROP TABLE IF EXISTS `plugin_review_policies`;
