-- Synthetic data only. A query-contract fixture, not a production migration.
USE marketplace_fixture;
CREATE TABLE plugins (
  plugin_id VARCHAR(64) PRIMARY KEY, plugin_name VARCHAR(64), plugin_type VARCHAR(32),
  owner_uid VARCHAR(64), space_id VARCHAR(64), visibility VARCHAR(32), listing_state VARCHAR(32),
  is_embedded BOOLEAN, deleted_at DATETIME NULL, current_version_id VARCHAR(64) NULL,
  published_version_id VARCHAR(64) NULL, plugin_json JSON NULL, attachment_keys_json JSON NULL
);
CREATE TABLE plugin_versions (
  version_id VARCHAR(64) PRIMARY KEY, plugin_id VARCHAR(64), snapshot_schema_version INT NULL,
  plugin_json JSON NULL, attachment_keys_json JSON NULL, artifacts_json JSON NULL, relations_json JSON NULL
);
CREATE TABLE plugin_relations (
  relation_id VARCHAR(64) PRIMARY KEY, source_plugin_id VARCHAR(64), target_plugin_id VARCHAR(64),
  relation_type VARCHAR(32), deleted_at DATETIME NULL, status INT
);
CREATE TABLE plugin_version_releases (plugin_id VARCHAR(64), version_id VARCHAR(64));
INSERT INTO plugins VALUES
 ('fixture-expert','Research','expert','fixture-alice','fixture-space-a','private','published',0,NULL,'fixture-ev1','fixture-ev1',NULL,NULL),
 ('fixture-skill-a','Search','skill','fixture-alice','fixture-space-a','private','published',1,NULL,'fixture-sv1','fixture-sv1',NULL,NULL),
 ('fixture-skill-b','Search','skill','fixture-bob','fixture-space-b','private','draft',0,NULL,NULL,NULL,JSON_OBJECT('attachments',JSON_ARRAY()),JSON_ARRAY()),
 ('fixture-connector','Search MCP','connector','fixture-alice','fixture-space-a','private','published',0,NULL,NULL,NULL,NULL,NULL),
 ('fixture-space','Space MCP','connector','fixture-alice','fixture-space-a','space','published',0,NULL,NULL,NULL,JSON_OBJECT('attachments',JSON_ARRAY()),NULL),
 ('fixture-system','System MCP','connector','fixture-alice','fixture-space-a','system','draft',0,NULL,NULL,NULL,JSON_OBJECT('attachments',JSON_ARRAY()),NULL),
 ('fixture-legacy-public','Legacy MCP','connector','fixture-alice','fixture-space-a','public','delisted',0,NULL,NULL,NULL,JSON_OBJECT('attachments',JSON_ARRAY()),NULL),
 ('fixture-unknown','Unknown MCP','connector','fixture-alice','fixture-space-a','other','other',0,NULL,NULL,NULL,JSON_OBJECT('attachments',JSON_ARRAY()),NULL);
INSERT INTO plugin_versions VALUES
 ('fixture-ev1','fixture-expert',1,NULL,NULL,JSON_ARRAY(),JSON_ARRAY(JSON_OBJECT('target_plugin_id','fixture-skill-a','target_version_id','fixture-sv1'),JSON_OBJECT('target_plugin_id','fixture-connector'))),
 ('fixture-sv1','fixture-skill-a',1,NULL,NULL,JSON_ARRAY(),JSON_ARRAY());
INSERT INTO plugin_relations VALUES
 ('fixture-r1','fixture-expert','fixture-skill-a','expert_skill',NULL,1),
 ('fixture-r2','fixture-expert','fixture-connector','expert_connector',NULL,1);
INSERT INTO plugin_version_releases VALUES ('fixture-expert','fixture-ev1'),('fixture-skill-a','fixture-sv1');
REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'inventory'@'%';
GRANT SELECT ON marketplace_fixture.* TO 'inventory'@'%';
