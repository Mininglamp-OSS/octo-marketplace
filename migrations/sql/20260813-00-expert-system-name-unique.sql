-- +migrate Up
-- ============================================================================
-- expert-catalog-v1 :: system name uniqueness hardening
-- ============================================================================
-- Closes the TOCTOU on platform-provided (visibility=system) names. System
-- rows store space_id NULL, and NULL tuples never collide in a MySQL unique
-- index, so uq_expert_owner_space_name_live / uq_squad_owner_space_name_live
-- (20260806-00) cannot fire for them. The SuperAdmin create/rename paths only
-- had a non-atomic SELECT pre-check, so two concurrent admin writes could both
-- pass it and persist duplicate system names — which then surface in every
-- Space's catalog (PR #51 review).
--
-- Same STORED-generated-column recipe as 20260714-05 / 20260806-00:
-- `system_name_live` equals `name` only for LIVE system rows and is NULL for
-- everything else, so a UNIQUE index over the single column
--   * blocks a second live system row with the same name — the write fails
--     with duplicate-key (Error 1062), mapped to ErrNameTaken / DUPLICATE by
--     the repository;
--   * ignores public/private rows and soft-deleted rows entirely (NULL);
--   * frees a system name for reuse after soft delete.
-- The service keeps its SELECT pre-check as a friendly fast path; this index
-- is the authoritative guard under concurrency.
-- ============================================================================

-- +migrate StatementBegin
ALTER TABLE `experts`
  ADD COLUMN `system_name_live` VARCHAR(128)
    GENERATED ALWAYS AS (IF(`visibility` = 'system' AND `deleted_at` IS NULL, `name`, NULL)) STORED
    AFTER `name_live`,
  ADD UNIQUE KEY `uq_expert_system_name_live` (`system_name_live`);
-- +migrate StatementEnd

-- +migrate StatementBegin
ALTER TABLE `expert_squads`
  ADD COLUMN `system_name_live` VARCHAR(128)
    GENERATED ALWAYS AS (IF(`visibility` = 'system' AND `deleted_at` IS NULL, `name`, NULL)) STORED
    AFTER `name_live`,
  ADD UNIQUE KEY `uq_squad_system_name_live` (`system_name_live`);
-- +migrate StatementEnd

-- +migrate Down

-- +migrate StatementBegin
ALTER TABLE `expert_squads`
  DROP KEY `uq_squad_system_name_live`,
  DROP COLUMN `system_name_live`;
-- +migrate StatementEnd

-- +migrate StatementBegin
ALTER TABLE `experts`
  DROP KEY `uq_expert_system_name_live`,
  DROP COLUMN `system_name_live`;
-- +migrate StatementEnd
