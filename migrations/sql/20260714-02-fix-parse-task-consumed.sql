-- +migrate Up

-- Add 'consumed' status to parse_tasks enum
ALTER TABLE parse_tasks MODIFY COLUMN status ENUM('pending','parsing','success','failed','consumed') NOT NULL DEFAULT 'pending';
-- Add index for visibility-based global lookups (public skills are globally visible)
CREATE INDEX idx_visibility ON skills (visibility);

-- +migrate Down

DROP INDEX idx_visibility ON skills;
ALTER TABLE parse_tasks MODIFY COLUMN status ENUM('pending','parsing','success','failed') NOT NULL DEFAULT 'pending';
