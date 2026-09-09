-- +migrate Up
-- Plugin space-review: add plugin_review_requests (frozen submissions waiting
-- for Space owner/admin approval) and plugin_card_action_receipts (idempotency
-- records for the IM card-action callback from octo-server).
--
-- Review state is deliberately an independent entity, NOT a column on plugins,
-- so a listed v1 and an in-review v2 can coexist and an admin metadata edit can
-- never change a tenant plugin's visibility.
--
-- The submission snapshot lives HERE, not in plugin_versions. plugin_versions
-- .version is a per-plugin auto-increment counter written by snapshotVersion
-- ("1", "2", ...), not a semver label; writing an applicant-typed label into it
-- would corrupt that sequence. Keeping the snapshot on the request also means a
-- rejected submission never occupies (plugin_id, version), so a fixed
-- resubmission may reuse the same label.
--
-- REPLAY SAFETY: this file carries TWO CREATE TABLE statements, and MySQL
-- implicitly commits each DDL, so the sql-migrate transaction protects nothing.
-- If the first CREATE commits and the process dies before the second statement
-- or before the migration-record insert, the next boot REPLAYS the whole file.
-- Both statements are therefore `CREATE TABLE IF NOT EXISTS`, which makes each
-- one a no-op against an already-created table instead of ERROR 1050 (table
-- already exists) — an error that strands the boot until a human edits
-- gorp_migrations by hand. This mirrors the `DROP TABLE IF EXISTS` already used
-- in the Down section below. The tables were split in the same order as their
-- FK dependency (receipts references requests), so a partial replay always
-- re-runs a whole, still-valid step.
--
-- The file is NOT split into two single-DDL files (the discipline later files
-- such as 20260902-03 follow) because it has ALREADY BEEN APPLIED on shared and
-- dev databases: renumbering would re-run against heads that recorded this id.
-- `IF NOT EXISTS` is a pure idempotency addition — the resulting schema, column
-- set, indexes, generated column, checks, charset and collation are unchanged.
-- The known cost of `IF NOT EXISTS` is that it also silently accepts a
-- PRE-EXISTING table of a DIFFERENT shape; that is acceptable here because these
-- two names are owned by this migration and created nowhere else.

CREATE TABLE IF NOT EXISTS `plugin_review_requests` (
  `review_id`          VARCHAR(64)   NOT NULL,
  `plugin_id`          VARCHAR(64)   NOT NULL,
  `space_id`           VARCHAR(64)   NOT NULL,
  `target_scope`       VARCHAR(16)   NOT NULL DEFAULT 'space',
  `status`             VARCHAR(16)   NOT NULL,
  `kind`               VARCHAR(16)   NOT NULL,
  `version`            VARCHAR(64)   NOT NULL,
  `changelog`          TEXT          NULL,
  `manifest_json`      JSON          NOT NULL,
  `plugin_json`        JSON          NOT NULL,
  -- The relation graph frozen alongside the documents. Without it an
  -- expert/expert_team approval would ship the reviewed manifest next to
  -- whatever the LIVE membership happened to be at approve time.
  `relations_json`     JSON          NOT NULL,
  `manifest_hash`      CHAR(71)      NOT NULL,
  `plugin_hash`        CHAR(71)      NOT NULL,
  `applicant_uid`      VARCHAR(64)   NOT NULL,
  `applicant_name`     VARCHAR(128)  NOT NULL,
  `reviewer_uid`       VARCHAR(64)   NULL DEFAULT NULL,
  `reviewer_name`      VARCHAR(128)  NULL DEFAULT NULL,
  `reason`             TEXT          NULL,
  `decision_source`    VARCHAR(16)   NULL DEFAULT NULL,
  `submitted_at`       DATETIME(3)   NOT NULL,
  `reviewed_at`        DATETIME(3)   NULL DEFAULT NULL,
  `created_at`         DATETIME(3)   NOT NULL,
  `updated_at`         DATETIME(3)   NOT NULL,
  `deleted_at`         DATETIME(3)   NULL DEFAULT NULL,
  -- Live-pending projection backing the single-pending constraint. MySQL has no
  -- partial indexes (`CREATE UNIQUE INDEX ... WHERE` is PostgreSQL syntax), but
  -- it treats NULLs in a UNIQUE index as distinct, so a generated column that
  -- collapses to NULL for every non-pending or soft-deleted row gives the same
  -- guarantee. Same trick as 20260720-01-category-live-name-unique.sql.
  `pending_plugin_id`  VARCHAR(64)
    GENERATED ALWAYS AS (IF(`deleted_at` IS NULL AND `status` = 'pending', `plugin_id`, NULL)) STORED,
  PRIMARY KEY (`review_id`),
  -- At most one pending request per plugin. Terminal rows (approved/rejected/
  -- canceled) and soft-deleted rows project NULL and coexist freely, so history
  -- keeps every submission and a resubmission may reuse a rejected label.
  UNIQUE KEY `uq_review_plugin_pending` (`pending_plugin_id`),
  KEY `idx_review_space_status_submitted` (`space_id`, `status`, `submitted_at`),
  KEY `idx_review_applicant` (`applicant_uid`, `status`, `submitted_at`),
  -- Backs the published-label lookup: the set of labels on approved requests of
  -- one plugin is what a submit checks its label against.
  KEY `idx_review_plugin_status_version` (`plugin_id`, `status`, `version`),
  KEY `idx_review_deleted_at` (`deleted_at`),
  CONSTRAINT `fk_review_plugin` FOREIGN KEY (`plugin_id`) REFERENCES `plugins`(`plugin_id`),
  CONSTRAINT `chk_review_manifest_object` CHECK (JSON_TYPE(`manifest_json`) = 'OBJECT'),
  CONSTRAINT `chk_review_package_object` CHECK (JSON_TYPE(`plugin_json`) = 'OBJECT'),
  CONSTRAINT `chk_review_relations_array` CHECK (JSON_TYPE(`relations_json`) = 'ARRAY')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Frozen plugin submissions awaiting Space owner/admin review';

-- Card-action callback receipts (at-least-once idempotency for IM decisions).
-- event_id is a decimal-string representation of an int64 octo-server event
-- sequence (its full range exceeds JS Number.MAX_SAFE_INTEGER, so it is a string
-- on the wire and stays one here); VARCHAR(32) leaves headroom over uint64.
CREATE TABLE IF NOT EXISTS `plugin_card_action_receipts` (
  `event_id`           VARCHAR(32)   NOT NULL,
  `review_id`          VARCHAR(64)   NOT NULL,
  `decision`           VARCHAR(32)   NOT NULL,
  `stored_response`    MEDIUMTEXT    NOT NULL,
  `created_at`         DATETIME(3)   NOT NULL,
  PRIMARY KEY (`event_id`),
  KEY `idx_receipt_review` (`review_id`),
  CONSTRAINT `fk_receipt_review` FOREIGN KEY (`review_id`) REFERENCES `plugin_review_requests`(`review_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Card-action event receipts giving IM decisions at-least-once idempotency';

-- +migrate Down
DROP TABLE IF EXISTS `plugin_card_action_receipts`;
DROP TABLE IF EXISTS `plugin_review_requests`;
