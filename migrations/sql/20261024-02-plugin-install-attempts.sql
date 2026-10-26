-- +migrate Up
CREATE TABLE plugin_install_attempts (
  space_id VARCHAR(64) NOT NULL,
  caller_uid VARCHAR(64) NOT NULL,
  workspace_id VARCHAR(200) NOT NULL,
  key_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  plugin_id VARCHAR(64) NOT NULL,
  root_version_id VARCHAR(64) NOT NULL,
  adapter_version VARCHAR(16) NOT NULL,
  input_fingerprint CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  request_fingerprint CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  agent_id VARCHAR(200) NOT NULL DEFAULT '',
  team_id VARCHAR(200) NOT NULL DEFAULT '',
  completed BOOLEAN NOT NULL DEFAULT FALSE,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (space_id, caller_uid, workspace_id, key_digest)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- +migrate Down
DROP TABLE plugin_install_attempts;
