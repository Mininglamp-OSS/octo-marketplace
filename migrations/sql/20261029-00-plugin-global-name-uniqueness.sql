-- +migrate Up
-- The per-Space reservation indexes cannot arbitrate two global names when
-- both writers pass the application check before either commits. Enforce the
-- effective published namespace independently of isolation and query plans.
-- Historical NULL keys remain nullable until the explicit name backfill.
ALTER TABLE plugins
  ADD COLUMN global_name_key VARCHAR(160) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin GENERATED ALWAYS AS (
    CASE
      WHEN deleted_at IS NULL AND status=1 AND is_embedded=0
       AND plugin_type IN ('expert','skill','expert_team')
       AND visibility IN ('public','system') AND listing_state='published'
      THEN COALESCE(published_name_key,name_key)
      ELSE NULL
    END
  ) STORED,
  ADD UNIQUE KEY uq_plugins_global_name (plugin_type,global_name_key);

-- +migrate Down
ALTER TABLE plugins DROP INDEX uq_plugins_global_name, DROP COLUMN global_name_key;
