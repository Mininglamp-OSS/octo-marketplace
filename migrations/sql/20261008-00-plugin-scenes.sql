-- +migrate Up

CREATE TABLE `plugin_scenes` (
  `scene_id`     VARCHAR(64)   NOT NULL,
  `scene_code`   VARCHAR(128)  NOT NULL,
  `name`         VARCHAR(128)  NOT NULL,
  `description`  VARCHAR(1024) NOT NULL DEFAULT '',
  `sort_order`   INT           NOT NULL DEFAULT 0,
  `created_at`   DATETIME(3)   NOT NULL,
  `updated_at`   DATETIME(3)   NOT NULL,
  PRIMARY KEY (`scene_id`),
  UNIQUE KEY `uq_plugin_scene_code` (`scene_code`),
  KEY `idx_plugin_scene_order` (`sort_order`, `scene_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Marketplace scene code registry';

INSERT INTO `plugin_scenes`
  (`scene_id`, `scene_code`, `name`, `description`, `sort_order`, `created_at`, `updated_at`)
SELECT
  SHA2(CONCAT('plugin-scene:', codes.scene_code), 256),
  codes.scene_code,
  CASE WHEN codes.scene_code = 'default' THEN 'Default' ELSE codes.scene_code END,
  '',
  CASE WHEN codes.scene_code = 'default' THEN 0 ELSE 100 END,
  CURRENT_TIMESTAMP(3),
  CURRENT_TIMESTAMP(3)
FROM (
  SELECT 'default' AS scene_code
  UNION
  SELECT DISTINCT placement_code FROM plugin_placements WHERE placement_code <> ''
  UNION
  SELECT DISTINCT placement_code FROM plugin_category_placements WHERE placement_code <> ''
) AS codes;

-- +migrate Down

DROP TABLE IF EXISTS `plugin_scenes`;
