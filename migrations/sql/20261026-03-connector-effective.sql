-- +migrate Up
CREATE TABLE plugin_connector_effective (
  plugin_id VARCHAR(64) NOT NULL PRIMARY KEY,
  effective_revision BIGINT UNSIGNED NOT NULL,
  effective_digest VARCHAR(71) NOT NULL,
  plugin_name VARCHAR(255) NOT NULL,
  tags_json JSON NOT NULL,
  tool_count INT NOT NULL DEFAULT 0,
  manifest_json JSON NOT NULL,
  plugin_json JSON NOT NULL,
  attachment_keys_json JSON NULL,
  manifest_hash VARCHAR(71) NOT NULL,
  plugin_hash VARCHAR(71) NOT NULL,
  current_version VARCHAR(64) NULL,
  visibility VARCHAR(16) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  CONSTRAINT fk_connector_effective_plugin FOREIGN KEY (plugin_id) REFERENCES plugins(plugin_id),
  CONSTRAINT chk_connector_effective_revision CHECK (effective_revision > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Before effective isolation, published connector edits were review-gated.
-- Only currently published, active rows are evidence for the initial effective.
INSERT INTO plugin_connector_effective
(plugin_id,effective_revision,effective_digest,plugin_name,tags_json,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version,visibility,updated_by,updated_at)
SELECT plugin_id,1,CONCAT('sha256:',SHA2(CONCAT('v3',CHAR(31),manifest_hash,CHAR(31),plugin_hash,CHAR(31),plugin_name,CHAR(31),tool_count,CHAR(31),COALESCE(current_version,''),CHAR(31),visibility),256)),plugin_name,tags_json,tool_count,manifest_json,plugin_json,attachment_keys_json,manifest_hash,plugin_hash,current_version,visibility,owner_uid,updated_at
FROM plugins WHERE plugin_type='connector' AND status=1 AND deleted_at IS NULL AND listing_state='published';

-- +migrate Down
-- Isolated tests only. Production rollback retains the effective records.
DROP TABLE plugin_connector_effective;
