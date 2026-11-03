-- +migrate Up
CREATE TABLE IF NOT EXISTS plugin_dependency_reminder_skips (
 space_id VARCHAR(64) NOT NULL,
 uid VARCHAR(64) NOT NULL,
 plugin_id VARCHAR(64) NOT NULL,
 dependency_plugin_id VARCHAR(64) NOT NULL,
 target_version_id VARCHAR(64) NOT NULL,
 created_at DATETIME(3) NOT NULL,
 PRIMARY KEY (space_id,uid,plugin_id,dependency_plugin_id,target_version_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE IF EXISTS plugin_dependency_reminder_skips;
