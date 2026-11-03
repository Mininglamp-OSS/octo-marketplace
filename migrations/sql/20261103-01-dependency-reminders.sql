-- +migrate Up
CREATE TABLE plugin_update_dismissals (
 space_id VARCHAR(64) NOT NULL, owner_uid VARCHAR(64) NOT NULL, source_id VARCHAR(64) NOT NULL,
 target_id VARCHAR(64) NOT NULL, revision VARCHAR(64) NOT NULL,
 PRIMARY KEY(space_id,owner_uid,source_id,target_id,revision)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE plugin_connector_baselines (
 source_id VARCHAR(64) NOT NULL, target_id VARCHAR(64) NOT NULL, revision CHAR(64) NOT NULL,
 PRIMARY KEY(source_id,target_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE plugin_review_dependency_updates (
 review_id VARCHAR(64) NOT NULL, selection_json JSON NOT NULL,
 PRIMARY KEY(review_id), FOREIGN KEY(review_id) REFERENCES plugin_review_requests(review_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
INSERT INTO plugin_connector_baselines(source_id,target_id,revision)
SELECT DISTINCT p.plugin_id,c.plugin_id,SHA2(CONCAT(c.manifest_hash,':',c.plugin_hash),256)
FROM plugins p JOIN plugin_versions v ON v.plugin_id=p.plugin_id AND v.version_id=p.published_version_id
JOIN JSON_TABLE(v.relations_json,'$[*]' COLUMNS (
 target_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PATH '$.target_plugin_id',
 legacy_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PATH '$.TargetPluginID',
 active_status INT PATH '$.status', legacy_status INT PATH '$.Status',
 removed VARCHAR(64) PATH '$.deleted_at', legacy_removed VARCHAR(64) PATH '$.DeletedAt')) e
JOIN plugins c ON c.plugin_id=COALESCE(e.target_id,e.legacy_id) AND c.plugin_type='connector'
WHERE p.plugin_type='expert' AND COALESCE(e.active_status,e.legacy_status,1)=1
AND COALESCE(e.removed,e.legacy_removed) IS NULL;
-- +migrate Down
DROP TABLE plugin_review_dependency_updates;
DROP TABLE plugin_connector_baselines;
DROP TABLE plugin_update_dismissals;
