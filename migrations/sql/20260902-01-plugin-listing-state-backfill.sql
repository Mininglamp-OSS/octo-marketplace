-- +migrate Up
-- Backfill + default-flip, split out of 20260902-00 so each file is a single
-- DDL/DML unit (see 20260902-00's header for the implicit-commit hazard a
-- DDL→UPDATE→DDL file carried).
--
-- 20260902-00 added the column with DEFAULT 'published' via ALGORITHM=INSTANT,
-- which grandfathered every EXISTING row — live or soft-deleted — to 'published'
-- at zero row cost. Two corrections remain, both here:
--
--  1. Soft-deleted rows must NOT stay 'published'. They were invisible to the org
--     already; leaving them listed means an accidental undelete silently
--     republishes a plugin the org had lost sight of. Stamp them 'draft'. This
--     UPDATE touches only WHERE deleted_at IS NOT NULL — a tiny slice — so it is
--     safe to run outside the (non-existent) DDL transaction and is idempotent:
--     re-running it re-sets the same rows to the same value.
--
--  2. Flip the column DEFAULT to the fail-closed 'draft' for FUTURE inserts.
--     RebuildGraph re-inserts a container's embedded children inheriting the
--     parent's row shape; a 'published' default would silently list a draft
--     container's children. Every Go write path threads ListingState explicitly,
--     so the default is only reachable from hand-written SQL and test fixtures,
--     where invisible is the safe outcome. SET DEFAULT is a metadata-only change
--     that does NOT rewrite the instant-add default 20260902-00 stamped onto
--     pre-existing rows, so the grandfather value survives untouched.
UPDATE `plugins` SET `listing_state` = 'draft' WHERE `deleted_at` IS NOT NULL;

ALTER TABLE `plugins` ALTER COLUMN `listing_state` SET DEFAULT 'draft';

-- +migrate Down
-- Restore the DEFAULT to the grandfather value 20260902-00 set. The soft-deleted
-- backfill is deliberately NOT reversed: re-listing rows the org had lost sight
-- of is exactly the hazard the Up guarded against, and Down is for rolling back
-- the schema shape, not for resurrecting a fail-open state.
ALTER TABLE `plugins` ALTER COLUMN `listing_state` SET DEFAULT 'published';
