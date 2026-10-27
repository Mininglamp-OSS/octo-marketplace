-- +migrate Up
-- Display-name uniqueness for marketplace Experts and Skills.
--
-- The normalized key is computed in Go (NFKC + trim + Unicode case fold), not
-- by MySQL LOWER/TRIM: MySQL cannot reproduce NFKC or Unicode case folding.
-- Existing rows are deliberately nullable so this migration can ship before the
-- backfill command has normalized historical names; the unique index takes
-- effect for every newly written row and for rows filled by the backfill.
ALTER TABLE plugins
  ADD COLUMN name_key VARCHAR(160) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL DEFAULT NULL,
  ADD COLUMN published_name_key VARCHAR(160) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL DEFAULT NULL,
  ADD COLUMN name_scope VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  ADD COLUMN active_name_key VARCHAR(160) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin GENERATED ALWAYS AS (
    CASE
      WHEN deleted_at IS NULL
       AND COALESCE(published_name_key,name_key) IS NOT NULL
       AND is_embedded = 0
       AND plugin_type IN ('expert','skill')
      THEN COALESCE(published_name_key,name_key)
      ELSE NULL
    END
  ) STORED,
  ADD UNIQUE KEY uq_plugins_active_name (name_scope, plugin_type, active_name_key);

-- The name_free flag is intentionally not added: soft deletion is represented by
-- deleted_at, and active_name_key becomes NULL for deleted rows. Delisted rows
-- keep deleted_at NULL and therefore continue to occupy their name.

-- +migrate Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Plugin name uniqueness downgrade refused';
