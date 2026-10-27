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

CREATE TABLE plugin_name_reservations (
  plugin_id  VARCHAR(64)  NOT NULL,
  name_scope VARCHAR(64)  CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  plugin_type ENUM('expert','expert_team','skill','connector') NOT NULL,
  name_key   VARCHAR(160) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  PRIMARY KEY (plugin_id, name_key),
  UNIQUE KEY uq_plugin_name_reservation (name_scope, plugin_type, name_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin
  COMMENT='Every active display name reserved by a plugin (draft and published)';

-- +migrate StatementBegin
CREATE TRIGGER plugins_name_reservations_ai AFTER INSERT ON plugins FOR EACH ROW
INSERT INTO plugin_name_reservations(plugin_id,name_scope,plugin_type,name_key)
SELECT NEW.plugin_id,COALESCE(NEW.space_id,''),NEW.plugin_type,NEW.name_key
WHERE NEW.deleted_at IS NULL AND NEW.is_embedded=0 AND NEW.plugin_type IN ('expert','skill') AND NEW.name_key IS NOT NULL
UNION ALL
SELECT NEW.plugin_id,COALESCE(NEW.space_id,''),NEW.plugin_type,NEW.published_name_key
WHERE NEW.deleted_at IS NULL AND NEW.is_embedded=0 AND NEW.plugin_type IN ('expert','skill') AND NEW.published_name_key IS NOT NULL AND NEW.published_name_key <> NEW.name_key;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE TRIGGER plugins_name_reservations_bu BEFORE UPDATE ON plugins FOR EACH ROW
DELETE FROM plugin_name_reservations WHERE plugin_id=OLD.plugin_id;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE TRIGGER plugins_name_reservations_au AFTER UPDATE ON plugins FOR EACH ROW
INSERT INTO plugin_name_reservations(plugin_id,name_scope,plugin_type,name_key)
SELECT NEW.plugin_id,COALESCE(NEW.space_id,''),NEW.plugin_type,NEW.name_key
WHERE NEW.deleted_at IS NULL AND NEW.is_embedded=0 AND NEW.plugin_type IN ('expert','skill') AND NEW.name_key IS NOT NULL
UNION ALL
SELECT NEW.plugin_id,COALESCE(NEW.space_id,''),NEW.plugin_type,NEW.published_name_key
WHERE NEW.deleted_at IS NULL AND NEW.is_embedded=0 AND NEW.plugin_type IN ('expert','skill') AND NEW.published_name_key IS NOT NULL AND NEW.published_name_key <> NEW.name_key;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE TRIGGER plugins_name_reservations_ad AFTER DELETE ON plugins FOR EACH ROW
DELETE FROM plugin_name_reservations WHERE plugin_id=OLD.plugin_id;
-- +migrate StatementEnd

-- +migrate Down
DROP TRIGGER IF EXISTS plugins_name_reservations_ai;
DROP TRIGGER IF EXISTS plugins_name_reservations_bu;
DROP TRIGGER IF EXISTS plugins_name_reservations_au;
DROP TRIGGER IF EXISTS plugins_name_reservations_ad;
DROP TABLE IF EXISTS plugin_name_reservations;
ALTER TABLE plugins
  DROP INDEX uq_plugins_active_name,
  DROP COLUMN active_name_key,
  DROP COLUMN name_scope,
  DROP COLUMN published_name_key,
  DROP COLUMN name_key;
