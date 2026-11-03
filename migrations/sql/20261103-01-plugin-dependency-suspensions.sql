-- +migrate Up
CREATE TABLE IF NOT EXISTS plugin_dependency_suspensions (
 plugin_id VARCHAR(64) NOT NULL,
 source_plugin_id VARCHAR(64) NOT NULL,
 created_at DATETIME(3) NOT NULL,
 PRIMARY KEY (plugin_id,source_plugin_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE IF EXISTS plugin_dependency_suspensions;
