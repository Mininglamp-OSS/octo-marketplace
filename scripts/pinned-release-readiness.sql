-- Read-only structural gate for the CURRENT listed roots and their released pins.
-- Old releases not reachable from a listed root are intentionally excluded.
-- Run with a SELECT-only account. Zero blockers is required, not sufficient for
-- provider/Fleet/Daemon acceptance or permission to activate a deployment.
WITH RECURSIVE reachable (plugin_id, version_id) AS (
  SELECT plugin_id, published_version_id FROM plugins
  WHERE status=1 AND deleted_at IS NULL AND is_embedded=0
    AND listing_state='published' AND plugin_type IN ('expert','expert_team')
  UNION DISTINCT
  SELECT ref.target_plugin_id, ref.target_version_id
  FROM reachable r JOIN plugin_version_relation_refs ref
    ON ref.source_plugin_id=r.plugin_id AND ref.source_version_id=r.version_id
  WHERE ref.target_version_id IS NOT NULL
), snapshots AS (
  SELECT r.plugin_id, r.version_id, p.plugin_type, p.status, p.deleted_at,
    p.is_embedded, p.listing_state, v.snapshot_schema_version, v.artifacts_json,
    v.manifest_json, v.plugin_json, v.relations_json, rel.version_id AS released,
    coverage.snapshot_digest, coverage.relation_count,
    (SELECT COUNT(*) FROM plugin_version_relation_refs ref
      WHERE ref.source_plugin_id=r.plugin_id AND ref.source_version_id=r.version_id) AS indexed_count
  FROM reachable r
  LEFT JOIN plugins p ON p.plugin_id=r.plugin_id
  LEFT JOIN plugin_versions v ON v.plugin_id=r.plugin_id AND v.version_id=r.version_id
  LEFT JOIN plugin_version_releases rel ON rel.plugin_id=r.plugin_id AND rel.version_id=r.version_id
  LEFT JOIN plugin_version_reference_index_state coverage
    ON coverage.source_plugin_id=r.plugin_id AND coverage.source_version_id=r.version_id
), blockers AS (
  SELECT plugin_id, version_id FROM snapshots
  WHERE released IS NULL OR COALESCE(status,0)<>1 OR deleted_at IS NOT NULL
    OR COALESCE(is_embedded,1)<>0 OR listing_state NOT IN ('published','delisted')
    OR COALESCE(snapshot_schema_version,0)<>1
    OR COALESCE(JSON_TYPE(plugin_json),'NULL')<>'OBJECT'
    OR COALESCE(JSON_TYPE(manifest_json),'NULL')<>'OBJECT'
    OR COALESCE(JSON_TYPE(artifacts_json),'NULL')<>'ARRAY'
    OR COALESCE(JSON_TYPE(relations_json),'NULL')<>'ARRAY'
    OR snapshot_digest IS NULL OR relation_count<>indexed_count
    OR relation_count<>JSON_LENGTH(relations_json)
    OR snapshot_digest<>SHA2(CAST(relations_json AS CHAR CHARACTER SET utf8mb4),256)
  UNION
  SELECT s.plugin_id, s.version_id FROM snapshots s
  WHERE s.plugin_type IN ('expert','expert_team') AND (
    EXISTS (
      SELECT 1 FROM JSON_TABLE('["skills","mcp_config","mcp_servers","members","experts"]',
        '$[*]' COLUMNS(field_name VARCHAR(32) PATH '$')) fields
      WHERE COALESCE(JSON_TYPE(JSON_EXTRACT(s.plugin_json,CONCAT('$.',field_name))),'NULL')<>'NULL'
         OR COALESCE(JSON_TYPE(JSON_EXTRACT(s.manifest_json,CONCAT('$.',field_name))),'NULL')<>'NULL'
    ) OR EXISTS (
      SELECT 1 FROM JSON_TABLE(JSON_MERGE_PRESERVE(
        COALESCE(JSON_EXTRACT(s.plugin_json,'$.attachments'),JSON_ARRAY()),
        COALESCE(JSON_EXTRACT(s.manifest_json,'$.attachments'),JSON_ARRAY())), '$[*]'
        COLUMNS(file_path VARCHAR(1024) PATH '$.path')) files
      WHERE LOWER(TRIM(file_path)) IN ('mcp.json','expert/context.json')
        OR LOWER(TRIM(file_path)) LIKE 'skills/%'
        OR LOWER(TRIM(file_path)) LIKE 'members/%'
    )
  )
)
SELECT 'pinned_readiness_blockers' AS check_name, COUNT(*) AS blockers FROM blockers;
