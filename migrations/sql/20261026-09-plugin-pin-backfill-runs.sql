-- +migrate Up
-- P cutover pin-backfill audit. One row per source version actually visited by
-- the offline-authorized migration. The unique key is the idempotency boundary:
-- replaying the same signed plan returns the existing row instead of appending
-- a second one or moving a pin again.
--
-- This table records evidence only. It does not own pointer, release, or pin
-- state; those remain in plugin_versions/plugin_version_relation_refs.
CREATE TABLE plugin_pin_backfill_runs (
 run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 plan_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_scope CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_identity VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 batch_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 actor_id VARCHAR(200) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_plugin_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_version_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 relation_count INT UNSIGNED NOT NULL,
 pinned_count INT UNSIGNED NOT NULL,
 inserted_refs INT UNSIGNED NOT NULL,
 status ENUM('applied','noop') NOT NULL,
 created_at DATETIME(3) NOT NULL,
 applied_at DATETIME(3) NOT NULL,
 PRIMARY KEY(run_id),
 UNIQUE KEY uq_pin_backfill_plan_source (plan_digest,source_version_id),
 KEY idx_pin_backfill_source (source_plugin_id,source_version_id),
 KEY idx_pin_backfill_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
-- Isolated verification only. The pinned snapshot mutations described by these
-- audit rows are not reversed by dropping this table, so a production rollback
-- must retain the additive table rather than run this Down.
DROP TABLE plugin_pin_backfill_runs;
