-- +migrate Up
-- Split out of 20260902-02 so each file carries a SINGLE DDL statement (see
-- 20260902-00's header for the implicit-commit hazard a multi-DDL file carries).
-- 20260902-02 rebuilt an index on `plugins`; this one adds an index on
-- `plugin_review_requests`. Two DDLs in one file are avoidable and reintroduce
-- the exact hazard the split exists to close: MySQL implicitly commits each
-- ALTER, so if the process dies between the two ALTERs and the migration-record
-- insert, the next boot replays the file — the first ALTER is replay-safe, but a
-- bare ADD INDEX is not (ERROR 1061 duplicate key name), stranding the boot.
--
-- TWO DISTINCT COLLISIONS, and they need different fixes. Do not conflate them.
--
--   (1) CROSS-HEAD. Earlier heads of this branch (pre-split 20260902-00 and the
--       round-8 20260902-02) ALREADY PHYSICALLY CREATED an index named
--       `idx_review_plugin_submitted` on environments that ran those heads. A
--       fresh migration id does not help: replay against such an environment
--       throws ERROR 1061 (duplicate key name) and bricks every subsequent boot.
--       Fixed by giving this index a FRESH NAME, `idx_review_plugin_submitted_at`.
--       Code and the optimizer reference the index by shape, not by name; the
--       leftover old-named index on upgraded envs is a duplicate of the same key
--       (same columns, same order) and costs one extra index write per insert, so
--       this file also DROPS it — guarded the same way, since it exists only on
--       environments that ran an intermediate branch head and never on a clean
--       install. Doing it here rather than promising a follow-up migration is what
--       makes the Down below converge: dropping only the new name would leave a
--       branch-introduced index behind after a full rollback.
--
--   (2) SELF-REPLAY. The fresh name does NOTHING for the hazard the header above
--       describes: MySQL implicitly commits the ALTER, so a crash between it and
--       the gorp_migrations insert replays THIS file against a schema that already
--       has THIS index — ERROR 1061 again, and again a boot that needs manual
--       gorp_migrations surgery. An earlier revision of this file claimed the
--       fresh name made the step "idempotent against both a clean schema and any
--       environment that ran an earlier head"; that was true only of (1).
--
--       20260902-02's DROP INDEX + ADD INDEX of the same name is NOT the fix here,
--       though it is genuinely self-idempotent there: that file rebuilds an index
--       that already exists, so its DROP always has a target. This file CREATES a
--       new one, and MySQL has no DROP INDEX IF EXISTS (that is MariaDB) — so
--       DROP-then-ADD would fail with ERROR 1091 on every clean schema, trading a
--       crash-only failure for an always failure.
--
--       MySQL has no ADD INDEX IF NOT EXISTS either, so the guard is explicit:
--       look the index up in information_schema and prepare either the ALTER or a
--       no-op. Idempotent in both directions and on any 8.0, at the cost of being
--       four statements instead of one.
--
-- Backs the latest-review-per-plugin lookup that derives the displayed status on
-- the "my publishes" list. The pending EXISTS is already served by
-- idx_review_plugin_status_version; this one serves the ORDER BY submitted_at.
SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.STATISTICS
     WHERE table_schema = DATABASE()
       AND table_name = 'plugin_review_requests'
       AND index_name = 'idx_review_plugin_submitted_at') > 0,
  'DO 0',
  'ALTER TABLE `plugin_review_requests` ADD INDEX `idx_review_plugin_submitted_at` (`plugin_id`, `submitted_at`), ALGORITHM=INPLACE, LOCK=NONE');
PREPARE add_submitted_index FROM @ddl;
EXECUTE add_submitted_index;
DEALLOCATE PREPARE add_submitted_index;

-- Collision (1)'s leftover. Present only on environments that ran an intermediate
-- head of this branch, so the drop must be conditional in both directions: a clean
-- install has nothing to drop, and a replay after the drop has nothing either.
SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.STATISTICS
     WHERE table_schema = DATABASE()
       AND table_name = 'plugin_review_requests'
       AND index_name = 'idx_review_plugin_submitted') > 0,
  'ALTER TABLE `plugin_review_requests` DROP INDEX `idx_review_plugin_submitted`, ALGORITHM=INPLACE, LOCK=NONE',
  'DO 0');
PREPARE drop_stale_submitted_index FROM @ddl;
EXECUTE drop_stale_submitted_index;
DEALLOCATE PREPARE drop_stale_submitted_index;

-- +migrate Down
-- Drops the only index this file can have left behind. The stale
-- `idx_review_plugin_submitted` is NOT recreated: it was never part of any
-- released schema, only of an intermediate head of this branch, so restoring it
-- would resurrect the duplicate the Up exists to remove.
ALTER TABLE `plugin_review_requests`
  DROP INDEX `idx_review_plugin_submitted_at`,
  ALGORITHM=INPLACE, LOCK=NONE;
