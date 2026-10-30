-- +migrate Up
-- Extend normalized draft/published name reservations to standalone MCP Connectors.
-- Legacy rows remain nullable; runtime write checks also inspect their original names.
-- Historical rows are not renamed or backfilled by this schema migration.
ALTER TABLE plugins MODIFY COLUMN active_name_key VARCHAR(160) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin GENERATED ALWAYS AS (
 CASE WHEN deleted_at IS NULL AND COALESCE(published_name_key,name_key) IS NOT NULL
 AND is_embedded=0 AND plugin_type IN ('expert','skill','expert_team','connector')
 THEN COALESCE(published_name_key,name_key) ELSE NULL END
) STORED;

DROP TRIGGER IF EXISTS plugins_name_reservations_ai;
CREATE TRIGGER plugins_name_reservations_ai AFTER INSERT ON plugins FOR EACH ROW
INSERT INTO plugin_name_reservations(plugin_id,name_scope,plugin_type,name_key)
SELECT NEW.plugin_id,COALESCE(NEW.space_id,''),NEW.plugin_type,NEW.name_key
WHERE NEW.deleted_at IS NULL AND NEW.is_embedded=0 AND NEW.plugin_type IN ('expert','skill','expert_team','connector') AND NEW.name_key IS NOT NULL
UNION ALL
SELECT NEW.plugin_id,COALESCE(NEW.space_id,''),NEW.plugin_type,NEW.published_name_key
WHERE NEW.deleted_at IS NULL AND NEW.is_embedded=0 AND NEW.plugin_type IN ('expert','skill','expert_team','connector') AND NEW.published_name_key IS NOT NULL AND NEW.published_name_key <> NEW.name_key;


DROP TRIGGER IF EXISTS plugins_name_reservations_au;
CREATE TRIGGER plugins_name_reservations_au AFTER UPDATE ON plugins FOR EACH ROW
INSERT INTO plugin_name_reservations(plugin_id,name_scope,plugin_type,name_key)
SELECT NEW.plugin_id,COALESCE(NEW.space_id,''),NEW.plugin_type,NEW.name_key
WHERE NEW.deleted_at IS NULL AND NEW.is_embedded=0 AND NEW.plugin_type IN ('expert','skill','expert_team','connector') AND NEW.name_key IS NOT NULL
UNION ALL
SELECT NEW.plugin_id,COALESCE(NEW.space_id,''),NEW.plugin_type,NEW.published_name_key
WHERE NEW.deleted_at IS NULL AND NEW.is_embedded=0 AND NEW.plugin_type IN ('expert','skill','expert_team','connector') AND NEW.published_name_key IS NOT NULL AND NEW.published_name_key <> NEW.name_key;


-- Populate any Connector keys already present (e.g. a staged writer rollout).
-- No IGNORE: collisions must fail visibly and be resolved through inventory.
INSERT INTO plugin_name_reservations(plugin_id,name_scope,plugin_type,name_key)
SELECT p.plugin_id,COALESCE(p.space_id,''),p.plugin_type,p.name_key
FROM plugins p WHERE p.deleted_at IS NULL AND p.is_embedded=0 AND p.plugin_type='connector' AND p.name_key IS NOT NULL
AND NOT EXISTS (SELECT 1 FROM plugin_name_reservations r WHERE r.plugin_id=p.plugin_id AND r.name_key=p.name_key)
UNION ALL
SELECT p.plugin_id,COALESCE(p.space_id,''),p.plugin_type,p.published_name_key
FROM plugins p WHERE p.deleted_at IS NULL AND p.is_embedded=0 AND p.plugin_type='connector' AND p.published_name_key IS NOT NULL AND (p.name_key IS NULL OR p.published_name_key<>p.name_key)
AND NOT EXISTS (SELECT 1 FROM plugin_name_reservations r WHERE r.plugin_id=p.plugin_id AND r.name_key=p.published_name_key);

-- +migrate Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Connector name uniqueness downgrade refused';
