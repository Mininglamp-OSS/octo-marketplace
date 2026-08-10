-- +migrate Up
-- ============================================================================
-- expert-catalog-v1 :: experts / expert_squads / expert_tags
-- ============================================================================
-- First business tables for the Expert Marketplace (专家市场). Backs the two
-- entity families and the shared tag dictionary described in
-- docs/api/expert-v1.md and .octospec/tasks/expert-catalog-v1/brief.md.
--
-- Design notes (see the API doc for the full contract):
--
--   * TWO entity tables, not one. `experts` (专家 / single agents) and
--     `expert_squads` (专家团 / teams) share only generic marketplace metadata;
--     their payloads do not overlap, and the octo-web UI shows them in separate
--     tabs, so a `kind`-discriminator single table (half-NULL rows) is avoided.
--   * Categories use the dedicated `expert_categories` taxonomy (added in
--     20260806-01, which supersedes the earlier "reuse the shared `categories`
--     table" plan) via a `category_id` string — no FK constraint, validated in
--     the service.
--   * Tags follow the Skill design: a per-Space `expert_tags` dictionary
--     (shared by both entities) + a `tags` JSON array of tag ids on each row
--     (mirrors `skill_tags` + `skills.tags` after 20260721-00). The wire always
--     exposes tag NAMES; the repository resolves ids <-> names.
--   * `instruction` / `mcp_config` are MEDIUMTEXT, not JSON: `mcp_config` is the
--     raw `mcpServers` config the user typed and must round-trip byte-for-byte
--     (the service validates it parses as JSON + size cap, but never reformats).
--   * `skills_json` / `strategies_json` / `dependencies_json` / `members_json`
--     are schemaless collections the list/search paths never filter on, so they
--     are JSON columns rather than child tables (matching mcp_servers' tools /
--     faqs / notes rationale). `members_json` snapshots each squad member as a
--     full ExpertSpec (instruction + mcp_config + skills) + role metadata;
--     members are NOT foreign keys into `experts`.
--   * Soft delete via `deleted_at` + a STORED generated `name_live` column, with
--     a UNIQUE index over (owner_uid, space_id, name_live) — the exact
--     non-deadlocking uniqueness recipe from mcp-catalog-v1 (20260714-05). A
--     duplicate live tuple fails with MySQL 1062, mapped to DUPLICATE; a
--     soft-deleted row carries name_live = NULL so the name frees up.
--   * Provenance triple (created_by_type / _bot_uid / _bot_name) matches
--     mcp_servers (20260720-01) so a Bot-created record shows the right badge.
--   * Timestamps are DATETIME(3) (millisecond) app-stamped, matching mcp_servers
--     and the RFC 3339 ms contract; `space_id` is NULL only for `system` rows.
--   * utf8mb4 / utf8mb4_unicode_ci throughout, matching the normalized
--     marketplace collation (20260722-00).
-- ============================================================================

CREATE TABLE `experts` (
  `id`                  VARCHAR(64)  NOT NULL,
  `short_name`          VARCHAR(16)  NOT NULL DEFAULT '',
  `name`                VARCHAR(128) NOT NULL,
  `summary`             VARCHAR(512) NOT NULL DEFAULT '',
  `category_id`         VARCHAR(36)  NOT NULL,
  `tags`                JSON         NOT NULL,
  `publisher`           VARCHAR(128) NOT NULL DEFAULT '',
  `owner_uid`           VARCHAR(64)  NOT NULL,
  `creator_name`        VARCHAR(128) NOT NULL DEFAULT '',
  `created_by_type`     ENUM('human','bot','import') NOT NULL DEFAULT 'human',
  `created_by_bot_uid`  VARCHAR(64)  NULL DEFAULT NULL,
  `created_by_bot_name` VARCHAR(128) NULL DEFAULT NULL,
  `space_id`            VARCHAR(64)  NULL,
  `visibility`          ENUM('public','private','system') NOT NULL DEFAULT 'public',
  `instruction`         MEDIUMTEXT   NOT NULL,
  `mcp_config`          MEDIUMTEXT   NOT NULL,
  `skills_json`         JSON         NOT NULL,
  `created_at`          DATETIME(3)  NOT NULL,
  `updated_at`          DATETIME(3)  NOT NULL,
  `deleted_at`          DATETIME(3)  NULL DEFAULT NULL,
  `name_live`           VARCHAR(128)
    GENERATED ALWAYS AS (IF(`deleted_at` IS NULL, `name`, NULL)) STORED,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_expert_owner_space_name_live` (`owner_uid`, `space_id`, `name_live`),
  KEY `idx_expert_scope_cat_created` (`visibility`, `space_id`, `category_id`, `created_at`),
  KEY `idx_expert_owner_created` (`owner_uid`, `created_at`),
  KEY `idx_expert_created_by_type` (`created_by_type`),
  KEY `idx_expert_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Expert catalog entries — single experts (expert-catalog-v1)';

CREATE TABLE `expert_squads` (
  `id`                  VARCHAR(64)  NOT NULL,
  `short_name`          VARCHAR(16)  NOT NULL DEFAULT '',
  `name`                VARCHAR(128) NOT NULL,
  `summary`             VARCHAR(512) NOT NULL DEFAULT '',
  `category_id`         VARCHAR(36)  NOT NULL,
  `tags`                JSON         NOT NULL,
  `publisher`           VARCHAR(128) NOT NULL DEFAULT '',
  `owner_uid`           VARCHAR(64)  NOT NULL,
  `creator_name`        VARCHAR(128) NOT NULL DEFAULT '',
  `created_by_type`     ENUM('human','bot','import') NOT NULL DEFAULT 'human',
  `created_by_bot_uid`  VARCHAR(64)  NULL DEFAULT NULL,
  `created_by_bot_name` VARCHAR(128) NULL DEFAULT NULL,
  `space_id`            VARCHAR(64)  NULL,
  `visibility`          ENUM('public','private','system') NOT NULL DEFAULT 'public',
  `leader`              VARCHAR(128) NOT NULL DEFAULT '',
  `strategies_json`     JSON         NOT NULL,
  `dependencies_json`   JSON         NOT NULL,
  `permission`          VARCHAR(512) NOT NULL DEFAULT '',
  `members_json`        JSON         NOT NULL,
  `created_at`          DATETIME(3)  NOT NULL,
  `updated_at`          DATETIME(3)  NOT NULL,
  `deleted_at`          DATETIME(3)  NULL DEFAULT NULL,
  `name_live`           VARCHAR(128)
    GENERATED ALWAYS AS (IF(`deleted_at` IS NULL, `name`, NULL)) STORED,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_squad_owner_space_name_live` (`owner_uid`, `space_id`, `name_live`),
  KEY `idx_squad_scope_cat_created` (`visibility`, `space_id`, `category_id`, `created_at`),
  KEY `idx_squad_owner_created` (`owner_uid`, `created_at`),
  KEY `idx_squad_created_by_type` (`created_by_type`),
  KEY `idx_squad_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Expert catalog entries — expert squads (expert-catalog-v1)';

CREATE TABLE `expert_tags` (
  `id`         BIGINT       NOT NULL AUTO_INCREMENT,
  `space_id`   VARCHAR(64)  NOT NULL,
  `name`       VARCHAR(128) NOT NULL,
  `created_by` VARCHAR(64)  NOT NULL,
  `created_at` DATETIME(3)  NOT NULL,
  `updated_at` DATETIME(3)  NOT NULL,
  PRIMARY KEY (`space_id`, `name`),
  UNIQUE KEY `uk_expert_tags_id` (`id`),
  KEY `idx_expert_tags_space_updated` (`space_id`, `updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-Space tag dictionary shared by experts + expert_squads (expert-catalog-v1)';

-- +migrate Down

DROP TABLE IF EXISTS `expert_tags`;
DROP TABLE IF EXISTS `expert_squads`;
DROP TABLE IF EXISTS `experts`;
