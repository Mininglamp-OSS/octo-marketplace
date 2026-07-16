-- +migrate Up
-- Add icon_url column to skills table for skill icon storage
ALTER TABLE skills ADD COLUMN icon_url VARCHAR(512) NOT NULL DEFAULT '' AFTER display_name;

-- +migrate Down
ALTER TABLE skills DROP COLUMN icon_url;
