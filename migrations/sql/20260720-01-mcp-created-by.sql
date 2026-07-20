-- +migrate Up
-- ============================================================================
-- mcp-created-by :: mcp_servers.created_by_type / created_by_bot_uid / _bot_name
-- ============================================================================
-- Adds the "created by whom" provenance triple used by octo-web to distinguish
-- MCPs created by a human user versus MCPs created by that user's Bot on their
-- behalf (issue #894). No behavioural change: a Bot-created MCP is owned by the
-- Bot's owner (owner_uid) with the owner's display name (creator_name) exactly
-- as before — the middleware already collapses the Bot into the owner
-- Identity. This triple is a metadata badge only:
--
--   * created_by_type — 'human' (default) or 'bot'. 'import' is reserved for a
--     future Git-import path (#867) but not written by anything today; the
--     ENUM lists it so a later migration doesn't need to widen the type.
--   * created_by_bot_uid  — the Bot's UID when the create request was made
--                           with a Bot token; NULL for human creates.
--   * created_by_bot_name — snapshot of Bot display name at create time so the
--                           badge remains meaningful after a Bot rename/delete.
--
-- Defaults + NULL semantics -------------------------------------------------
-- created_by_type has NOT NULL DEFAULT 'human' so every legacy row (pre-#894)
-- reads back as 'human' without a backfill pass. The bot_uid/bot_name columns
-- default to NULL, matching the "no Bot involved" case. New rows created after
-- this migration land with values stamped by the service layer.
--
-- Index ---------------------------------------------------------------------
-- idx_mcp_created_by_type supports the `created_by_type IN (…)` filter on the
-- list endpoints (mcp-v1.md §4.2). It is a light-weight secondary index — no
-- covering intent — because the value has extremely low cardinality; MySQL
-- will still fall back to table scans when the filter is not selective. That
-- is fine at v1 scale (see the existing scaling notes on other indexes in
-- 20260714-04-mcp-catalog.sql).
-- ============================================================================

-- +migrate StatementBegin
ALTER TABLE `mcp_servers`
  ADD COLUMN `created_by_type`     ENUM('human','bot','import') NOT NULL DEFAULT 'human'
    COMMENT "Provenance of this row: 'human' for user-created MCPs, 'bot' for MCPs created via a Bot token on behalf of its owner, 'import' reserved for #867"
    AFTER `creator_name`,
  ADD COLUMN `created_by_bot_uid`  VARCHAR(64)  NULL DEFAULT NULL
    COMMENT 'Bot UID when created_by_type=bot; NULL otherwise'
    AFTER `created_by_type`,
  ADD COLUMN `created_by_bot_name` VARCHAR(128) NULL DEFAULT NULL
    COMMENT 'Snapshot of Bot display name at create time; kept even if the Bot is later renamed or deleted'
    AFTER `created_by_bot_uid`,
  ADD INDEX  `idx_mcp_created_by_type` (`created_by_type`);
-- +migrate StatementEnd

-- +migrate Down
ALTER TABLE `mcp_servers`
  DROP INDEX `idx_mcp_created_by_type`,
  DROP COLUMN `created_by_bot_name`,
  DROP COLUMN `created_by_bot_uid`,
  DROP COLUMN `created_by_type`;
