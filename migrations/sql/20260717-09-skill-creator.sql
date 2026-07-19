-- +migrate Up

ALTER TABLE skills
  ADD COLUMN creator_id VARCHAR(64) NOT NULL DEFAULT '' AFTER owner_name,
  ADD COLUMN creator_name VARCHAR(128) NOT NULL DEFAULT '' AFTER creator_id;

UPDATE skills
SET creator_id = owner_id,
    creator_name = owner_name
WHERE creator_id = '';

-- +migrate Down

ALTER TABLE skills
  DROP COLUMN creator_name,
  DROP COLUMN creator_id;
