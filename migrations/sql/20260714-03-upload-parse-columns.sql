-- +migrate Up

-- Add columns for upload metadata and reupload support
ALTER TABLE parse_tasks ADD COLUMN file_name VARCHAR(256) NOT NULL DEFAULT '' AFTER file_url;
ALTER TABLE parse_tasks ADD COLUMN file_size BIGINT NOT NULL DEFAULT 0 AFTER file_name;
ALTER TABLE parse_tasks ADD COLUMN file_sha256 VARCHAR(64) NOT NULL DEFAULT '' AFTER result_readme;
ALTER TABLE parse_tasks ADD COLUMN skill_id VARCHAR(36) NOT NULL DEFAULT '' AFTER space_id;

-- Add index for upload_id lookups
CREATE INDEX idx_upload_id ON parse_tasks (upload_id);

-- +migrate Down

DROP INDEX idx_upload_id ON parse_tasks;
ALTER TABLE parse_tasks DROP COLUMN skill_id;
ALTER TABLE parse_tasks DROP COLUMN file_sha256;
ALTER TABLE parse_tasks DROP COLUMN file_size;
ALTER TABLE parse_tasks DROP COLUMN file_name;
