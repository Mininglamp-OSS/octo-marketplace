-- +migrate Up

-- Administrator-created Skills use system visibility. Existing public rows
-- intentionally remain unchanged.
ALTER TABLE skills
  MODIFY COLUMN visibility ENUM('public','space','private','system') NOT NULL DEFAULT 'space';

-- +migrate Down

-- A rollback is only safe when no system Skill rows exist.
ALTER TABLE skills
  MODIFY COLUMN visibility ENUM('public','space','private') NOT NULL DEFAULT 'space';
