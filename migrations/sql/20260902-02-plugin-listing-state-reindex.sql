-- +migrate Up
-- Reindex, split out of 20260902-00 so each file is a single DDL unit (see
-- 20260902-00's header for the implicit-commit hazard a DDL→UPDATE→DDL file
-- carried). This file now carries exactly ONE DDL — the review_requests index
-- that used to sit here moved to 20260902-03, because two ALTERs in one file
-- reintroduce that same hazard (a crash between them replays a bare ADD INDEX,
-- ERROR 1061).
--
-- The catalog read predicate now filters (visibility, space_id, listing_state)
-- together, so listing_state joins the existing scope index rather than getting
-- one of its own. DROP+ADD in one ALTER lets InnoDB do a single index rebuild.
--
-- LOCK=NONE keeps the table writable during the rebuild; ALGORITHM=INPLACE
-- forbids a silent copy-table fallback. An explicit clause turns "this index
-- change would block writes / rewrite the whole table" into an error an operator
-- sees at deploy time rather than a surprise stall on the largest table.
ALTER TABLE `plugins`
  DROP INDEX `idx_plugins_scope_category_created`,
  ADD INDEX `idx_plugins_scope_category_created` (`visibility`, `space_id`, `listing_state`, `category_id`, `created_at`),
  ALGORITHM=INPLACE, LOCK=NONE;

-- +migrate Down
ALTER TABLE `plugins`
  DROP INDEX `idx_plugins_scope_category_created`,
  ADD INDEX `idx_plugins_scope_category_created` (`visibility`, `space_id`, `category_id`, `created_at`),
  ALGORITHM=INPLACE, LOCK=NONE;
