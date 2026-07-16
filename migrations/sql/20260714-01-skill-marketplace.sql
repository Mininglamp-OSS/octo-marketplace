-- +migrate Up

CREATE TABLE categories (
  id VARCHAR(36) PRIMARY KEY,
  name VARCHAR(64) NOT NULL,
  icon_key VARCHAR(64) NOT NULL DEFAULT '',
  sort_order INT NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE TABLE skills (
  id VARCHAR(36) PRIMARY KEY,
  name VARCHAR(128) NOT NULL,
  description TEXT NOT NULL,
  category_id VARCHAR(36) NOT NULL,
  tags JSON NOT NULL,
  owner_id VARCHAR(64) NOT NULL,
  owner_name VARCHAR(128) NOT NULL,
  space_id VARCHAR(64) NOT NULL,
  visibility ENUM('public','space','private') NOT NULL DEFAULT 'space',
  version VARCHAR(32) NOT NULL DEFAULT '1.0.0',
  readme_content MEDIUMTEXT NOT NULL,
  file_name VARCHAR(256) NOT NULL,
  file_url VARCHAR(512) NOT NULL,
  file_size BIGINT NOT NULL DEFAULT 0,
  file_sha256 VARCHAR(64) NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_category (category_id),
  INDEX idx_owner (owner_id),
  INDEX idx_space_visibility (space_id, visibility),
  INDEX idx_created_at (created_at)
);

CREATE TABLE parse_tasks (
  id VARCHAR(36) PRIMARY KEY,
  upload_id VARCHAR(64) NOT NULL,
  file_url VARCHAR(512) NOT NULL,
  status ENUM('pending','parsing','success','failed') NOT NULL DEFAULT 'pending',
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  error_message VARCHAR(512) NOT NULL DEFAULT '',
  result_name VARCHAR(128) NOT NULL DEFAULT '',
  result_description TEXT,
  result_version VARCHAR(32) NOT NULL DEFAULT '',
  result_tags JSON,
  result_readme MEDIUMTEXT,
  owner_id VARCHAR(64) NOT NULL,
  space_id VARCHAR(64) NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_status (status),
  INDEX idx_owner (owner_id)
);

-- +migrate Down

DROP TABLE IF EXISTS parse_tasks;
DROP TABLE IF EXISTS skills;
DROP TABLE IF EXISTS categories;
