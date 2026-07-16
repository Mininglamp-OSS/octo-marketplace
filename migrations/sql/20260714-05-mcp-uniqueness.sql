-- +migrate Up
-- ============================================================================
-- mcp-catalog-v1 :: uniqueness hardening
-- ============================================================================
-- Replaces the service-layer `SELECT ... FOR UPDATE` uniqueness recipe with a
-- real DB-level UNIQUE constraint. The FOR UPDATE approach was proven to
-- DEADLOCK under concurrency (InnoDB Error 1213): a gap-lock `SELECT ... FOR
-- UPDATE` on a *non-existent* (owner_uid, space_id, name) row takes a shared
-- gap lock, so N concurrent creators all acquire it and then race to upgrade to
-- an insert-intention lock — a circular wait — instead of one winning and the
-- rest getting name_taken. See internal/repository/mcp_test.go
-- (TestConcurrentCreateSameName), which reproduced the deadlock against MySQL 8
-- and now guards this fix.
--
-- MySQL 8 has no partial (`WHERE deleted_at IS NULL`) unique index, so we use a
-- STORED generated column `name_live` that equals `name` for live rows and is
-- NULL for soft-deleted rows. A UNIQUE index over
-- (owner_uid, space_id, name_live) then:
--   * blocks a second LIVE row with the same (owner_uid, space_id, name) —
--     the INSERT fails with duplicate-key (Error 1062), which the repository
--     maps to err.marketplace.mcp.name_taken. No pre-SELECT, no gap lock, no
--     deadlock.
--   * allows the name to be reused after soft delete, because a deleted row's
--     name_live is NULL and MySQL unique indexes permit many NULLs.
--   * leaves `system` rows (space_id NULL) unconstrained against each other,
--     matching the prior <=> NULL-safe behavior; system rows are not created
--     through the public API.
-- ============================================================================

-- +migrate StatementBegin
ALTER TABLE `mcp_servers`
  ADD COLUMN `name_live` VARCHAR(128)
    GENERATED ALWAYS AS (IF(`deleted_at` IS NULL, `name`, NULL)) STORED
    AFTER `name`,
  ADD UNIQUE KEY `uq_owner_space_name_live`
    (`owner_uid`, `space_id`, `name_live`);
-- +migrate StatementEnd

-- +migrate Down
-- +migrate StatementBegin
ALTER TABLE `mcp_servers`
  DROP KEY `uq_owner_space_name_live`,
  DROP COLUMN `name_live`;
-- +migrate StatementEnd
