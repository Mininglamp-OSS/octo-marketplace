-- +migrate Up

ALTER TABLE skills
  ADD COLUMN is_deleted TINYINT(1) NOT NULL DEFAULT 0 AFTER updated_at,
  ADD COLUMN name_live VARCHAR(128)
    GENERATED ALWAYS AS (IF(is_deleted = 0, name, NULL)) STORED AFTER is_deleted,
  DROP INDEX uq_skill_owner_space_name,
  ADD UNIQUE KEY uq_skill_owner_space_name_live (owner_id, space_id, name_live);

-- +migrate Down

ALTER TABLE skills
  DROP INDEX uq_skill_owner_space_name_live,
  DROP COLUMN name_live,
  DROP COLUMN is_deleted,
  ADD UNIQUE KEY uq_skill_owner_space_name (owner_id, space_id, name);
