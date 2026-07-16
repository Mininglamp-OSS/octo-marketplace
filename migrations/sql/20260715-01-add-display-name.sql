-- +migrate Up
-- Add display_name column to skills table
ALTER TABLE skills ADD COLUMN display_name VARCHAR(128) NOT NULL DEFAULT '' AFTER name;

-- +migrate Down
ALTER TABLE skills DROP COLUMN display_name;
