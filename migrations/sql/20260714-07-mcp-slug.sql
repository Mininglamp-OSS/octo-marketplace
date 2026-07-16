-- +migrate Up
-- ============================================================================
-- mcp-catalog-v1 :: slug (per-Space identifier)
-- ============================================================================
-- Adds the ASCII slug column that clients paste into their mcpServers JSON
-- config as the object key (mcp-v1.md §3, "服务标识"). Distinct from `name`,
-- which is the display label and may contain CJK / emoji. Slug must satisfy
-- ^[a-z0-9-]{1,64}$; the service layer validates on write.
--
-- Uniqueness: per Space among LIVE rows. Same tuple as the name constraint
-- (migration 02) — a generated column mirrors the slug for live rows and NULL
-- for soft-deleted, then a UNIQUE index over (space_id, slug_live) enforces
-- the rule at the DB level without needing app-side locks.
--
-- SYSTEM ROWS CAVEAT ---------------------------------------------------------
-- `system` rows carry space_id=NULL. MySQL treats two rows with the same
-- (NULL, "github") as distinct under a UNIQUE index (NULL != NULL), so this
-- constraint does NOT globally deduplicate system MCP slugs — two admins
-- publishing "github" as system will both succeed. Per the v1 decision
-- ("按 space 唯一"), that's acceptable; a follow-up brief will decide whether
-- to add a separate UNIQUE for system-scoped slugs.
--
-- NAME/SLUG SEPARATE INDEXES -------------------------------------------------
-- (owner_uid, space_id, name_live)  — migration 02, still enforced
-- (space_id, slug_live)             — this migration
-- The two collide on independent axes, so the app maps 1062 by which
-- constraint fired: `uq_owner_space_name_live` → name_taken;
-- `uq_space_slug_live` → slug_taken.
-- ============================================================================

-- +migrate StatementBegin
ALTER TABLE `mcp_servers`
  ADD COLUMN `slug` VARCHAR(64) NOT NULL DEFAULT '' AFTER `name`;
-- +migrate StatementEnd

-- +migrate StatementBegin
ALTER TABLE `mcp_servers`
  ADD COLUMN `slug_live` VARCHAR(64)
    AS (IF(`deleted_at` IS NULL AND `slug` <> '', `slug`, NULL))
    STORED
    AFTER `slug`;
-- +migrate StatementEnd

-- +migrate StatementBegin
ALTER TABLE `mcp_servers`
  ADD UNIQUE KEY `uq_space_slug_live` (`space_id`, `slug_live`);
-- +migrate StatementEnd

-- +migrate Down
-- +migrate StatementBegin
ALTER TABLE `mcp_servers` DROP INDEX `uq_space_slug_live`;
-- +migrate StatementEnd

-- +migrate StatementBegin
ALTER TABLE `mcp_servers` DROP COLUMN `slug_live`;
-- +migrate StatementEnd

-- +migrate StatementBegin
ALTER TABLE `mcp_servers` DROP COLUMN `slug`;
-- +migrate StatementEnd
